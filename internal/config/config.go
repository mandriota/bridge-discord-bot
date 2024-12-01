package config

type Config struct {
	DBPath   string
	BotToken string

	ForwarderHookName string
	MaxAttachmentSize int
}
