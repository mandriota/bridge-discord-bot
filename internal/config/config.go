package config

type Config struct {
	DBPath   string
	ProxyURL string
	BotToken string

	ForwarderHookName string
	MaxAttachmentSize int
}
