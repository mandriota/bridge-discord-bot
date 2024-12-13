# bridge-discord-bot
configure a bridge between your discord channels

## Installation

Download and install Go from [go.dev](https://go.dev), then enter the following command in your terminal:
```sh
go install github.com/mandriota/bridge-discord-bot@latest
```

You may also need to add `go/bin` directory to `PATH` environment variable.
Enter the following command in your terminal to find `go/bin` directory:
```sh
echo `go env GOPATH`/bin
```

## Configuration
Bot token is read from `BRIDGE_BOT_TOKEN` environment variable.

The are 4 slash commands for bot configuration, available in Discord channels:
- `/list` - lists linked virtual channels associated with current channel.
- `/link` - links current channel to existing virtual channel specified by `virtual_channel_key` parameter or creates a new one. You can provide an optional `note` to simplify management of many virtual channels.
- `/unlink` - unlinks current channel from virtual channel specified by `virtual_channel_key`.
- `/unlink_all` - unlinks all virtual channels from current channel.

All commands above require manage channels permission.
