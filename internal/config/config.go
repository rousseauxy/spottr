package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds all application configuration, loaded from environment variables.
type Config struct {
	// Server
	ListenAddr string

	// Database
	DBPath string

	// Usenet / NNTP
	NNTPHost     string
	NNTPPort     int
	NNTPUser     string
	NNTPPass     string
	NNTPTLS      bool
	NNTPMaxConns int

	// Sync
	SyncInterval time.Duration
	SyncLookback int // number of articles to look back on first run

	// SABnzbd
	SABHost   string
	SABPort   int
	SABAPIKey string
	SABTLS    bool

	// Auth
        AppPassword     string        // APP_PASSWORD — if empty, auth is disabled
        SessionDuration time.Duration
	AllowAdult bool   // ALLOW_ADULT — show 18+ spots (default: false)

	// API
	APIKey string // API_KEY — required key for API access; defaults to JWTSecret

	// Misc
	TZ string
}

func Load() (*Config, error) {
	cfg := &Config{
		ListenAddr:      envOr("LISTEN_ADDR", ":8080"),
		DBPath:          envOr("DB_PATH", "/data/spottr.db"),
		NNTPHost:        envOr("NNTP_HOST", ""),
		NNTPPort:        envInt("NNTP_PORT", 119),
		NNTPUser:        envOr("NNTP_USER", ""),
		NNTPPass:        envOr("NNTP_PASS", ""),
		NNTPTLS:         envBool("NNTP_TLS", false),
		NNTPMaxConns:    envInt("NNTP_MAX_CONNS", 4),
		SyncInterval:    envDuration("SYNC_INTERVAL", 15*time.Minute),
		SyncLookback:    envInt("SYNC_LOOKBACK", 500000),
		SABHost:         envOr("SAB_HOST", ""),
		SABPort:         envInt("SAB_PORT", 8080),
		SABAPIKey:       envOr("SAB_API_KEY", ""),
		SABTLS:          envBool("SAB_TLS", false),
                AppPassword:     envOr("APP_PASSWORD", ""),
                SessionDuration: envDuration("SESSION_DURATION", 24*time.Hour),
                AllowAdult:      envBool("ALLOW_ADULT", false),
                APIKey:          envOr("API_KEY", ""),
                TZ:              envOr("TZ", "UTC"),
        }

        if cfg.NNTPHost == "" {
                return nil, fmt.Errorf("NNTP_HOST is required")
        }
        // If no explicit API_KEY, generate a random one so newznab is still
        // usable without exposing any secret from the environment.
        if cfg.APIKey == "" {
                cfg.APIKey = randomHex(16)
        }

        return cfg, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("config: rand.Read: " + err.Error())
	}
	return hex.EncodeToString(b)
}
