package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/gateway"
	"github.com/mandriota/bridge-discord-bot/internal/config"
	"github.com/mandriota/bridge-discord-bot/internal/handler"
	"github.com/mandriota/bridge-discord-bot/internal/repository"
	_ "github.com/mattn/go-sqlite3"
)

func main() {
	ctx := context.Background()
	cfg := config.Config{
		DBPath:            "messages.db",
		BotToken:          os.Getenv("BRIDGE_BOT_TOKEN"),
		ForwarderHookName: "Bridge",
		MaxAttachmentSize: (1 << 20) * 10,
	}
	
	eh := handler.EventHandler{
		Ctx: ctx,
		Cfg: cfg,
	}

	slog.Info("initializating database...")

	if err := repository.InitDB(ctx, &eh.DB, cfg.DBPath); err != nil {
		slog.Error("failed to initialize database", "error", err)
		return
	}
	defer eh.DB.Close()

	client, err := disgo.New(cfg.BotToken,
		bot.WithGatewayConfigOpts(
			gateway.WithIntents(
				gateway.IntentGuilds,
				gateway.IntentGuildMessages,
				gateway.IntentGuildExpressions,
				gateway.IntentMessageContent,
			),
		),
		bot.WithEventListenerFunc(eh.OnCommandInteractionCreate),
		bot.WithEventListenerFunc(eh.OnGuildMessageCreate),
		bot.WithEventListenerFunc(eh.OnGuildMessageUpdate),
		bot.WithEventListenerFunc(eh.OnGuildMessageDelete),
	)
	if err != nil {
		slog.Error("failed to create client", "error", err)
		return
	}
	defer client.Close(ctx)

	eh.Rest = client.Rest()

	slog.Info("opening gateway...")

	if err = client.OpenGateway(ctx); err != nil {
		slog.Error("failed to open gateway", "error", err)
		return
	}

	eh.InitCommands(client.ApplicationID())

	slog.Info("listening...")

	notifyCtx, _ := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	<-notifyCtx.Done()
}
