package config

import (
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

// Load reads env vars per PLAN.md section 3/4.
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
