package config

import (
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	base := Config{Port: "8080", DataDir: "./data", DBPath: "./data/x.db",
		RetentionHrs: 168, MaxBodyBytes: 1 << 20, RateLimitRPS: 50, Version: "test"}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid cfg: %v", err)
	}
	cases := []struct {
		name string
		mut  func(*Config)
		want string
	}{
		{"port empty", func(c *Config) { c.Port = "" }, "PORT"},
		{"port bad", func(c *Config) { c.Port = "abc" }, "PORT"},
		{"port zero", func(c *Config) { c.Port = "0" }, "PORT"},
		{"no dirs", func(c *Config) { c.DataDir, c.DBPath = "", "" }, "DATA_DIR"},
		{"body zero", func(c *Config) { c.MaxBodyBytes = 0 }, "MAX_BODY_BYTES"},
		{"body negative", func(c *Config) { c.MaxBodyBytes = -5 }, "MAX_BODY_BYTES"},
		{"retention negative", func(c *Config) { c.RetentionHrs = -1 }, "RETENTION_HOURS"},
		{"rps negative", func(c *Config) { c.RateLimitRPS = -1 }, "RATE_LIMIT_RPS"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := base
			tc.mut(&c)
			err := c.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v, want containing %q", err, tc.want)
			}
		})
	}
}
