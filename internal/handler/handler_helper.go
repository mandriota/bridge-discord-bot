package handler

import (
	"io"
	"net/http"
	"strings"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"
	"github.com/mandriota/bridge-discord-bot/internal/config"
)

func processMessageAttachments(cfg *config.Config, e *events.GenericGuildMessage, onlyFooter bool) (footer string, attach []uint8, bodies [][]byte) {
	contentCommonFooter := strings.Builder{}
	contentCommonFileAttach := []uint8{}
	contentCommonFileBodies := [][]byte{}

	for i, attach := range e.Message.Attachments {
		if attach.Size > cfg.MaxAttachmentSize {
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

func loadOrCreateWebhook(cfg *config.Config, client bot.Client, channelID snowflake.ID) (*discord.IncomingWebhook, error) {
	webhooks, err := client.Rest().GetWebhooks(channelID)
	if err != nil {
		return nil, err
	}

	for _, webhook := range webhooks {
		if webhook, ok := webhook.(discord.IncomingWebhook); ok && webhook.ApplicationID != nil && *webhook.ApplicationID == client.ApplicationID() {
			return &webhook, nil
		}
	}

	return client.Rest().CreateWebhook(channelID, discord.WebhookCreate{
		Name: cfg.ForwarderHookName,
	})
}

func sendErrorMessage(e *events.ApplicationCommandInteractionCreate, description string) {
	if err := e.CreateMessage(discord.NewMessageCreateBuilder().
		SetEmbeds(discord.Embed{
			Title:       "Error",
			Description: description,
			Color:       0xFF0000,
		}).
		SetEphemeral(true).
		Build(),
	); err != nil {
		e.Client().Logger().Error("failed to send message", "error", err)
	}
}

func sendSuccessMessage(e *events.ApplicationCommandInteractionCreate, title, description string) {
	if err := e.CreateMessage(discord.NewMessageCreateBuilder().
		SetEmbeds(discord.Embed{
			Title:       title,
			Description: description,
			Color:       0x00FF00,
		}).
		SetEphemeral(true).
		Build(),
	); err != nil {
		e.Client().Logger().Error("failed to send message", "error", err)
	}
}
