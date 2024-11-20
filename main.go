package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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

const ForwarderHookName = "ForwarderHook2"

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

type Stats struct {
	MessageForwardCount uint64
}

type Handler struct {
	ctx   context.Context
	cfg   Config
	stats Stats
}

func (h *Handler) getOrCreateWebhook(client bot.Client, channelID snowflake.ID) (*discord.IncomingWebhook, error) {
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

func (h *Handler) onMessageCreate(e *events.MessageCreate) {
	if e.Message.Author.Bot {
		return
	}

	targetChannels, ok := h.cfg[e.ChannelID.String()]
	if !ok {
		return
	}

	fmt.Print("\nforwarded messages count: ", atomic.AddUint64(&h.stats.MessageForwardCount, 1))

	for _, targetChannelID := range targetChannels {
		targetID, err := snowflake.Parse(targetChannelID)
		if err != nil {
			e.Client().Logger().Error("failed to parse channel ID", "error", err)
			continue
		}

		forwarderWebhook, err := h.getOrCreateWebhook(e.Client(), targetID)
		if err != nil {
			e.Client().Logger().Error("failed to get/create webhook", "error", err)
			continue
		}

		content := strings.Builder{}
		content.WriteString(e.Message.Content)

		messageBuilder := discord.NewWebhookMessageCreateBuilder().
			SetAvatarURL(*e.Message.Author.AvatarURL()).
			SetUsername(e.Message.Author.Username)

		bodies := []io.ReadCloser{}

		for _, attachment := range e.Message.Attachments {
			if attachment.Size <= 1<<15 {
				desc := ""
				if attachment.Description != nil {
					desc = *attachment.Description
				}
				resp, err := http.Get(attachment.URL)
				if err != nil {
					e.Client().Logger().Error("failed to download attachment", "error", err)
					continue
				}
				bodies = append(bodies, resp.Body)
				messageBuilder.AddFile(attachment.Filename, desc, resp.Body)
				continue
			}

			content.WriteByte('\n')
			content.WriteString(attachment.URL)
		}

		messageBuilder.SetContent(content.String())

		forwarderClient := webhook.New(forwarderWebhook.ID(), forwarderWebhook.Token)
		if _, err := forwarderClient.CreateMessage(messageBuilder.Build()); err != nil {
			e.Client().Logger().Error("failed to send message via webhook", "error", err)
		}
		forwarderClient.Close(h.ctx)

		for _, body := range bodies {
			body.Close()
		}
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

	client, err := disgo.New(os.Getenv("BRIDGE_BOT_TOKEN"),
		bot.WithGatewayConfigOpts(
			gateway.WithIntents(
				gateway.IntentGuilds,
				gateway.IntentGuildMessages,
				gateway.IntentMessageContent,
			),
		),
		bot.WithEventListenerFunc(handler.onMessageCreate),
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
