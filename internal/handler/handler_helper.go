package handler

import (
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"
)

const ForwarderHookName = "ForwarderHook"

func loadOrCreateWebhook(client bot.Client, channelID snowflake.ID) (*discord.IncomingWebhook, error) {
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
		Name: ForwarderHookName,
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
