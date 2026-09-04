// Package config parses the same environment-variable surface the current
// Node server uses, so the Go binary can be dropped into the existing
// systemd unit and .env files without a config migration of its own.
package config

import (
	"os"
	"strconv"
)

type Config struct {
	NodeEnv      string
	Port         string
	Host         string
	PublicURL    string
	DatabasePath string
	LogLevel     string

	SessionSecret string
	TrustProxy    bool
	CookieSecure  bool

	AdminPlexIDs []string

	PlexClientID string

	SpotifyClientID     string
	SpotifyClientSecret string
	SpotifyRedirectURI  string

	DevNoAuth bool
}

func Load() Config {
	return Config{
		NodeEnv:      getEnv("NODE_ENV", "development"),
		Port:         getEnv("PORT", "3001"),
		Host:         getEnv("HOST", "0.0.0.0"),
		PublicURL:    getEnv("PUBLIC_URL", "http://127.0.0.1:3001"),
		DatabasePath: getEnv("DATABASE_PATH", "./data/playlist-lab.db"),
		LogLevel:     getEnv("LOG_LEVEL", "info"),

		// Must match utils/encryption.ts's fallback exactly: it's also used
		// to derive the AES key for values (OAuth tokens, API keys) already
		// encrypted by the Node server in the production database this
		// binary reads at cutover - a different default here would make
		// every such value permanently undecryptable.
		SessionSecret: getEnv("SESSION_SECRET", "default-secret-change-in-production"),
		TrustProxy:    getBool("TRUST_PROXY", false),
		CookieSecure:  getBool("COOKIE_SECURE", false),

		PlexClientID: getEnv("PLEX_CLIENT_ID", "playlist-lab-server"),

		SpotifyClientID:     getEnv("SPOTIFY_CLIENT_ID", ""),
		SpotifyClientSecret: getEnv("SPOTIFY_CLIENT_SECRET", ""),
		SpotifyRedirectURI:  getEnv("SPOTIFY_REDIRECT_URI", ""),

		DevNoAuth: getBool("DEV_NO_AUTH", false),
	}
}

func (c Config) IsProduction() bool {
	return c.NodeEnv == "production"
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}
