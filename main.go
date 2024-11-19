package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/webhook"
	"github.com/disgoorg/snowflake/v2"
)

type Config map[string][]string

const ForwarderHookName = "ForwarderHook"

func loadConfig(filePath string) (Config, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	config := Config{}
	if err := json.NewDecoder(file).Decode(&config); err != nil {
		return nil, err
	}
	return config, nil
}

func getOrCreateWebhook(client bot.Client, channelID snowflake.ID) (*discord.IncomingWebhook, error) {
	webhooks, err := client.Rest().GetWebhooks(channelID)
	if err != nil {
		return nil, err
	}

	for _, webhook := range webhooks {
		if incomingWebhook, ok := webhook.(discord.IncomingWebhook); ok && webhook.Name() == ForwarderHookName {
			return &incomingWebhook, nil
		}
	}

	return client.Rest().CreateWebhook(channelID, discord.WebhookCreate{
		Name: ForwarderHookName,
	})
}

func main() {
	ctx := context.Background()

	cfgPath := "config.json"
	if len(os.Args) >= 2 {
		cfgPath = os.Args[1]
	} else if path := os.Getenv("BRIDGE_BOT_CONFIG"); path != "" {
		cfgPath = path
	}

	slog.Info("reading config...")
	
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		return
	}

	forwardCounter := int64(0)

	client, err := disgo.New(os.Getenv("BRIDGE_BOT_TOKEN"),
		bot.WithGatewayConfigOpts(
			gateway.WithIntents(
				gateway.IntentGuilds,
				gateway.IntentGuildMessages,
				gateway.IntentMessageContent,
			),
		),
		bot.WithEventListenerFunc(func(e *events.MessageCreate) {
			if e.Message.Author.Bot {
				return
			}

			targetChannels, ok := cfg[e.ChannelID.String()]
			if !ok {
				return
			}

			fmt.Print("\nforwarded messages count: ", atomic.AddInt64(&forwardCounter, 1))
			
			for _, targetChannelID := range targetChannels {
				targetID, err := snowflake.Parse(targetChannelID)
				if err != nil {
					e.Client().Logger().Error("failed to parse channel ID", "error", err)
					continue
				}

				forwarderWebhook, err := getOrCreateWebhook(e.Client(), targetID)
				if err != nil {
					e.Client().Logger().Error("failed to get/create webhook", "error", err)
					continue
				}

				content := strings.Builder{}
				content.WriteString(e.Message.Content)

				for _, attachment := range e.Message.Attachments {
					content.WriteByte('\n')
					content.WriteString(attachment.URL)
				}

				messageBuilder := discord.NewWebhookMessageCreateBuilder().
					SetAvatarURL(*e.Message.Author.AvatarURL()).
					SetUsername(e.Message.Author.Username).
					SetContent(content.String())

				forwarderClient := webhook.New(forwarderWebhook.ID(), forwarderWebhook.Token)
				if _, err := forwarderClient.CreateMessage(messageBuilder.Build()); err != nil {
					e.Client().Logger().Error("failed to send message via webhook", "error", err)
				}
				forwarderClient.Close(ctx)
			}
		}),
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
