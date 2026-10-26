package config

import (
	"bufio"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	Server    ServerConfig
	Database  DatabaseConfig
	Telegram  TelegramConfig
	Discord   DiscordConfig
	Matter    MatterConfig
	YTDL      YTDLConfig
	Downloads DownloadsConfig
	WebDir    string `env:"WEB_DIR"`
}

// DownloadsConfig holds the background download settings; zero means off.
type DownloadsConfig struct {
	// AutoInterval is how often subscriptions are polled for new videos.
	AutoInterval time.Duration `env:"AUTO_DOWNLOAD_INTERVAL"`
	// MaxGB and MaxAgeDays limit the downloaded files kept on disk.
	MaxGB      float64 `env:"DOWNLOADS_MAX_GB"`
	MaxAgeDays int     `env:"DOWNLOADS_MAX_AGE_DAYS"`
}

type ServerConfig struct {
	Host string `env:"SERVER_HOST"` // empty = all interfaces
	Port string `env:"SERVER_PORT"`
	// APIToken guards the HTTP API. Empty means no authentication.
	APIToken string `env:"API_TOKEN"`
}

type DatabaseConfig struct {
	Path string `env:"DB_PATH"`
}

type TelegramConfig struct {
	BotToken string `env:"TELEGRAM_BOT_TOKEN"`
}

type DiscordConfig struct {
	BotToken string `env:"DISCORD_BOT_TOKEN"`
}

type MatterConfig struct {
	URL      string `env:"MATTERMOST_URL"`
	BotToken string `env:"MATTERMOST_BOT_TOKEN"`
}

type YTDLConfig struct {
	BinaryPath    string `env:"YTDLP_PATH"`
	OutputDir     string `env:"YTDLP_OUTPUT_DIR"`
	MaxConcurrent int    `env:"YTDLP_MAX_CONCURRENT"`
	// YouTube now rejects plain media requests with 403, so yt-dlp needs a JS
	// runtime for the player challenges, the EJS solver, a PO token provider
	// and a client whose formats are still served over plain HTTPS.
	JSRuntime        string `env:"YTDLP_JS_RUNTIME"`
	RemoteComponents string `env:"YTDLP_REMOTE_COMPONENTS"`
	POTScript        string `env:"YTDLP_POT_SCRIPT"`
	PlayerClient     string `env:"YTDLP_PLAYER_CLIENT"`
	// MarkWatched counts every download as a view for the creator.
	MarkWatched bool `env:"YTDLP_MARK_WATCHED"`
}

func defaults() *Config {
	return &Config{
		Server:   ServerConfig{Port: "8070"},
		Database: DatabaseConfig{Path: "ytbloader.db"},
		YTDL: YTDLConfig{
			BinaryPath:       "yt-dlp",
			OutputDir:        "./downloads",
			MaxConcurrent:    2,
			RemoteComponents: "ejs:github",
			PlayerClient:     "web_embedded",
			MarkWatched:      true,
		},
		WebDir: "web",
	}
}

func Load() (*Config, error) {
	if err := loadDotEnv(".env"); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("Failed to read .env: %v", err)
	}

	cfg := defaults()
	if err := env.Parse(cfg); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	// env.Parse ignores empty values; for these two an explicitly empty variable
	// switches the yt-dlp flag off instead of falling back to the default.
	for key, field := range map[string]*string{
		"YTDLP_REMOTE_COMPONENTS": &cfg.YTDL.RemoteComponents,
		"YTDLP_PLAYER_CLIENT":     &cfg.YTDL.PlayerClient,
	} {
		if v, ok := os.LookupEnv(key); ok && v == "" {
			*field = ""
		}
	}
	return cfg, nil
}

// Validate refuses to expose the API without authentication on a non-loopback host.
func (c *Config) Validate() error {
	if c.Server.APIToken == "" && !isLoopbackHost(c.Server.Host) {
		return fmt.Errorf("API_TOKEN is empty while SERVER_HOST %q is not a loopback address: set API_TOKEN or bind SERVER_HOST=127.0.0.1", c.Server.Host)
	}
	if c.Downloads.AutoInterval < 0 || c.Downloads.MaxGB < 0 || c.Downloads.MaxAgeDays < 0 {
		return errors.New("AUTO_DOWNLOAD_INTERVAL, DOWNLOADS_MAX_GB and DOWNLOADS_MAX_AGE_DAYS must not be negative")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// loadDotEnv sets variables from a KEY=VALUE file; real environment variables win.
func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, value)
		}
	}
	return scanner.Err()
}
