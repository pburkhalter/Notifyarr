// Package config parses the bot's runtime configuration from environment
// variables. Keep this file the single source of truth for every knob.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the parsed, validated environment.
type Config struct {
	// HTTP listener for the webhook receiver + healthz.
	Listen string

	// WAHA — the WhatsApp HTTP API the bot reads from and writes to.
	WAHAURL      string // e.g. http://waha-nas:3000
	WAHAAPIKey   string // optional; sent as X-Api-Key
	WAHASession  string // e.g. "default"
	WAHAChatID   string // group jid: 1203...@g.us
	WAHABotPhone string // bot's own phone (digits only) — used to detect @mentions
	WAHABotLID   string // bot's WhatsApp linked-id (digits only) — modern groups
	// @-mention by LID, not by phone; we accept either.

	// Jellyseerr — request + search source of truth.
	SeerrURL    string
	SeerrAPIKey string

	// Sonarr v3.
	SonarrURL    string
	SonarrAPIKey string

	// Radarr v3.
	RadarrURL    string
	RadarrAPIKey string

	// Prowlarr — optional, powers the streaming dashboard's SceneNZB daily
	// grab-quota headroom. Empty URL/key disables that field of the
	// /streaming-status.json aggregator (it degrades, never fails).
	ProwlarrURL          string
	ProwlarrAPIKey       string
	ProwlarrQuotaIndexer string // Prowlarr indexer name to track (e.g. "SceneNZB")
	QuotaIndexerDailyCap int    // that indexer's grabs/day ceiling (e.g. 400)

	// HealthStatusFile is the healthcheck script's JSON drop, mounted
	// read-only into the container. The aggregator surfaces its issues[]
	// as the dashboard's error panel. Empty disables the issues field.
	HealthStatusFile string

	// Jellyfin — recently-added, library counts, posters.
	JellyfinURL    string
	JellyfinAPIKey string
	JellyfinUserID string // admin user used for /Users/{id}/Items queries

	// Library VirtualFolder ids — looked up once at deploy time via
	// /Library/VirtualFolders. Used by the stats command to count only
	// the visible items rather than the global Items table (which leaks
	// orphans from destroyed libraries). Empty disables per-library
	// counting and falls back to global counts.
	JellyfinMovieLibraryID  string
	JellyfinSeriesLibraryID string

	// External, user-facing URLs included in reply text + welcome message.
	JellyfinExternalURL string
	SeerrExternalURL    string

	// Cron expressions; empty disables that job.
	CronWeeklyDigest string
	CronWeeklyPoll   string
	CronDailyHealth  string

	// PhoneMap maps a Jellyseerr username (lowercased) to a WhatsApp phone
	// (digits only). Populated from every env var prefixed PHONE_MAP_, so
	// adding a user is one new env var, not a code change.
	PhoneMap map[string]string

	// SQLite path for poll state + welcome dedup.
	DBPath string

	// Logger config.
	LogLevel  string // debug | info | warn | error
	LogFormat string // json | text

	// HTTPTimeout caps every outbound API call.
	HTTPTimeout time.Duration

	// Journarr flow-tracker callback (optional). When set, the concierge
	// POSTs a "notification.sent" event after each delivered WhatsApp
	// notification so Journarr can mark items 'notified'. Empty URL disables.
	JournarrCallbackURL   string
	JournarrCallbackToken string

	// WAHASendImages controls whether the flush worker attempts SendImage
	// at all. WAHA Core on the NOWEB engine returns 422 for that endpoint,
	// so on a Core deployment every grouped Sonarr push wastes one HTTP
	// round-trip to WAHA, eats a warn-log line, then falls back to text.
	// Set false (the default) to skip the image path entirely and go
	// straight to SendText. Flip to true once WAHA is upgraded to Plus or
	// switched to WEBJS.
	WAHASendImages bool
}

// LoadFromOS parses os.Environ(). Use in main.
func LoadFromOS() (*Config, error) { return Load(os.Environ()) }

// Load parses a KEY=VALUE slice (as returned by os.Environ). Pure function
// so tests can pass arbitrary fixtures.
func Load(environ []string) (*Config, error) {
	envMap := make(map[string]string, len(environ))
	for _, kv := range environ {
		if eq := strings.IndexByte(kv, '='); eq > 0 {
			envMap[kv[:eq]] = kv[eq+1:]
		}
	}
	get := func(k string) string { return envMap[k] }

	c := &Config{
		Listen:                  defaulted(get, "LISTEN", ":8080"),
		WAHAURL:                 strings.TrimRight(get("WAHA_URL"), "/"),
		WAHAAPIKey:              get("WAHA_API_KEY"),
		WAHASession:             defaulted(get, "WAHA_SESSION", "default"),
		WAHAChatID:              get("WAHA_CHAT_ID"),
		WAHABotPhone:            digitsOnly(get("WAHA_BOT_PHONE")),
		WAHABotLID:              digitsOnly(get("WAHA_BOT_LID")),
		SeerrURL:                strings.TrimRight(get("SEERR_URL"), "/"),
		SeerrAPIKey:             get("SEERR_API_KEY"),
		SonarrURL:               strings.TrimRight(get("SONARR_URL"), "/"),
		SonarrAPIKey:            get("SONARR_API_KEY"),
		RadarrURL:               strings.TrimRight(get("RADARR_URL"), "/"),
		RadarrAPIKey:            get("RADARR_API_KEY"),
		ProwlarrURL:             strings.TrimRight(get("PROWLARR_URL"), "/"),
		ProwlarrAPIKey:          get("PROWLARR_API_KEY"),
		ProwlarrQuotaIndexer:    defaulted(get, "PROWLARR_QUOTA_INDEXER", "SceneNZB"),
		QuotaIndexerDailyCap:    parseInt(get("QUOTA_INDEXER_DAILY_CAP"), 400),
		HealthStatusFile:        get("HEALTH_STATUS_FILE"),
		JellyfinURL:             strings.TrimRight(get("JELLYFIN_URL"), "/"),
		JellyfinAPIKey:          get("JELLYFIN_API_KEY"),
		JellyfinUserID:          get("JELLYFIN_USER_ID"),
		JellyfinMovieLibraryID:  get("JELLYFIN_MOVIE_LIBRARY_ID"),
		JellyfinSeriesLibraryID: get("JELLYFIN_SERIES_LIBRARY_ID"),
		JellyfinExternalURL:     strings.TrimRight(get("JELLYFIN_EXTERNAL_URL"), "/"),
		SeerrExternalURL:        strings.TrimRight(get("SEERR_EXTERNAL_URL"), "/"),
		CronWeeklyDigest:        defaulted(get, "CRON_WEEKLY_DIGEST", "0 9 * * 0"),
		CronWeeklyPoll:          defaulted(get, "CRON_WEEKLY_POLL", "0 19 * * 5"),
		CronDailyHealth:         defaulted(get, "CRON_DAILY_HEALTH", "0 8 * * *"),
		PhoneMap:                phoneMapFrom(envMap),
		DBPath:                  defaulted(get, "DB_PATH", "/data/concierge.db"),
		LogLevel:                defaulted(get, "LOG_LEVEL", "info"),
		LogFormat:               defaulted(get, "LOG_FORMAT", "json"),
		HTTPTimeout:             parseDuration(get("HTTP_TIMEOUT"), 30*time.Second),
		WAHASendImages:          parseBool(get("WAHA_SEND_IMAGES"), false),
		JournarrCallbackURL:     strings.TrimRight(get("JOURNARR_CALLBACK_URL"), "/"),
		JournarrCallbackToken:   get("JOURNARR_CALLBACK_TOKEN"),
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// Validate enforces the minimum set of fields the bot needs to operate.
func (c *Config) Validate() error {
	for _, p := range []struct{ name, val string }{
		{"WAHA_URL", c.WAHAURL},
		{"WAHA_CHAT_ID", c.WAHAChatID},
		{"WAHA_BOT_PHONE", c.WAHABotPhone},
		{"SEERR_URL", c.SeerrURL},
		{"SEERR_API_KEY", c.SeerrAPIKey},
		{"SONARR_URL", c.SonarrURL},
		{"SONARR_API_KEY", c.SonarrAPIKey},
		{"RADARR_URL", c.RadarrURL},
		{"RADARR_API_KEY", c.RadarrAPIKey},
		{"JELLYFIN_URL", c.JellyfinURL},
		{"JELLYFIN_API_KEY", c.JellyfinAPIKey},
		{"JELLYFIN_USER_ID", c.JellyfinUserID},
		{"JELLYFIN_EXTERNAL_URL", c.JellyfinExternalURL},
		{"SEERR_EXTERNAL_URL", c.SeerrExternalURL},
	} {
		if p.val == "" {
			return fmt.Errorf("%s is required", p.name)
		}
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("LOG_LEVEL must be one of debug|info|warn|error, got %q", c.LogLevel)
	}
	switch c.LogFormat {
	case "json", "text":
	default:
		return fmt.Errorf("LOG_FORMAT must be one of json|text, got %q", c.LogFormat)
	}
	return nil
}

// MentionToken is the primary literal "@<digits>" string the bot expects
// at the start of an incoming message. For modern WhatsApp groups the
// real prefix is the LID, not the phone — see MentionTokens for the full
// list the parser should accept.
func (c *Config) MentionToken() string { return "@" + c.WAHABotPhone }

// MentionTokens returns every "@<digits>" prefix the bot answers to. In
// LID-style groups WhatsApp inserts "@<lid>", in older/personal chats it
// inserts "@<phone>". We accept both so the user doesn't have to know
// which one their WhatsApp client picks.
func (c *Config) MentionTokens() []string {
	out := []string{"@" + c.WAHABotPhone}
	if c.WAHABotLID != "" {
		out = append(out, "@"+c.WAHABotLID)
	}
	return out
}

func defaulted(get func(string) string, key, def string) string {
	if v := get(key); v != "" {
		return v
	}
	return def
}

func parseBool(s string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "yes", "1", "on":
		return true
	case "false", "no", "0", "off":
		return false
	}
	return def
}

func parseInt(s string, def int) int {
	if s == "" {
		return def
	}
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}

func parseDuration(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d
	}
	return def
}

func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func phoneMapFrom(envMap map[string]string) map[string]string {
	const prefix = "PHONE_MAP_"
	out := map[string]string{}
	for k, v := range envMap {
		if !strings.HasPrefix(k, prefix) || v == "" {
			continue
		}
		username := strings.ToLower(strings.TrimPrefix(k, prefix))
		out[username] = digitsOnly(v)
	}
	return out
}
