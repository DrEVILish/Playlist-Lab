// Package config parses the same environment-variable surface the current
// Node server uses, so the Go binary can be dropped into the existing
// systemd unit and .env files without a config migration of its own.
package config

import (
	"os"
	"strconv"
	"strings"
)

type Config struct {
	NodeEnv      string
	Port         string
	Host         string
	PublicURL    string
	DatabasePath string
	LogLevel     string
	LogDir       string

	SessionSecret string
	TrustProxy    bool
	CookieSecure  bool

	AdminPlexIDs []string

	PlexClientID string

	SpotifyClientID     string
	SpotifyClientSecret string
	SpotifyRedirectURI  string

	YouTubeClientID     string
	YouTubeClientSecret string
	YouTubeRedirectURI  string

	DevNoAuth bool

	EnableJobs              bool
	EnableCacheCleanup      bool
	EnableScraperJob        bool
	ScraperSchedule         string
	EnableScheduleChecker   bool
	ScheduleCheckerSchedule string

	// Deemix (services/deemix.ts). DeemixURL/DeemixConfigPath/ServiceName
	// point at our own local deemix-server install and are pure env config.
	// DeemixArl is also DB-persisted admin config on the Node side
	// (Settings > Admin > Deemix, editable without touching .env) - that
	// admin UI hasn't been ported to Go yet, so this only reads the env var
	// fallback for now; same value, just missing the in-app edit path.
	DeemixURL              string
	DeemixConfigPath       string
	DeemixServiceName      string
	DeemixArl              string
	EnableDeemixArlCheck   bool
	DeemixArlCheckSchedule string

	// Lidarr (services/lidarr.ts) - same admin-DB-override caveat as
	// DeemixArl above, env var only for now.
	LidarrURL    string
	LidarrAPIKey string
}

func Load() Config {
	return Config{
		NodeEnv:      getEnv("NODE_ENV", "development"),
		Port:         getEnv("PORT", "3001"),
		Host:         getEnv("HOST", "0.0.0.0"),
		PublicURL:    getEnv("PUBLIC_URL", "http://127.0.0.1:3001"),
		DatabasePath: getEnv("DATABASE_PATH", "./data/playlist-lab.db"),
		LogLevel:     getEnv("LOG_LEVEL", "info"),
		LogDir:       getEnv("LOG_DIR", "./logs"),

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

		YouTubeClientID:     getEnv("YOUTUBE_CLIENT_ID", ""),
		YouTubeClientSecret: getEnv("YOUTUBE_CLIENT_SECRET", ""),
		YouTubeRedirectURI:  getEnv("YOUTUBE_REDIRECT_URI", ""),

		DevNoAuth: getBool("DEV_NO_AUTH", false),

		EnableJobs:              getBool("ENABLE_JOBS", false),
		EnableCacheCleanup:      getBool("ENABLE_CACHE_CLEANUP", true),
		EnableScraperJob:        getBool("ENABLE_SCRAPER_JOB", true),
		ScraperSchedule:         getEnv("SCRAPER_SCHEDULE", "0 2 * * *"), // 2:00 AM daily
		EnableScheduleChecker:   getBool("ENABLE_SCHEDULE_CHECKER", true),
		ScheduleCheckerSchedule: getEnv("SCHEDULE_CHECKER_SCHEDULE", "0,10,20,30,40,50 * * * *"), // every 10 minutes

		DeemixURL:              strings.TrimSuffix(getEnv("DEEMIX_URL", "http://127.0.0.1:6595"), "/"),
		DeemixConfigPath:       getEnv("DEEMIX_CONFIG_PATH", "/opt/deemix-server/config/config.json"),
		DeemixServiceName:      getEnv("DEEMIX_SERVICE_NAME", "deemix-server.service"),
		DeemixArl:              getEnv("DEEMIX_ARL", ""),
		EnableDeemixArlCheck:   getBool("ENABLE_DEEMIX_ARL_CHECK", true),
		DeemixArlCheckSchedule: getEnv("DEEMIX_ARL_CHECK_SCHEDULE", "0 4 * * *"),

		LidarrURL:    strings.TrimSuffix(getEnv("LIDARR_URL", ""), "/"),
		LidarrAPIKey: getEnv("LIDARR_API_KEY", ""),
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
