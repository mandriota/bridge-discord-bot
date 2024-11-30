package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"unicode"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/disgo/webhook"
	"github.com/disgoorg/json"
	"github.com/disgoorg/snowflake/v2"
	_ "github.com/mattn/go-sqlite3"
)

const ForwarderHookName = "ForwarderHook"

const MaxAttachmentDownloadSize = (1 << 20) * 10

type Handler struct {
	ctx context.Context

	rest           rest.Rest
	db             *sql.DB
	recentDelCache sync.Map
}

func (h *Handler) initDB(filePath string) (err error) {
	h.db, err = sql.Open("sqlite3", filePath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}

	tx, err := h.db.BeginTx(h.ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := createMessagesTable(h.ctx, tx); err != nil {
		return fmt.Errorf("failed to create table: %w", err)
	}

	if err := createAuthorsTable(h.ctx, tx); err != nil {
		return fmt.Errorf("failed to create table: %w", err)
	}

	if err := createLinksTable(h.ctx, tx); err != nil {
		return fmt.Errorf("failed to create table: %w", err)
	}
	return tx.Commit()
}

func (h *Handler) initCommands(appID snowflake.ID) error {
	commands := []discord.ApplicationCommandCreate{
		discord.SlashCommandCreate{
			Name:        "link",
			Description: "links current channel to virtual channel",
			Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionString{
					Name:        "virtual_channel_key",
					Description: "virtual channel key to link to",
					Required:    true,
				},
				discord.ApplicationCommandOptionString{
					Name:        "note",
					Description: "note about virtual channel",
				},
			},
			DefaultMemberPermissions: json.NewNullablePtr(discord.PermissionManageChannels),
			Contexts: []discord.InteractionContextType{discord.InteractionContextTypeGuild},
		},
		discord.SlashCommandCreate{
			Name:        "unlink",
			Description: "unlinks current channel from virtual channel",
			Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionString{
					Name:        "virtual_channel_key",
					Description: "virtual channel key to unlink from",
					Required:    true,
				},
			},
		},
		discord.SlashCommandCreate{
			Name:        "unlink_all",
			Description: "unlinks current channel from all virtual channels",
		},
		discord.SlashCommandCreate{
			Name:        "list",
			Description: "list virtual channels linked to current channel",
		},
	}

	_, err := h.rest.SetGlobalCommands(appID, commands)
	return err
}

func (h *Handler) loadOrCreateWebhook(client bot.Client, channelID snowflake.ID) (*discord.IncomingWebhook, error) {
	webhooks, err := client.Rest().GetWebhooks(channelID)
	if err != nil {
		return nil, err
	}

	for _, webhook := range webhooks {
		if incomingWebhook, ok := webhook.(discord.IncomingWebhook); ok && optionToTypeOrZero(incomingWebhook.ApplicationID) == client.ApplicationID() {
			return &incomingWebhook, nil
		}
	}

	return client.Rest().CreateWebhook(channelID, discord.WebhookCreate{
		Name: ForwarderHookName,
	})
}

func (h *Handler) tryWriteReferenceHeader(w *strings.Builder, targetGuildID, targetChannelID snowflake.ID, msgRef *discord.MessageReference) error {
	if msgRef == nil {
		return nil
	}

	relatedMsgID, err := loadDirelatedMessageID(h.ctx, h.db, targetChannelID, *msgRef.MessageID)
	if err != nil {
		return err
	}

	referredMsg, err := h.rest.GetMessage(*msgRef.ChannelID, *msgRef.MessageID)
	if err != nil {
		return err
	}

	referredMsgAuthorID, err := loadAuthorID(h.ctx, h.db, referredMsg.Author.Username)
	if err != nil {
		return err
	}

	referredMsgPreview := referredMsg.Content[skipPrefixedLine(referredMsg.Content, "-#"):]
	cutIndicator := ""
	referredMsgPreviewWindow := nthRune(referredMsgPreview, 128)
	if referredMsgPreviewWindow < len(referredMsgPreview) {
		cutIndicator = " **. . .**"
		referredMsgPreview = referredMsgPreview[:referredMsgPreviewWindow]

		lastSpace := strings.LastIndexFunc(referredMsgPreview, unicode.IsSpace)
		if lastSpace > 0 {
			referredMsgPreview = referredMsgPreview[:lastSpace]
		}
	}
	referredMsgPreview = strings.TrimRightFunc(referredMsgPreview, unicode.IsSpace)

	w.WriteString("-# ↵ https://discord.com/channels/")
	w.WriteString(targetGuildID.String())
	w.WriteByte('/')
	w.WriteString(targetChannelID.String())
	w.WriteByte('/')
	w.WriteString(relatedMsgID.String())
	w.WriteString(" (<@")
	w.WriteString(referredMsgAuthorID.String())
	w.WriteString(">)\n-# > ")
	w.WriteString(referredMsgPreview)
	w.WriteString(cutIndicator)
	w.WriteByte('\n')

	return nil
}

func (h *Handler) processMessageAttachments(e *events.GenericGuildMessage, onlyFooter bool) (footer string, attach []uint8, bodies [][]byte) {
	contentCommonFooter := strings.Builder{}
	contentCommonFileAttach := []uint8{}
	contentCommonFileBodies := [][]byte{}

	for i, attach := range e.Message.Attachments {
		if attach.Size > MaxAttachmentDownloadSize {
			contentCommonFooter.WriteByte('\n')
			contentCommonFooter.WriteString(attach.URL)
			continue
		}

		if onlyFooter {
			continue
		}

		resp, err := http.Get(attach.URL)
		if err != nil {
			e.Client().Logger().Error("failed to download attachment", "error", err)
			continue
		}

		text, err := io.ReadAll(resp.Body)
		if err != nil {
			e.Client().Logger().Error("failed to download attachment", "error", err)
		} else {
			contentCommonFileBodies = append(contentCommonFileBodies, text)
			contentCommonFileAttach = append(contentCommonFileAttach, uint8(i))
		}
		resp.Body.Close()
	}
	return contentCommonFooter.String(), contentCommonFileAttach, contentCommonFileBodies
}

func (h *Handler) onCommandInteractionCreateLink(e *events.ApplicationCommandInteractionCreate, commandData discord.SlashCommandInteractionData) {
	virtualChannelKey := commandData.String("virtual_channel_key")
	note := commandData.String("note")

	hash := sha256.Sum256([]byte(virtualChannelKey))
	virtualChannelHash := hex.EncodeToString(hash[:])

	query, args := buildInsertLinkQuery(virtualChannelHash, e.Channel().ID(), note)
	_, err := h.db.Exec(query, args...)
	if err != nil {
		e.Client().Logger().Error("failed to link channel to virtual channel key", "error", err)
		err := e.CreateMessage(discord.NewMessageCreateBuilder().
			SetEmbeds(discord.Embed{
				Title:       "Error",
				Description: "Could not link the channel.",
				Color:       0xFF0000,
			}).
			SetEphemeral(true).
			Build(),
		)
		if err != nil {
			e.Client().Logger().Error("error sending response", "error", err)
		}
		return
	}

	if err := e.CreateMessage(discord.NewMessageCreateBuilder().
		SetContent(fmt.Sprintf("Channel successfully linked to virtual channel `%s`.", virtualChannelHash)).
		SetEphemeral(true).
		Build(),
	); err != nil {
		e.Client().Logger().Error("error sending response", "error", err)
	}
}

func (h *Handler) onCommandInteractionCreateUnlink(e *events.ApplicationCommandInteractionCreate, commandData discord.SlashCommandInteractionData) {
	virtualChannelKey := commandData.String("virtual_channel_key")

	query, args := buildDeleteLinkQuery(virtualChannelKey, e.Channel().ID())
	res, err := h.db.Exec(query, args...)
	if err != nil {
		e.Client().Logger().Error("failed to unlink channel from virtual channel key", "error", err)
		e.CreateMessage(discord.NewMessageCreateBuilder().
			SetEmbeds(discord.Embed{
				Title:       "Error",
				Description: "Could not unlink the channel.",
				Color:       0xFF0000,
			}).
			SetEphemeral(true).
			Build(),
		)
		return
	}

	rowsAffected, _ := res.RowsAffected()
	if rowsAffected == 0 {
		e.CreateMessage(discord.NewMessageCreateBuilder().
			SetEmbeds(discord.Embed{
				Title:       "Error",
				Description: fmt.Sprintf("No link found for virtual channel key `%s`.", virtualChannelKey),
				Color:       0xFF0000,
			}).
			SetEphemeral(true).
			Build(),
		)
		return
	}

	e.CreateMessage(discord.NewMessageCreateBuilder().
		SetEmbeds(discord.Embed{
			Title:       "Success",
			Description: fmt.Sprintf("Channel successfully unlinked from virtual channel key `%s`.", virtualChannelKey),
			Color:       0x00FF00,
		}).
		SetEphemeral(true).
		Build(),
	)
}

func (h *Handler) onCommandInteractionCreateUnlinkAll(e *events.ApplicationCommandInteractionCreate, _ discord.SlashCommandInteractionData) {
	query, args := buildDeleteAllLinksQuery(e.Channel().ID())

	res, err := h.db.Exec(query, args...)
	if err != nil {
		e.Client().Logger().Error("failed to unlink all virtual channels for the channel", "error", err)
		e.CreateMessage(discord.NewMessageCreateBuilder().
			SetEmbeds(discord.Embed{
				Title:       "Error",
				Description: "Could not unlink all virtual channels.",
				Color:       0xFF0000,
			}).
			SetEphemeral(true).
			Build(),
		)
		return
	}

	rowsAffected, _ := res.RowsAffected()
	if rowsAffected == 0 {
		e.CreateMessage(discord.NewMessageCreateBuilder().
			SetEmbeds(discord.Embed{
				Title:       "No Links Found",
				Description: "No links found for this channel.",
				Color:       0xFF0000,
			}).
			SetEphemeral(true).
			Build(),
		)
		return
	}

	e.CreateMessage(discord.NewMessageCreateBuilder().
		SetEmbeds(discord.Embed{
			Title:       "Success",
			Description: fmt.Sprintf("Successfully unlinked %d virtual channel(s) from this channel.", rowsAffected),
			Color:       0x00FF00,
		}).
		SetEphemeral(true).
		Build(),
	)
}

func (h *Handler) onCommandInteractionCreateList(e *events.ApplicationCommandInteractionCreate, _ discord.SlashCommandInteractionData) {
	query, args := buildSelectVirtualChannelKeyQuery(e.Channel().ID())

	rows, err := h.db.Query(query, args...)
	if err != nil {
		e.Client().Logger().Error("failed to list virtual channels for the channel", "error", err)
		e.CreateMessage(discord.NewMessageCreateBuilder().
			SetEmbeds(discord.Embed{
				Title:       "Error",
				Description: "Could not retrieve the list of virtual channels.",
				Color:       0xFF0000,
			}).
			SetEphemeral(true).
			Build(),
		)
		return
	}
	defer rows.Close()

	virtualChannelKey := ""
	note := ""

	sb := strings.Builder{}
	for rows.Next() {
		if err := rows.Scan(&virtualChannelKey, &note); err != nil {
			e.Client().Logger().Error("failed to scan virtual channel key", "error", err)
			continue
		}
		sb.WriteString("- `")
		sb.WriteString(virtualChannelKey)
		sb.WriteByte('`')
		if note != "" {
			sb.WriteString(" (note: ")
			sb.WriteString(note)
			sb.WriteByte(')')
		}
		sb.WriteByte('\n')
	}

	if sb.Len() == 0 {
		e.CreateMessage(discord.NewMessageCreateBuilder().
			SetEmbeds(discord.Embed{
				Title:       "No Virtual Channels Linked",
				Description: "No virtual channels are linked to this channel.",
				Color:       0xFF0000,
			}).
			SetEphemeral(true).
			Build(),
		)
		return
	}

	e.CreateMessage(discord.NewMessageCreateBuilder().
		SetEmbeds(discord.Embed{
			Title:       "Virtual Channels Linked",
			Description: fmt.Sprintf("Virtual channels linked to this channel:\n%s", sb.String()),
			Color:       0x00FF00,
		}).
		SetEphemeral(true).
		Build(),
	)
}

func (h *Handler) onCommandInteractionCreate(e *events.ApplicationCommandInteractionCreate) {
	commandData := e.SlashCommandInteractionData()

	switch commandData.CommandName() {
	case "link":
		h.onCommandInteractionCreateLink(e, commandData)
	case "unlink":
		h.onCommandInteractionCreateUnlink(e, commandData)
	case "unlink_all":
		h.onCommandInteractionCreateUnlinkAll(e, commandData)
	case "list":
		h.onCommandInteractionCreateList(e, commandData)
	}
}

func (h *Handler) onMessageCreate(e *events.GuildMessageCreate) {
	if e.Message.Author.Bot {
		return
	}

	targetChannels, err := loadRelatedChannels(h.ctx, h.db, e.ChannelID)
	if err != nil {
		e.Client().Logger().Error("failed to load related channels", "error", err)
		return
	}

	tx, err := h.db.BeginTx(h.ctx, nil)
	if err != nil {
		e.Client().Logger().Error("failed to begin transaction", "error", err)
		return
	}
	defer tx.Rollback()

	if err := saveAuthorMapping(h.ctx, tx, e.Message.Author.Username, e.Message.Author.ID); err != nil {
		e.Client().Logger().Error("failed to save author mapping", "error", err)
	}

	contentCommonFooter, contentCommonFileAttach, contentCommonFileBodies := h.processMessageAttachments(e.GenericGuildMessage, false)

	for _, targetChannelID := range targetChannels {
		forwarderWebhook, err := h.loadOrCreateWebhook(e.Client(), targetChannelID)
		if err != nil {
			e.Client().Logger().Error("failed to get/create webhook", "error", err)
			continue
		}

		content := &strings.Builder{}
		if err := h.tryWriteReferenceHeader(content, forwarderWebhook.GuildID, targetChannelID, e.Message.MessageReference); err != nil {
			e.Client().Logger().Error("failed to fetch hook message ID", "error", err)
		}
		content.WriteString(e.Message.Content)
		content.WriteString(contentCommonFooter)

		if content.Len() == 0 && len(contentCommonFileAttach) == 0 {
			e.Client().Logger().Error("unsupported message")
			continue
		}

		messageBuilder := discord.NewWebhookMessageCreateBuilder().
			SetAllowedMentions(&discord.AllowedMentions{}).
			SetUsername(e.Message.Author.Username).
			SetContent(content.String())

		if url := e.Message.Author.AvatarURL(); url != nil {
			messageBuilder.SetAvatarURL(*url)
		} else {
			messageBuilder.SetAvatarURL(fmt.Sprintf("%s/embed/avatars/%d.png", discord.CDN, e.Message.Author.ID))
		}

		for i, attachDownloaded := range contentCommonFileAttach {
			attach := e.Message.Attachments[attachDownloaded]
			messageBuilder.AddFile(attach.Filename, optionToTypeOrZero(attach.Description), bytes.NewReader(contentCommonFileBodies[i]))
		}

		forwarderClient := webhook.New(forwarderWebhook.ID(), forwarderWebhook.Token)
		webhookMessage, err := forwarderClient.CreateMessage(messageBuilder.Build())
		if err != nil {
			e.Client().Logger().Error("failed to send message via webhook", "error", err)
		}
		forwarderClient.Close(h.ctx)

		if err := saveMessageMapping(h.ctx, tx, e.Message.ChannelID, e.MessageID, webhookMessage.ChannelID, webhookMessage.ID); err != nil {
			e.Client().Logger().Error("failed to save message mapping", "error", err)
		}
	}

	if err := tx.Commit(); err != nil {
		e.Client().Logger().Error("failed to commit transaction", "error", err)
		return
	}
}

func (h *Handler) onMessageUpdate(e *events.GuildMessageUpdate) {
	if e.Message.Author.Bot {
		return
	}

	targetChannels, err := loadRelatedChannels(h.ctx, h.db, e.ChannelID)
	if err != nil {
		e.Client().Logger().Error("failed to load related channels", "error", err)
		return
	}

	contentCommonFooter, _, _ := h.processMessageAttachments(e.GenericGuildMessage, true)

	for _, targetChannelID := range targetChannels {
		relatedMessageID, err := loadRelatedMessageID(h.ctx, h.db, targetChannelID, e.MessageID)
		if err != nil {
			e.Client().Logger().Error("failed to fetch related message ID for update", "error", err)
			continue
		}

		forwarderWebhook, err := h.loadOrCreateWebhook(e.Client(), targetChannelID)
		if err != nil {
			e.Client().Logger().Error("failed to load or create webhook", "error", err)
			continue
		}

		content := &strings.Builder{}
		if err := h.tryWriteReferenceHeader(content, forwarderWebhook.GuildID, targetChannelID, e.Message.MessageReference); err != nil {
			e.Client().Logger().Error("failed to fetch hook message ID", "error", err)
		}
		content.WriteString(e.Message.Content)
		content.WriteString(contentCommonFooter)

		messageBuilder := discord.NewWebhookMessageUpdateBuilder().
			SetContent(content.String())

		forwarderClient := webhook.New(forwarderWebhook.ID(), forwarderWebhook.Token)

		if _, err := forwarderClient.UpdateMessage(relatedMessageID, messageBuilder.Build()); err != nil {
			e.Client().Logger().Error("failed to update forwarded message via webhook", "error", err)
		}

		forwarderClient.Close(h.ctx)
	}
}

func (h *Handler) onMessageDelete(e *events.GuildMessageDelete) {
	if e.Message.Author.Bot {
		return
	}

	if _, ok := h.recentDelCache.LoadAndDelete(e.MessageID); ok {
		return
	}

	targetChannels, err := loadRelatedChannels(h.ctx, h.db, e.ChannelID)
	if err != nil {
		e.Client().Logger().Error("failed to load related channels", "error", err)
		return
	}

	for _, targetChannelID := range targetChannels {
		relatedMessageID, err := loadRelatedMessageID(h.ctx, h.db, targetChannelID, e.MessageID)
		if err != nil {
			e.Client().Logger().Error("failed to fetch related message ID for deletion", "error", err)
			continue
		}

		forwarderWebhook, err := h.loadOrCreateWebhook(e.Client(), targetChannelID)
		if err != nil {
			e.Client().Logger().Error("failed to load or create webhook", "error", err)
			continue
		}

		forwarderClient := webhook.New(forwarderWebhook.ID(), forwarderWebhook.Token)

		if err := forwarderClient.DeleteMessage(relatedMessageID); err != nil {
			e.Client().Logger().Error("failed to delete forwarded message via webhook", "error", err)
		} else {
			h.recentDelCache.Store(relatedMessageID, nil)
		}

		forwarderClient.Close(h.ctx)
	}
}

func main() {
	ctx := context.Background()
	handler := Handler{ctx: ctx}

	slog.Info("initializating database...")

	if err := handler.initDB("messages.db"); err != nil {
		slog.Error("failed to initialize database", "error", err)
		return
	}
	defer handler.db.Close()

	client, err := disgo.New(os.Getenv("BRIDGE_BOT_TOKEN"),
		bot.WithGatewayConfigOpts(
			gateway.WithIntents(
				gateway.IntentGuilds,
				gateway.IntentGuildMessages,
				gateway.IntentGuildExpressions,
				gateway.IntentMessageContent,
			),
		),
		bot.WithEventListenerFunc(handler.onCommandInteractionCreate),
		bot.WithEventListenerFunc(handler.onMessageCreate),
		bot.WithEventListenerFunc(handler.onMessageUpdate),
		bot.WithEventListenerFunc(handler.onMessageDelete),
	)
	if err != nil {
		slog.Error("failed to create client", "error", err)
		return
	}
	defer client.Close(ctx)

	handler.rest = client.Rest()

	slog.Info("opening gateway...")

	if err = client.OpenGateway(ctx); err != nil {
		slog.Error("failed to open gateway", "error", err)
		return
	}

	handler.initCommands(client.ApplicationID())

	slog.Info("listening...")

	notifyCtx, _ := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	<-notifyCtx.Done()
}
