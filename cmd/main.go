package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/json"
	"github.com/mandriota/bridge-discord-bot/internal/handler"
	"github.com/mandriota/bridge-discord-bot/internal/repository"
	_ "github.com/mattn/go-sqlite3"
)

var (
	commands = []discord.ApplicationCommandCreate{
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
)

func main() {
	ctx := context.Background()
	handler := handler.Handler{Ctx: ctx}

	slog.Info("initializating database...")

	if err := repository.InitDB(ctx, &handler.DB, "messages.db"); err != nil {
		slog.Error("failed to initialize database", "error", err)
		return
	}
	defer handler.DB.Close()

	client, err := disgo.New(os.Getenv("BRIDGE_BOT_TOKEN"),
		bot.WithGatewayConfigOpts(
			gateway.WithIntents(
				gateway.IntentGuilds,
				gateway.IntentGuildMessages,
				gateway.IntentGuildExpressions,
				gateway.IntentMessageContent,
			),
		),
		bot.WithEventListenerFunc(handler.OnCommandInteractionCreate),
		bot.WithEventListenerFunc(handler.OnGuildMessageCreate),
		bot.WithEventListenerFunc(handler.OnGuildMessageUpdate),
		bot.WithEventListenerFunc(handler.OnGuildMessageDelete),
	)
	if err != nil {
		slog.Error("failed to create client", "error", err)
		return
	}
	defer client.Close(ctx)

	handler.Rest = client.Rest()

	slog.Info("opening gateway...")

	if err = client.OpenGateway(ctx); err != nil {
		slog.Error("failed to open gateway", "error", err)
		return
	}

	client.Rest().SetGlobalCommands(client.ApplicationID(), commands)

	slog.Info("listening...")

	notifyCtx, _ := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	<-notifyCtx.Done()
}
