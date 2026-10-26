package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

var envKeys = []string{
	"SERVER_HOST", "SERVER_PORT", "API_TOKEN", "DB_PATH",
	"TELEGRAM_BOT_TOKEN", "DISCORD_BOT_TOKEN", "MATTERMOST_URL", "MATTERMOST_BOT_TOKEN",
	"YTDLP_PATH", "YTDLP_OUTPUT_DIR", "YTDLP_MAX_CONCURRENT", "YTDLP_JS_RUNTIME",
	"YTDLP_REMOTE_COMPONENTS", "YTDLP_POT_SCRIPT", "YTDLP_PLAYER_CLIENT", "WEB_DIR",
	"AUTO_DOWNLOAD_INTERVAL", "DOWNLOADS_MAX_GB", "DOWNLOADS_MAX_AGE_DAYS",
}

// isolateEnv unsets every config variable for the test and restores it afterwards.
func isolateEnv(t *testing.T) {
	t.Helper()
	for _, k := range envKeys {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

func writeDotEnv(t *testing.T, content string) {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadReadsDotEnv(t *testing.T) {
	isolateEnv(t)
	writeDotEnv(t, "# comment\n\nSERVER_PORT=9999\nAPI_TOKEN=\"x\"\nDB_PATH='my.db'\n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Server.Port != "9999" {
		t.Errorf("Port = %q, want 9999", cfg.Server.Port)
	}
	if cfg.Server.APIToken != "x" {
		t.Errorf("APIToken = %q, want x", cfg.Server.APIToken)
	}
	if cfg.Database.Path != "my.db" {
		t.Errorf("DB path = %q, want my.db", cfg.Database.Path)
	}
}

func TestEnvOverridesDotEnv(t *testing.T) {
	isolateEnv(t)
	writeDotEnv(t, "SERVER_PORT=9999\n")
	t.Setenv("SERVER_PORT", "7000")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Server.Port != "7000" {
		t.Errorf("Port = %q, want 7000", cfg.Server.Port)
	}
}

func TestMissingDotEnvIsFine(t *testing.T) {
	isolateEnv(t)
	t.Chdir(t.TempDir())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Server.Port != "8070" {
		t.Errorf("Port = %q, want default 8070", cfg.Server.Port)
	}
}

func TestValidateRejectsEmptyTokenOnPublicHost(t *testing.T) {
	tests := []struct {
		host    string
		token   string
		wantErr bool
	}{
		{"", "", true},
		{"0.0.0.0", "", true},
		{"192.168.1.5", "", true},
		{"127.0.0.1", "", false},
		{"localhost", "", false},
		{"::1", "", false},
		{"", "secret", false},
		{"0.0.0.0", "secret", false},
		{"192.168.1.5", "secret", false},
	}
	for _, tt := range tests {
		cfg := &Config{Server: ServerConfig{Host: tt.host, APIToken: tt.token}}
		err := cfg.Validate()
		if (err != nil) != tt.wantErr {
			t.Errorf("host %q token %q: err = %v, wantErr %v", tt.host, tt.token, err, tt.wantErr)
		}
	}
}

func TestLoadRejectsBadInt(t *testing.T) {
	isolateEnv(t)
	t.Chdir(t.TempDir())
	t.Setenv("YTDLP_MAX_CONCURRENT", "abc")

	if _, err := Load(); err == nil {
		t.Fatal("expected an error for a non-numeric YTDLP_MAX_CONCURRENT")
	}
}

func TestLoadDefaults(t *testing.T) {
	isolateEnv(t)
	t.Chdir(t.TempDir())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.YTDL.MaxConcurrent != 2 || cfg.YTDL.RemoteComponents != "ejs:github" || cfg.YTDL.PlayerClient != "web_embedded" || cfg.YTDL.MarkWatched || cfg.YTDL.Cookies != "" {
		t.Errorf("unexpected yt-dlp defaults: %+v", cfg.YTDL)
	}
	if cfg.Database.Path != "ytbloader.db" || cfg.YTDL.OutputDir != "./downloads" || cfg.YTDL.BinaryPath != "yt-dlp" || cfg.WebDir != "web" {
		t.Errorf("unexpected defaults: db %q, output %q, binary %q, web %q", cfg.Database.Path, cfg.YTDL.OutputDir, cfg.YTDL.BinaryPath, cfg.WebDir)
	}
}

func TestEmptyValueDropsDefaultedOption(t *testing.T) {
	isolateEnv(t)
	t.Chdir(t.TempDir())
	t.Setenv("YTDLP_REMOTE_COMPONENTS", "")
	t.Setenv("YTDLP_PLAYER_CLIENT", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.YTDL.RemoteComponents != "" || cfg.YTDL.PlayerClient != "" {
		t.Errorf("empty variables must switch the yt-dlp flags off, got %q / %q", cfg.YTDL.RemoteComponents, cfg.YTDL.PlayerClient)
	}
}

func TestLoadAutoDownloadAndRetentionDefaultsAreOff(t *testing.T) {
	isolateEnv(t)
	t.Chdir(t.TempDir())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Downloads.AutoInterval != 0 || cfg.Downloads.MaxGB != 0 || cfg.Downloads.MaxAgeDays != 0 {
		t.Errorf("downloads defaults = %+v, want everything off", cfg.Downloads)
	}
}

func TestLoadAutoDownloadAndRetention(t *testing.T) {
	isolateEnv(t)
	t.Chdir(t.TempDir())
	t.Setenv("AUTO_DOWNLOAD_INTERVAL", "90m")
	t.Setenv("DOWNLOADS_MAX_GB", "2.5")
	t.Setenv("DOWNLOADS_MAX_AGE_DAYS", "30")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Downloads.AutoInterval != 90*time.Minute || cfg.Downloads.MaxGB != 2.5 || cfg.Downloads.MaxAgeDays != 30 {
		t.Errorf("downloads = %+v", cfg.Downloads)
	}
}

func TestLoadRejectsBadInterval(t *testing.T) {
	isolateEnv(t)
	t.Chdir(t.TempDir())
	t.Setenv("AUTO_DOWNLOAD_INTERVAL", "soon")

	if _, err := Load(); err == nil {
		t.Fatal("expected an error for an invalid AUTO_DOWNLOAD_INTERVAL")
	}
}

func TestValidateRejectsNegativeDownloadSettings(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"interval": func(c *Config) { c.Downloads.AutoInterval = -time.Hour },
		"max gb":   func(c *Config) { c.Downloads.MaxGB = -1 },
		"max days": func(c *Config) { c.Downloads.MaxAgeDays = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := defaults()
			cfg.Server.Host = "127.0.0.1"
			mutate(cfg)
			if err := cfg.Validate(); err == nil {
				t.Error("Validate accepted a negative value")
			}
		})
	}
}
