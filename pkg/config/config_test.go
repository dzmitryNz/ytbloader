package config

import (
	"os"
	"path/filepath"
	"testing"
)

var envKeys = []string{
	"SERVER_HOST", "SERVER_PORT", "API_TOKEN", "DB_PATH",
	"TELEGRAM_BOT_TOKEN", "DISCORD_BOT_TOKEN", "MATTERMOST_URL", "MATTERMOST_BOT_TOKEN",
	"YTDLP_PATH", "YTDLP_OUTPUT_DIR", "YTDLP_MAX_CONCURRENT", "YTDLP_JS_RUNTIME",
	"YTDLP_REMOTE_COMPONENTS", "YTDLP_POT_SCRIPT", "YTDLP_PLAYER_CLIENT", "WEB_DIR",
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

	cfg := Load()

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

	cfg := Load()

	if cfg.Server.Port != "7000" {
		t.Errorf("Port = %q, want 7000", cfg.Server.Port)
	}
}

func TestMissingDotEnvIsFine(t *testing.T) {
	isolateEnv(t)
	t.Chdir(t.TempDir())

	cfg := Load()

	if cfg.Server.Port != "8070" {
		t.Errorf("Port = %q, want default 8070", cfg.Server.Port)
	}
}
