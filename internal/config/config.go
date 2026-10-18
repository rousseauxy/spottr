package config

import (
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
)

// Auth modes. Anything else is rejected at startup.
const (
	// AuthModePassword requires APP_PASSWORD and gates privileged endpoints
	// behind a session cookie. This is the default.
	AuthModePassword = "password"
	// AuthModeProxy trusts an upstream authenticating reverse proxy
	// (Authentik, oauth2-proxy, ...). Privileged endpoints are open to
	// anything that reaches the app, so the proxy MUST be the only path in.
	AuthModeProxy = "proxy"
	// AuthModeOpen disables authentication entirely. Explicit opt-in only.
	AuthModeOpen = "open"
)

// Moderation modes decide what a validated withdrawal request does.
const (
	// ModerationOff ignores moderation commands entirely.
	ModerationOff = "off"
	// ModerationMark flags the spot as moderated so it stops being served,
	// keeping the row. Reversible.
	ModerationMark = "mark"
	// ModerationDelete removes the spot, as Spotweb's default does.
	ModerationDelete = "delete"
)

// Config holds all application configuration, loaded from environment variables.
type Config struct {
	// Server
	ListenAddr string
	// TrustedProxies lists CIDRs whose X-Forwarded-For/X-Real-IP headers are
	// believed. Empty means trust nobody and always use the socket peer.
	TrustedProxies []netip.Prefix

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
	// BackfillEnabled starts a background worker that walks history backwards
	// from the lookback window towards the group's first article.
	BackfillEnabled bool

	// SABnzbd
	SABHost   string
	SABPort   int
	SABAPIKey string
	SABTLS    bool

	// Auth
	AuthMode        string // AUTH_MODE — password | proxy | open
	AppPassword     string // APP_PASSWORD — required when AuthMode == password
	SessionDuration time.Duration
	SecureCookie    bool // SECURE_COOKIE — set the Secure flag on session cookies
	// ProxyAuthHeader is the header an authenticating proxy sets on a request it let through
	// (Authentik's forward auth: X-authentik-username). With AUTH_MODE=proxy it is believed only
	// from a peer in TRUSTED_PROXIES; it opens the NZB download route to signed-in browsers.
	ProxyAuthHeader string
	AllowAdult      bool // ALLOW_ADULT — show 18+ spots (default: false)

	// API
	// APIKey is an optional override. When empty the key is read from (or
	// generated into) the database so it survives restarts. See db.APIKey.
	APIKey string

	// Limits — these bound how expensive a single anonymous request can be.
	MaxPageSize     int   // MAX_PAGE_SIZE — hard cap on limit=
	MaxOffset       int   // MAX_OFFSET — hard cap on offset=
	MaxArticleBytes int64 // MAX_ARTICLE_BYTES — cap on a single NNTP article read
	MaxNZBBytes     int64 // MAX_NZB_BYTES — cap on decompressed NZB size
	RateLimitRPS    float64
	RateLimitBurst  int

	// Retention / cache
	RetentionDays int   // RETENTION_DAYS — delete spots older than this (0 = keep forever)
	CacheMaxBytes int64 // CACHE_MAX_BYTES — evict NZB/image blobs beyond this

	// Content moderation
	BlacklistURL      string        // BLACKLIST_URL — external spotter blacklist (XML)
	WhitelistURL      string        // WHITELIST_URL — external spotter whitelist (XML)
	BlacklistInterval time.Duration // how often to refresh the lists
	VerifySignatures  bool          // VERIFY_SIGNATURES — drop spots failing RSA check
	ModerationMode    string        // MODERATION_MODE — off | mark | delete

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
		BackfillEnabled: envBool("BACKFILL", false),

		SABHost:   envOr("SAB_HOST", ""),
		SABPort:   envInt("SAB_PORT", 8080),
		SABAPIKey: envOr("SAB_API_KEY", ""),
		SABTLS:    envBool("SAB_TLS", false),

		AuthMode:        strings.ToLower(envOr("AUTH_MODE", AuthModePassword)),
		AppPassword:     envOr("APP_PASSWORD", ""),
		SessionDuration: envDuration("SESSION_DURATION", 24*time.Hour),
		SecureCookie:    envBool("SECURE_COOKIE", false),
		ProxyAuthHeader: envOr("PROXY_AUTH_HEADER", "X-authentik-username"),
		AllowAdult:      envBool("ALLOW_ADULT", false),

		APIKey: envOr("API_KEY", ""),

		MaxPageSize:     envInt("MAX_PAGE_SIZE", 200),
		MaxOffset:       envInt("MAX_OFFSET", 100000),
		MaxArticleBytes: envInt64("MAX_ARTICLE_BYTES", 32<<20), // 32 MiB
		MaxNZBBytes:     envInt64("MAX_NZB_BYTES", 64<<20),     // 64 MiB decompressed
		RateLimitRPS:    envFloat("RATE_LIMIT_RPS", 5),
		RateLimitBurst:  envInt("RATE_LIMIT_BURST", 30),

		RetentionDays: envInt("RETENTION_DAYS", 0),
		CacheMaxBytes: envInt64("CACHE_MAX_BYTES", 512<<20), // 512 MiB

		BlacklistURL:      envOr("BLACKLIST_URL", ""),
		WhitelistURL:      envOr("WHITELIST_URL", ""),
		BlacklistInterval: envDuration("BLACKLIST_INTERVAL", 12*time.Hour),
		VerifySignatures:  envBool("VERIFY_SIGNATURES", false),
		ModerationMode:    strings.ToLower(envOr("MODERATION_MODE", ModerationOff)),

		TZ: envOr("TZ", "UTC"),
	}

	if cfg.NNTPHost == "" {
		return nil, fmt.Errorf("NNTP_HOST is required")
	}

	switch cfg.AuthMode {
	case AuthModePassword:
		// Fail closed: refuse to start rather than silently serving privileged
		// endpoints (SABnzbd control, download queue) to anonymous callers.
		if cfg.AppPassword == "" {
			return nil, fmt.Errorf(
				"APP_PASSWORD is required when AUTH_MODE=password; " +
					"set a password, or set AUTH_MODE=proxy if an authenticating reverse proxy " +
					"is the only route to this service, or AUTH_MODE=open to deliberately run without auth")
		}
	case AuthModeProxy, AuthModeOpen:
		// Both intentionally leave privileged endpoints ungated inside the app.
	default:
		return nil, fmt.Errorf("invalid AUTH_MODE %q (want %q, %q or %q)",
			cfg.AuthMode, AuthModePassword, AuthModeProxy, AuthModeOpen)
	}

	switch cfg.ModerationMode {
	case ModerationOff, ModerationMark, ModerationDelete:
	default:
		return nil, fmt.Errorf("invalid MODERATION_MODE %q (want %q, %q or %q)",
			cfg.ModerationMode, ModerationOff, ModerationMark, ModerationDelete)
	}

	proxies, err := parseCIDRs(envOr("TRUSTED_PROXIES", ""))
	if err != nil {
		return nil, fmt.Errorf("TRUSTED_PROXIES: %w", err)
	}
	cfg.TrustedProxies = proxies

	if cfg.NNTPMaxConns < 1 {
		cfg.NNTPMaxConns = 1
	}
	if cfg.MaxPageSize < 1 {
		cfg.MaxPageSize = 1
	}

	return cfg, nil
}

// AuthEnforced reports whether the app itself gates privileged endpoints.
func (c *Config) AuthEnforced() bool { return c.AuthMode == AuthModePassword }

// parseCIDRs accepts a comma-separated list of CIDRs or bare IPs.
func parseCIDRs(s string) ([]netip.Prefix, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	var out []netip.Prefix
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if p, err := netip.ParsePrefix(part); err == nil {
			out = append(out, p)
			continue
		}
		addr, err := netip.ParseAddr(part)
		if err != nil {
			return nil, fmt.Errorf("%q is neither a CIDR nor an IP", part)
		}
		out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return out, nil
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

func envInt64(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
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
