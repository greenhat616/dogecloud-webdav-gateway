package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

const (
	defaultAddr            = ":8080"
	defaultDogeAPIBaseURL  = "https://api.dogecloud.com"
	defaultRegion          = "automatic"
	defaultService         = "s3"
	defaultTmpTokenTTL     = 7200
	defaultRefreshSkew     = 5 * time.Minute
	defaultCleanupInterval = time.Minute
	defaultBodyMemoryLimit = int64(32 << 20)
	accountsJSONEnv        = "DOGE_S3_ACCOUNTS_JSON"
	defaultDotenvPath      = ".env"
)

type Account struct {
	S3AccessKey   string `json:"s3AccessKey"`
	S3SecretKey   string `json:"s3SecretKey"`
	DogeAccessKey string `json:"dogeAccessKey"`
	DogeSecretKey string `json:"dogeSecretKey"`
}

type Config struct {
	Addr                 string
	PublicBaseHost       string
	DogeAPIBaseURL       string
	Region               string
	Service              string
	TmpTokenTTL          int
	TokenRefreshSkew     time.Duration
	TokenCleanupInterval time.Duration
	BodyMemoryLimit      int64
	Accounts             []Account
}

func Load() (Config, error) {
	loadDotenv()
	return FromEnv()
}

func FromEnv() (Config, error) {
	cfg := Config{
		Addr:                 envString("GATEWAY_ADDR", envString("ADDR", defaultAddr)),
		PublicBaseHost:       strings.TrimSpace(os.Getenv("PUBLIC_BASE_HOST")),
		DogeAPIBaseURL:       envString("DOGE_API_BASE_URL", defaultDogeAPIBaseURL),
		Region:               envString("S3_REGION", defaultRegion),
		Service:              envString("S3_SERVICE", defaultService),
		TmpTokenTTL:          envInt("DOGE_TMP_TOKEN_TTL", defaultTmpTokenTTL),
		TokenRefreshSkew:     time.Duration(envInt("DOGE_TOKEN_REFRESH_SKEW_SECONDS", int(defaultRefreshSkew.Seconds()))) * time.Second,
		TokenCleanupInterval: time.Duration(envInt("DOGE_TOKEN_CACHE_CLEANUP_INTERVAL_SECONDS", int(defaultCleanupInterval.Seconds()))) * time.Second,
		BodyMemoryLimit:      envInt64("MAX_BODY_MEMORY_BYTES", defaultBodyMemoryLimit),
	}

	accounts, err := loadAccounts()
	if err != nil {
		return Config{}, err
	}
	cfg.Accounts = accounts
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if c.Addr == "" {
		return errors.New("GATEWAY_ADDR cannot be empty")
	}
	if c.DogeAPIBaseURL == "" {
		return errors.New("DOGE_API_BASE_URL cannot be empty")
	}
	if c.Region == "" {
		return errors.New("S3_REGION cannot be empty")
	}
	if c.Service == "" {
		return errors.New("S3_SERVICE cannot be empty")
	}
	if c.TmpTokenTTL < 0 {
		return errors.New("DOGE_TMP_TOKEN_TTL cannot be negative")
	}
	if c.BodyMemoryLimit < 0 {
		return errors.New("MAX_BODY_MEMORY_BYTES cannot be negative")
	}
	if c.TokenCleanupInterval < 0 {
		return errors.New("DOGE_TOKEN_CACHE_CLEANUP_INTERVAL_SECONDS cannot be negative")
	}
	seen := map[string]struct{}{}
	for i, account := range c.Accounts {
		if strings.TrimSpace(account.S3AccessKey) == "" {
			return fmt.Errorf("account %d has empty s3 access key", i)
		}
		if strings.TrimSpace(account.S3SecretKey) == "" {
			return fmt.Errorf("account %d has empty s3 secret key", i)
		}
		if strings.TrimSpace(account.DogeAccessKey) == "" {
			return fmt.Errorf("account %d has empty doge access key", i)
		}
		if strings.TrimSpace(account.DogeSecretKey) == "" {
			return fmt.Errorf("account %d has empty doge secret key", i)
		}
		if _, exists := seen[account.S3AccessKey]; exists {
			return fmt.Errorf("duplicate s3 access key %q", account.S3AccessKey)
		}
		seen[account.S3AccessKey] = struct{}{}
	}
	return nil
}

func loadDotenv() {
	paths := []string{}
	if p := strings.TrimSpace(os.Getenv("DOTENV")); p != "" {
		paths = append(paths, p)
	}
	if p := strings.TrimSpace(os.Getenv("ENV_FILE")); p != "" {
		paths = append(paths, p)
	}
	if len(paths) == 0 {
		paths = append(paths, defaultDotenvPath)
	}
	_ = godotenv.Load(paths...)
}

func loadAccounts() ([]Account, error) {
	if raw := strings.TrimSpace(os.Getenv(accountsJSONEnv)); raw != "" {
		var accounts []Account
		if err := json.Unmarshal([]byte(raw), &accounts); err != nil {
			return nil, fmt.Errorf("parse %s: %w", accountsJSONEnv, err)
		}
		return normalizeAccounts(accounts), nil
	}

	dogeAccessKey := firstEnv("DOGE_ACCESS_KEY", "DOGE_API_ID")
	dogeSecretKey := firstEnv("DOGE_SECRET_KEY", "DOGE_API_TOKEN")
	account := Account{
		S3AccessKey:   firstEnv("S3_ACCESS_KEY", "AWS_ACCESS_KEY_ID"),
		S3SecretKey:   firstEnv("S3_SECRET_KEY", "AWS_SECRET_ACCESS_KEY"),
		DogeAccessKey: dogeAccessKey,
		DogeSecretKey: dogeSecretKey,
	}
	if account.S3AccessKey == "" {
		account.S3AccessKey = dogeAccessKey
	}
	if account.S3SecretKey == "" {
		account.S3SecretKey = dogeSecretKey
	}
	if account.S3AccessKey == "" && account.S3SecretKey == "" && account.DogeAccessKey == "" && account.DogeSecretKey == "" {
		return nil, nil
	}
	return normalizeAccounts([]Account{account}), nil
}

func normalizeAccounts(accounts []Account) []Account {
	out := make([]Account, 0, len(accounts))
	for _, account := range accounts {
		account.S3AccessKey = strings.TrimSpace(account.S3AccessKey)
		account.S3SecretKey = strings.TrimSpace(account.S3SecretKey)
		account.DogeAccessKey = strings.TrimSpace(account.DogeAccessKey)
		account.DogeSecretKey = strings.TrimSpace(account.DogeSecretKey)
		out = append(out, account)
	}
	return out
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func envString(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return n
}

func envInt64(key string, fallback int64) int64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return n
}
