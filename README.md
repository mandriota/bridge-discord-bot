# bridge-discord-bot
configure a bridge between your discord channels

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
