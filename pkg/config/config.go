package config

import (
	"os"
	"strconv"
)

type Config struct {
	Server   ServerConfig
	Database DatabaseConfig
	Telegram TelegramConfig
	Discord  DiscordConfig
	Matter   MatterConfig
	YTDL     YTDLConfig
	WebDir   string
}

type ServerConfig struct {
	Port string
	// APIToken guards the HTTP API. Empty means no authentication.
	APIToken string
}

type DatabaseConfig struct {
	Path string
}

type TelegramConfig struct {
	BotToken string
}

type DiscordConfig struct {
	BotToken string
}

type MatterConfig struct {
	URL      string
	BotToken string
}

type YTDLConfig struct {
	BinaryPath    string
	OutputDir     string
	MaxConcurrent int
	// YouTube now rejects plain media requests with 403, so yt-dlp needs a JS
	// runtime for the player challenges, the EJS solver, a PO token provider
	// and a client whose formats are still served over plain HTTPS.
	JSRuntime        string
	RemoteComponents string
	POTScript        string
	PlayerClient     string
}

func Load() *Config {
	return &Config{
		Server: ServerConfig{
			Port:     getEnv("SERVER_PORT", "8070"),
			APIToken: getEnv("API_TOKEN", ""),
		},
		Database: DatabaseConfig{
			Path: getEnv("DB_PATH", "ytbloader.db"),
		},
		Telegram: TelegramConfig{
			BotToken: getEnv("TELEGRAM_BOT_TOKEN", ""),
		},
		Discord: DiscordConfig{
			BotToken: getEnv("DISCORD_BOT_TOKEN", ""),
		},
		Matter: MatterConfig{
			URL:      getEnv("MATTERMOST_URL", ""),
			BotToken: getEnv("MATTERMOST_BOT_TOKEN", ""),
		},
		YTDL: YTDLConfig{
			BinaryPath:       getEnv("YTDLP_PATH", "yt-dlp"),
			OutputDir:        getEnv("YTDLP_OUTPUT_DIR", "./downloads"),
			MaxConcurrent:    getEnvInt("YTDLP_MAX_CONCURRENT", 2),
			JSRuntime:        getEnv("YTDLP_JS_RUNTIME", ""),
			RemoteComponents: getEnv("YTDLP_REMOTE_COMPONENTS", "ejs:github"),
			POTScript:        getEnv("YTDLP_POT_SCRIPT", ""),
			PlayerClient:     getEnv("YTDLP_PLAYER_CLIENT", "web_embedded"),
		},
		WebDir: getEnv("WEB_DIR", "web"),
	}
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if value, exists := os.LookupEnv(key); exists {
		if intVal, err := strconv.Atoi(value); err == nil {
			return intVal
		}
	}
	return fallback
}
