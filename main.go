package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/webhook"
	"github.com/disgoorg/snowflake/v2"
	"github.com/huandu/go-sqlbuilder"
	_ "github.com/mattn/go-sqlite3"
)

type Config map[string][]string

const ForwarderHookName = "ForwarderHook"

const MaxAttachmentDownloadSize = 1 << 15

func loadConfig(cfg *Config, filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	if err := json.NewDecoder(file).Decode(cfg); err != nil {
		return err
	}
	return nil
}

type Handler struct {
	ctx context.Context
	cfg Config

	db             *sql.DB
	recentDelCache sync.Map
}

func (h *Handler) initDB(filePath string) error {
	var err error
	h.db, err = sql.Open("sqlite3", filePath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}

	createMessagesTableQuery, _ := sqlbuilder.CreateTable("messages").
		IfNotExists().
		Define("original_channel_id", "TEXT", "NOT NULL").
		Define("original_message_id", "TEXT", "NOT NULL").
		Define("hook_channel_id", "TEXT", "NOT NULL").
		Define("hook_message_id", "TEXT", "NOT NULL").
		Define("PRIMARY KEY", "(original_channel_id, original_message_id, hook_channel_id, hook_message_id)").
		BuildWithFlavor(sqlbuilder.SQLite)

	if _, err := h.db.Exec(createMessagesTableQuery); err != nil {
		return fmt.Errorf("failed to create table: %w", err)
	}
	return nil
}

func (h *Handler) loadRelatedMessageID(targetChannelID, messageRef string) (related string, err error) {
	selectB := sqlbuilder.NewSelectBuilder()
	selectB.Select(selectB.As("hook_message_id", "related_message_id")).
		From("messages").
		Where(
			selectB.And(
				selectB.Equal("hook_channel_id", targetChannelID),
				selectB.Equal("original_message_id", messageRef),
			),
		)

	query, args := selectB.BuildWithFlavor(sqlbuilder.SQLite)
	return related, h.db.QueryRow(query, args...).Scan(&related)
}

func (h *Handler) loadDirelatedMessageID(targetChannelID, messageRef string) (related string, err error) {
	selectBL := sqlbuilder.NewSelectBuilder()
	selectBL.Select(selectBL.As("original_message_id", "related_message_id")).
		From("messages").
		Where(
			selectBL.And(
				selectBL.Equal("original_channel_id", targetChannelID),
				selectBL.Equal("hook_message_id", messageRef),
			),
		)

	selectBR := sqlbuilder.NewSelectBuilder()
	selectBR.Select(selectBR.As("hook_message_id", "related_message_id")).
		From("messages").
		Where(
			selectBR.And(
				selectBR.Equal("hook_channel_id", targetChannelID),
				selectBR.Equal("original_message_id", messageRef),
			),
		)

	query, args := sqlbuilder.Union(selectBL, selectBR).BuildWithFlavor(sqlbuilder.SQLite)
	return related, h.db.QueryRow(query, args...).Scan(&related)
}

func (h *Handler) saveMessageMapping(originalChannelID, originalID, hookChannelID, hookID string) error {
	query, args := sqlbuilder.SQLite.NewInsertBuilder().
		InsertIgnoreInto("messages").
		Cols("original_channel_id", "original_message_id", "hook_channel_id", "hook_message_id").
		Values(originalChannelID, originalID, hookChannelID, hookID).
		Build()

	_, err := h.db.Exec(query, args...)
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

func (h *Handler) tryWriteReferenceHeader(w *strings.Builder, targetGuildIDText, targetChannelIDText string, messageRef *discord.MessageReference) error {
	if messageRef == nil {
		return nil
	}

	relatedMessageID, err := h.loadDirelatedMessageID(targetChannelIDText, messageRef.MessageID.String())
	if err != nil {
		return err
	}

	w.WriteString("-# in reply to: https://discord.com/channels/")
	w.WriteString(targetGuildIDText)
	w.WriteByte('/')
	w.WriteString(targetChannelIDText)
	w.WriteByte('/')
	w.WriteString(relatedMessageID)
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

func (h *Handler) onMessageCreate(e *events.GuildMessageCreate) {
	if e.Message.Author.Bot {
		return
	}

	targetChannels, ok := h.cfg[e.ChannelID.String()]
	if !ok {
		return
	}

	contentCommonFooter, contentCommonFileAttach, contentCommonFileBodies := h.processMessageAttachments(e.GenericGuildMessage, false)

	for _, targetChannelIDText := range targetChannels {
		targetChannelID, err := snowflake.Parse(targetChannelIDText)
		if err != nil {
			e.Client().Logger().Error("failed to parse channel ID", "error", err)
			continue
		}

		forwarderWebhook, err := h.loadOrCreateWebhook(e.Client(), targetChannelID)
		if err != nil {
			e.Client().Logger().Error("failed to get/create webhook", "error", err)
			continue
		}

		content := &strings.Builder{}
		if err := h.tryWriteReferenceHeader(content, forwarderWebhook.GuildID.String(), targetChannelIDText, e.Message.MessageReference); err != nil {
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
			SetAvatarURL(*e.Message.Author.AvatarURL()).
			SetUsername(e.Message.Author.Username).
			SetContent(content.String())

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

		if err := h.saveMessageMapping(e.Message.ChannelID.String(), e.MessageID.String(), webhookMessage.ChannelID.String(), webhookMessage.ID.String()); err != nil {
			e.Client().Logger().Error("failed to save message mapping", "error", err)
		}
	}
}

func (h *Handler) onMessageUpdate(e *events.GuildMessageUpdate) {
	if e.Message.Author.Bot {
		return
	}

	targetChannels, ok := h.cfg[e.ChannelID.String()]
	if !ok {
		return
	}

	contentCommonFooter, _, _ := h.processMessageAttachments(e.GenericGuildMessage, true)

	for _, targetChannelIDText := range targetChannels {
		relatedMessageIDText, err := h.loadRelatedMessageID(targetChannelIDText, e.Message.ID.String())
		if err != nil {
			e.Client().Logger().Error("failed to fetch related message ID for update", "error", err)
			continue
		}

		forwarderWebhook, err := h.loadOrCreateWebhook(e.Client(), snowflake.MustParse(targetChannelIDText))
		if err != nil {
			e.Client().Logger().Error("failed to load or create webhook", "error", err)
			continue
		}

		content := &strings.Builder{}
		if err := h.tryWriteReferenceHeader(content, forwarderWebhook.GuildID.String(), targetChannelIDText, e.Message.MessageReference); err != nil {
			e.Client().Logger().Error("failed to fetch hook message ID", "error", err)
		}
		content.WriteString(e.Message.Content)
		content.WriteString(contentCommonFooter)

		messageBuilder := discord.NewWebhookMessageUpdateBuilder().
			SetContent(content.String())

		forwarderClient := webhook.New(forwarderWebhook.ID(), forwarderWebhook.Token)

		relatedMessageID := snowflake.MustParse(relatedMessageIDText)
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

	targetChannels, ok := h.cfg[e.ChannelID.String()]
	if !ok {
		return
	}

	for _, targetChannelIDText := range targetChannels {
		relatedMessageIDText, err := h.loadRelatedMessageID(targetChannelIDText, e.MessageID.String())
		if err != nil {
			e.Client().Logger().Error("failed to fetch related message ID for deletion", "error", err)
			continue
		}

		forwarderWebhook, err := h.loadOrCreateWebhook(e.Client(), snowflake.MustParse(targetChannelIDText))
		if err != nil {
			e.Client().Logger().Error("failed to load or create webhook", "error", err)
			continue
		}

		forwarderClient := webhook.New(forwarderWebhook.ID(), forwarderWebhook.Token)

		relatedMessageID := snowflake.MustParse(relatedMessageIDText)
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

	cfgPath := "config.json"
	if len(os.Args) >= 2 {
		cfgPath = os.Args[1]
	} else if path := os.Getenv("BRIDGE_BOT_CONFIG"); path != "" {
		cfgPath = path
	}

	slog.Info("reading config...")

	if err := loadConfig(&handler.cfg, cfgPath); err != nil {
		slog.Error("failed to load config", "error", err)
		return
	}

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
				gateway.IntentMessageContent,
			),
		),
		bot.WithEventListenerFunc(handler.onMessageCreate),
		bot.WithEventListenerFunc(handler.onMessageUpdate),
		bot.WithEventListenerFunc(handler.onMessageDelete),
	)
	if err != nil {
		slog.Error("failed to create client", "error", err)
		return
	}
	defer client.Close(ctx)

	slog.Info("opening gateway...")

	if err = client.OpenGateway(ctx); err != nil {
		slog.Error("failed to open gateway", "error", err)
		return
	}

	slog.Info("listening...")

	notifyCtx, _ := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	<-notifyCtx.Done()
}
