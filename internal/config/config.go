package config

import (
	"os"
	"strings"
)

type Config struct {
	Port               string
	DatabaseURL        string
	AppEnv             string
	AppURL             string // public URL of the frontend, used for OAuth redirects
	OwnerEmail         string // the only account allowed to sign in (GD-01)
	GoogleClientID     string
	GoogleClientSecret string
}

func Load() Config {
	return Config{
		Port:               env("PORT", "8080"),
		DatabaseURL:        env("DATABASE_URL", "postgres://rakuma:rakuma@localhost:5432/rakuma?sslmode=disable"),
		AppEnv:             env("APP_ENV", "development"),
		AppURL:             strings.TrimRight(env("APP_URL", "http://localhost:5173"), "/"),
		OwnerEmail:         strings.ToLower(strings.TrimSpace(env("RAKUMA_OWNER_EMAIL", "chushop.rakuma@gmail.com"))),
		GoogleClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
		GoogleClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
	}
}

func (c Config) GoogleEnabled() bool { return c.GoogleClientID != "" && c.GoogleClientSecret != "" }

// Dev login lets the owner sign in without Google, only outside production.
func (c Config) DevLoginEnabled() bool { return c.AppEnv != "production" }

func (c Config) SecureCookies() bool { return strings.HasPrefix(c.AppURL, "https://") }

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
