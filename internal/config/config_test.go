package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadReadsDotenv(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	content := []byte("S3_ACCESS_KEY=public-ak\nS3_SECRET_KEY=public-sk\nDOGE_ACCESS_KEY=doge-ak\nDOGE_SECRET_KEY=doge-sk\nGATEWAY_ADDR=:9090\n")
	if err := os.WriteFile(envPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENV_FILE", envPath)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":9090" {
		t.Fatalf("Addr = %q", cfg.Addr)
	}
	if len(cfg.Accounts) != 1 {
		t.Fatalf("accounts = %d", len(cfg.Accounts))
	}
	account := cfg.Accounts[0]
	if account.S3AccessKey != "public-ak" || account.S3SecretKey != "public-sk" {
		t.Fatalf("unexpected s3 account: %#v", account)
	}
	if account.DogeAccessKey != "doge-ak" || account.DogeSecretKey != "doge-sk" {
		t.Fatalf("unexpected doge account: %#v", account)
	}
}

func TestFromEnvFallsBackToDogeKeysForS3(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("DOGE_ACCESS_KEY", "doge-ak")
	t.Setenv("DOGE_SECRET_KEY", "doge-sk")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	account := cfg.Accounts[0]
	if account.S3AccessKey != "doge-ak" || account.S3SecretKey != "doge-sk" {
		t.Fatalf("unexpected fallback account: %#v", account)
	}
}

func TestFromEnvParsesAccountsJSON(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv(accountsJSONEnv, `[{"s3AccessKey":"s3-ak","s3SecretKey":"s3-sk","dogeAccessKey":"doge-ak","dogeSecretKey":"doge-sk"}]`)

	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Accounts[0].S3AccessKey; got != "s3-ak" {
		t.Fatalf("S3AccessKey = %q", got)
	}
}

func clearConfigEnv(t *testing.T) {
	t.Helper()
	keys := []string{
		"DOTENV",
		"ENV_FILE",
		"S3_ACCESS_KEY",
		"S3_SECRET_KEY",
		"AWS_ACCESS_KEY_ID",
		"AWS_SECRET_ACCESS_KEY",
		"DOGE_ACCESS_KEY",
		"DOGE_SECRET_KEY",
		"DOGE_API_ID",
		"DOGE_API_TOKEN",
		accountsJSONEnv,
	}
	for _, key := range keys {
		oldValue, existed := os.LookupEnv(key)
		_ = os.Unsetenv(key)
		t.Cleanup(func() {
			if existed {
				_ = os.Setenv(key, oldValue)
			} else {
				_ = os.Unsetenv(key)
			}
		})
	}
}
