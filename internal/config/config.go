package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config holds all runtime settings (env-driven, single volume + single port).
type Config struct {
	Port         string
	DataDir      string
	DBPath       string
	RetentionHrs int
	MaxBodyBytes int64
	AccessToken  string
	PublicURL    string
	RateLimitRPS int
	Version      string
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getenvInt64(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}

// Load reads env vars per README config table.
func Load(version string) Config {
	dataDir := getenv("DATA_DIR", "./data")
	return Config{
		Port:         getenv("PORT", "8080"),
		DataDir:      dataDir,
		DBPath:       getenv("DATABASE_URL", dataDir+"/omnihook.db"),
		RetentionHrs: getenvInt("RETENTION_HOURS", 168),
		MaxBodyBytes: getenvInt64("MAX_BODY_BYTES", 1<<20), // 1 MB
		AccessToken:  os.Getenv("ACCESS_TOKEN"),
		PublicURL:    os.Getenv("PUBLIC_URL"),
		RateLimitRPS: getenvInt("RATE_LIMIT_RPS", 50),
		Version:      version,
	}
}

// Validate rejects nonsensical configuration at startup instead of failing
// obscurely at runtime (negative body caps panic on slicing; zero silently
// truncates every body; negative retention builds invalid SQL).
func (c Config) Validate() error {
	if c.Port == "" {
		return fmt.Errorf("PORT must not be empty")
	}
	if p, err := strconv.Atoi(c.Port); err != nil || p < 1 || p > 65535 {
		return fmt.Errorf("PORT must be 1-65535, got %q", c.Port)
	}
	if c.DataDir == "" && c.DBPath == "" {
		return fmt.Errorf("DATA_DIR or DATABASE_URL must be set")
	}
	if c.MaxBodyBytes <= 0 {
		return fmt.Errorf("MAX_BODY_BYTES must be > 0, got %d", c.MaxBodyBytes)
	}
	if c.RetentionHrs < 0 {
		return fmt.Errorf("RETENTION_HOURS must be >= 0, got %d", c.RetentionHrs)
	}
	if c.RateLimitRPS < 0 {
		return fmt.Errorf("RATE_LIMIT_RPS must be >= 0 (0 disables), got %d", c.RateLimitRPS)
	}
	return nil
}
