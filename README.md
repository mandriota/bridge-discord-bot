# bridge-discord-bot
configure a bridge between your discord channels

## Installation

Download and install Go from [go.dev](https://go.dev), then enter the following command in your terminal:
```sh
go install https://github.com/mandriota/bridge-discord-bot.git
```

You may also need to add `go/bin` directory to `PATH` environment variable.
Enter the following command in your terminal to find `go/bin` directory:
```sh
echo `go env GOPATH`/bin
```

## Configuration
Config location is read in following order:
1. CLI argument
2. Environment variable `BRIDGE_BOT_CONFIG`
3. Default value `config.json`

Configuration file looks like:
```json
{
  "channel-id-a": ["channel-id-b", "channel-id-c", "channel-id-d"],
  "channel-id-b": ["channel-id-a", "channel-id-c", "channel-id-d"],
  "channel-id-c": ["channel-id-a", "channel-id-b", "channel-id-d"],
  "channel-id-d": ["channel-id-a", "channel-id-b", "channel-id-c"]
}
```

Bot token is read from `BRIDGE_BOT_TOKEN` environment variable.

