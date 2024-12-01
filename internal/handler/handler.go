package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"unicode"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/disgo/webhook"
	"github.com/disgoorg/json"
	"github.com/disgoorg/snowflake/v2"
	"github.com/mandriota/bridge-discord-bot/internal/config"
	"github.com/mandriota/bridge-discord-bot/internal/repository/dbqueries"
	"github.com/mandriota/bridge-discord-bot/internal/repository"
	"github.com/mandriota/bridge-discord-bot/internal/texts"
)

type EventHandler struct {
	Ctx context.Context
	Cfg config.Config

	Rest           rest.Rest
	DB             *sql.DB
	recentDelCache sync.Map
}

//=:handler:messages

func (h *EventHandler) tryWriteReferenceHeader(w *strings.Builder, targetGuildID, targetChannelID snowflake.ID, msgRef *discord.MessageReference) error {
	if msgRef == nil {
		return nil
	}

	relatedMsgID, err := repository.LoadDirelatedMessageID(h.Ctx, h.DB, targetChannelID, *msgRef.MessageID)
	if err != nil {
		return err
	}

	referredMsg, err := h.Rest.GetMessage(*msgRef.ChannelID, *msgRef.MessageID)
	if err != nil {
		return err
	}

	referredMsgAuthorID, err := repository.LoadAuthorID(h.Ctx, h.DB, referredMsg.Author.Username)
	if err != nil {
		return err
	}

	referredMsgPreview := referredMsg.Content[texts.SkipPrefixedLine(referredMsg.Content, "-#"):]
	cutIndicator := ""
	referredMsgPreviewWindow := texts.NthRune(referredMsgPreview, 128)
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

func (h *EventHandler) OnGuildMessageCreate(e *events.GuildMessageCreate) {
	if e.Message.Author.Bot {
		return
	}

	targetChannels, err := repository.LoadRelatedChannels(h.Ctx, h.DB, e.ChannelID)
	if err != nil {
		e.Client().Logger().Error("failed to load related channels", "error", err)
		return
	}

	tx, err := h.DB.BeginTx(h.Ctx, nil)
	if err != nil {
		e.Client().Logger().Error("failed to begin transaction", "error", err)
		return
	}
	defer tx.Rollback()

	if err := repository.SaveAuthorMapping(h.Ctx, tx, e.Message.Author.Username, e.Message.Author.ID); err != nil {
		e.Client().Logger().Error("failed to save author mapping", "error", err)
	}

	contentCommonFooter, contentCommonFileAttach, contentCommonFileBodies := processMessageAttachments(&h.Cfg, e.GenericGuildMessage, false)

	for _, targetChannelID := range targetChannels {
		forwarderWebhook, err := loadOrCreateWebhook(&h.Cfg, e.Client(), targetChannelID)
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
			desc := ""
			if attach.Description != nil {
				desc = *attach.Description
			}
			messageBuilder.AddFile(attach.Filename, desc, bytes.NewReader(contentCommonFileBodies[i]))
		}

		forwarderClient := webhook.New(forwarderWebhook.ID(), forwarderWebhook.Token)
		webhookMessage, err := forwarderClient.CreateMessage(messageBuilder.Build())
		if err != nil {
			e.Client().Logger().Error("failed to send message via webhook", "error", err)
		}
		forwarderClient.Close(h.Ctx)

		if err := repository.SaveMessageMapping(h.Ctx, tx, e.Message.ChannelID, e.MessageID, webhookMessage.ChannelID, webhookMessage.ID); err != nil {
			e.Client().Logger().Error("failed to save message mapping", "error", err)
		}
	}

	if err := tx.Commit(); err != nil {
		e.Client().Logger().Error("failed to commit transaction", "error", err)
		return
	}
}

func (h *EventHandler) OnGuildMessageUpdate(e *events.GuildMessageUpdate) {
	if e.Message.Author.Bot {
		return
	}

	targetChannels, err := repository.LoadRelatedChannels(h.Ctx, h.DB, e.ChannelID)
	if err != nil {
		e.Client().Logger().Error("failed to load related channels", "error", err)
		return
	}

	contentCommonFooter, _, _ := processMessageAttachments(&h.Cfg, e.GenericGuildMessage, true)

	for _, targetChannelID := range targetChannels {
		relatedMessageID, err := repository.LoadRelatedMessageID(h.Ctx, h.DB, targetChannelID, e.MessageID)
		if err != nil {
			e.Client().Logger().Error("failed to fetch related message ID for update", "error", err)
			continue
		}

		forwarderWebhook, err := loadOrCreateWebhook(&h.Cfg, e.Client(), targetChannelID)
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

		forwarderClient.Close(h.Ctx)
	}
}

func (h *EventHandler) OnGuildMessageDelete(e *events.GuildMessageDelete) {
	if e.Message.Author.Bot {
		return
	}

	if _, ok := h.recentDelCache.LoadAndDelete(e.MessageID); ok {
		return
	}

	targetChannels, err := repository.LoadRelatedChannels(h.Ctx, h.DB, e.ChannelID)
	if err != nil {
		e.Client().Logger().Error("failed to load related channels", "error", err)
		return
	}

	for _, targetChannelID := range targetChannels {
		relatedMessageID, err := repository.LoadRelatedMessageID(h.Ctx, h.DB, targetChannelID, e.MessageID)
		if err != nil {
			e.Client().Logger().Error("failed to fetch related message ID for deletion", "error", err)
			continue
		}

		forwarderWebhook, err := loadOrCreateWebhook(&h.Cfg, e.Client(), targetChannelID)
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

		forwarderClient.Close(h.Ctx)
	}
}

//=:handler:slash_commands

func (h *EventHandler) InitCommands(appID snowflake.ID) error {
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
			Contexts:                 []discord.InteractionContextType{discord.InteractionContextTypeGuild},
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
			DefaultMemberPermissions: json.NewNullablePtr(discord.PermissionManageChannels),
			Contexts:                 []discord.InteractionContextType{discord.InteractionContextTypeGuild},
		},
		discord.SlashCommandCreate{
			Name:        "unlink_all",
			Description: "unlinks current channel from all virtual channels",
			DefaultMemberPermissions: json.NewNullablePtr(discord.PermissionManageChannels),
			Contexts:                 []discord.InteractionContextType{discord.InteractionContextTypeGuild},
		},
		discord.SlashCommandCreate{
			Name:        "list",
			Description: "list virtual channels linked to current channel",
			DefaultMemberPermissions: json.NewNullablePtr(discord.PermissionManageChannels),
			Contexts:                 []discord.InteractionContextType{discord.InteractionContextTypeGuild},
		},
	}

	_, err := h.Rest.SetGlobalCommands(appID, commands)
	return err
}

func (h *EventHandler) onCommandInteractionCreateList(e *events.ApplicationCommandInteractionCreate, _ discord.SlashCommandInteractionData) {
	query, args := dbqueries.BuildSelectVirtualChannelKeyQuery(e.Channel().ID())

	rows, err := h.DB.Query(query, args...)
	if err != nil {
		e.Client().Logger().Error("failed to list virtual channels for the channel", "error", err)
		sendErrorMessage(e, "Could not retrieve the list of virtual channels.")
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
		sendSuccessMessage(e, "No Virtual Channels Linked", "No virtual channels are linked to this channel.")
		return
	}

	sendSuccessMessage(e, "Virtual Channels Linked", fmt.Sprintf("Virtual channels linked to this channel:\n%s", sb.String()))
}

func (h *EventHandler) onCommandInteractionCreateLink(e *events.ApplicationCommandInteractionCreate, commandData discord.SlashCommandInteractionData) {
	virtualChannelKey := commandData.String("virtual_channel_key")
	note := commandData.String("note")

	hash := sha256.Sum256([]byte(virtualChannelKey))
	virtualChannelHash := hex.EncodeToString(hash[:])

	query, args := dbqueries.BuildInsertLinkQuery(virtualChannelHash, e.Channel().ID(), note)
	_, err := h.DB.Exec(query, args...)
	if err != nil {
		e.Client().Logger().Error("failed to link channel to virtual channel key", "error", err)
		sendErrorMessage(e, "Could not link the channel.")
		return
	}

	sendSuccessMessage(e, "Success", fmt.Sprintf("Channel successfully linked to virtual channel `%s`.", virtualChannelHash))
}

func (h *EventHandler) onCommandInteractionCreateUnlink(e *events.ApplicationCommandInteractionCreate, commandData discord.SlashCommandInteractionData) {
	virtualChannelKey := commandData.String("virtual_channel_key")

	query, args := dbqueries.BuildDeleteLinkQuery(virtualChannelKey, e.Channel().ID())
	res, err := h.DB.Exec(query, args...)
	if err != nil {
		e.Client().Logger().Error("failed to unlink channel from virtual channel key", "error", err)
		sendErrorMessage(e, "Could not unlink the channel.")
		return
	}

	rowsAffected, _ := res.RowsAffected()
	if rowsAffected == 0 {
		sendErrorMessage(e, fmt.Sprintf("No link found for virtual channel key `%s`.", virtualChannelKey))
		return
	}

	sendSuccessMessage(e, "Success", fmt.Sprintf("Channel successfully unlinked from virtual channel key `%s`.", virtualChannelKey))
}

func (h *EventHandler) onCommandInteractionCreateUnlinkAll(e *events.ApplicationCommandInteractionCreate, _ discord.SlashCommandInteractionData) {
	query, args := dbqueries.BuildDeleteAllLinksQuery(e.Channel().ID())

	res, err := h.DB.Exec(query, args...)
	if err != nil {
		e.Client().Logger().Error("failed to unlink all virtual channels for the channel", "error", err)
		sendErrorMessage(e, "Could not unlink all virtual channels.")
		return
	}

	rowsAffected, _ := res.RowsAffected()
	if rowsAffected == 0 {
		sendErrorMessage(e, "No links found for this channel.")
		return
	}

	sendSuccessMessage(e, "Success", fmt.Sprintf("Successfully unlinked %d virtual channel(s) from this channel.", rowsAffected))
}

func (h *EventHandler) OnCommandInteractionCreate(e *events.ApplicationCommandInteractionCreate) {
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
