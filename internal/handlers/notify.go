package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/pburkhalter/waha-concierge/internal/jellyfin"
	"github.com/pburkhalter/waha-concierge/internal/journarr"
	"github.com/pburkhalter/waha-concierge/internal/seerr"
	"github.com/pburkhalter/waha-concierge/internal/store"
	"github.com/pburkhalter/waha-concierge/internal/waha"
)

// ─── inbound webhook payloads (Sonarr/Radarr "Connect → Webhook") ────────

// sonarrWebhook is a tolerant subset of Sonarr's outgoing Connect webhook.
// We only read what we actually use; the real payload has many more fields.
type sonarrWebhook struct {
	EventType string `json:"eventType"`
	Series    struct {
		ID       int    `json:"id"`
		Title    string `json:"title"`
		TmdbID   int    `json:"tmdbId"`
		TvdbID   int    `json:"tvdbId"`
		ImagesV2 []struct {
			RemoteURL string `json:"remoteUrl"`
			Type      string `json:"coverType"`
		} `json:"images"`
	} `json:"series"`
	Episodes []struct {
		Title         string `json:"title"`
		SeasonNumber  int    `json:"seasonNumber"`
		EpisodeNumber int    `json:"episodeNumber"`
	} `json:"episodes"`
}

// radarrWebhook is a tolerant subset of Radarr's outgoing Connect webhook.
type radarrWebhook struct {
	EventType string `json:"eventType"`
	Movie     struct {
		ID       int    `json:"id"`
		Title    string `json:"title"`
		Year     int    `json:"year"`
		TmdbID   int    `json:"tmdbId"`
		ImagesV2 []struct {
			RemoteURL string `json:"remoteUrl"`
			Type      string `json:"coverType"`
		} `json:"images"`
	} `json:"movie"`
}

// ─── routing ──────────────────────────────────────────────────────────────

// WebhookHandler returns the http.Handler the bot exposes for upstream
// Sonarr+Radarr Connect webhooks. Mount at e.g. /webhook/sonarr and
// /webhook/radarr respectively in main.
func (b *Bot) WebhookHandler(source string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "body", http.StatusBadRequest)
			return
		}
		// Always 200 — upstream retries are noisy and uninformative.
		go func(payload []byte) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := b.processWebhook(ctx, source, payload); err != nil {
				b.Log.Warn("notify webhook failed",
					"err", err, "source", source, "body_len", len(payload))
			}
		}(body)
		w.WriteHeader(http.StatusOK)
	})
}

func (b *Bot) processWebhook(ctx context.Context, source string, payload []byte) error {
	switch source {
	case "sonarr":
		var ev sonarrWebhook
		if err := json.Unmarshal(payload, &ev); err != nil {
			return err
		}
		return b.handleSonarr(ctx, ev)
	case "radarr":
		var ev radarrWebhook
		if err := json.Unmarshal(payload, &ev); err != nil {
			return err
		}
		return b.handleRadarr(ctx, ev)
	}
	return fmt.Errorf("unknown source %q", source)
}

// ─── sonarr (episodes) ────────────────────────────────────────────────────

// handleSonarr buffers Download events. The flush worker batches them per
// (series, season) before sending so a season import doesn't spam the
// chat with 20 individual pings.
func (b *Bot) handleSonarr(ctx context.Context, ev sonarrWebhook) error {
	if ev.EventType != "Download" && ev.EventType != "Upgrade" {
		return nil
	}
	if len(ev.Episodes) == 0 || ev.Series.Title == "" {
		return nil
	}
	ep := ev.Episodes[0]
	showKey := fmt.Sprintf("sonarr:%d:%d", ev.Series.ID, ep.SeasonNumber)
	displayName := fmt.Sprintf("%s S%02d", ev.Series.Title, ep.SeasonNumber)

	payload := pendingPayload{
		SeriesTitle: ev.Series.Title,
		Season:      ep.SeasonNumber,
		Episode:     ep.EpisodeNumber,
		EpisodeName: ep.Title,
		TmdbID:      ev.Series.TmdbID,
		PosterURL:   pickPoster(ev.Series.ImagesV2, "poster"),
	}
	raw, _ := json.Marshal(payload)
	return b.Store.EnqueuePendingImport(ctx, showKey, displayName, string(raw))
}

// ─── radarr (movies) ──────────────────────────────────────────────────────

// handleRadarr ships a movie notification immediately — no batching since
// movies arrive one at a time and the user expects a fast ping.
func (b *Bot) handleRadarr(ctx context.Context, ev radarrWebhook) error {
	if ev.EventType != "Download" && ev.EventType != "MovieFileImported" {
		return nil
	}
	if ev.Movie.Title == "" {
		return nil
	}
	body, mentions := b.formatMovieNotice(ctx, ev)
	poster := pickPoster(ev.Movie.ImagesV2, "poster")
	// WAHA Core + NOWEB rejects SendImage with 422; fall back to plain text
	// so the chat at least gets the title/year/link.
	if poster != "" {
		if _, err := b.WAHA.SendImage(ctx, b.Cfg.WAHAChatID, poster, body, mentions); err == nil {
			b.Journarr.NotifyMovie(ev.Movie.TmdbID, ev.Movie.Title)
			return nil
		} else {
			b.Log.Warn("sendImage failed for movie, falling back to text", "err", err, "movie", ev.Movie.Title)
		}
	}
	if _, err := b.WAHA.SendText(ctx, b.Cfg.WAHAChatID, body, mentions); err != nil {
		return err
	}
	b.Journarr.NotifyMovie(ev.Movie.TmdbID, ev.Movie.Title)
	return nil
}

func (b *Bot) formatMovieNotice(ctx context.Context, ev radarrWebhook) (string, []string) {
	year := ""
	if ev.Movie.Year > 0 {
		year = fmt.Sprintf(" (%d)", ev.Movie.Year)
	}
	link := b.jellyfinLink(ctx, ev.Movie.TmdbID, "movie")

	body := fmt.Sprintf("🎬 *Film:* %s%s\n🍿 %s", ev.Movie.Title, year, link)
	return body, nil
}

// jellyfinLink resolves the TMDB id to a Jellyfin item and builds the
// web-client deep link. Falls back to the bare library URL when the item
// hasn't been scanned yet.
func (b *Bot) jellyfinLink(ctx context.Context, tmdbID int, mediaType string) string {
	if it, err := b.Jellyfin.FindByTMDB(ctx, tmdbID, mediaType); err == nil {
		if u := jellyfin.ItemWebURL(b.Cfg.JellyfinExternalURL, it.ID, it.ServerID); u != "" {
			return u
		}
	}
	return b.Cfg.JellyfinExternalURL
}

// ─── grouping flush ───────────────────────────────────────────────────────

// FlushPending posts buffered Sonarr notifications. Call from a periodic
// ticker (e.g. every 60s) with a `wait` like 10*time.Minute — anything
// older than wait gets grouped and sent. When the buffered payload has
// a poster URL, the bot uses SendImage so the show poster renders inline.
func (b *Bot) FlushPending(ctx context.Context, wait, quietPeriod time.Duration) error {
	groups, err := b.Store.DueImports(ctx, wait, quietPeriod)
	if err != nil {
		return err
	}
	for showKey, items := range groups {
		if len(items) == 0 {
			continue
		}
		body, mentions, ids, poster, tmdbID, seriesTitle, eps := b.formatEpisodeGroup(ctx, showKey, items)

		var sendErr error
		// WAHASendImages defaults false: on Core+NOWEB the SendImage path
		// returns 422 every time and the row gets stuck in the retry loop.
		// Set true once WAHA is on Plus or WEBJS — then the image path runs
		// first and falls back to text on any failure.
		if poster != "" && b.Cfg.WAHASendImages {
			if _, sendErr = b.WAHA.SendImage(ctx, b.Cfg.WAHAChatID, poster, body, mentions); sendErr != nil {
				b.Log.Warn("sendImage failed, falling back to text", "err", sendErr, "show", showKey)
				_, sendErr = b.WAHA.SendText(ctx, b.Cfg.WAHAChatID, body, mentions)
			}
		} else {
			_, sendErr = b.WAHA.SendText(ctx, b.Cfg.WAHAChatID, body, mentions)
		}

		if sendErr != nil {
			// Two failures means something durable is wrong (WAHA unreachable,
			// session disconnected, …) — mark flushed anyway so the row
			// doesn't pile up forever and the operator can spot the issue in
			// logs. Loud-warn so it's noticed.
			b.Log.Error("flush send failed, marking flushed to prevent pile-up",
				"err", sendErr, "show", showKey, "items", len(items))
			if err := b.Store.MarkFlushed(ctx, ids); err != nil {
				b.Log.Warn("mark flushed failed", "err", err, "show", showKey)
			}
			continue
		}

		b.Log.Info("flush sent", "show", showKey, "items", len(items))
		if err := b.Store.MarkFlushed(ctx, ids); err != nil {
			b.Log.Warn("mark flushed failed", "err", err, "show", showKey)
		}
		b.Journarr.NotifyEpisodes(tmdbID, seriesTitle, eps)
	}
	return nil
}

// pendingPayload is the json we stuff into the store while waiting to
// flush. Kept small — only what the format step actually reads.
type pendingPayload struct {
	SeriesTitle string `json:"series_title"`
	Season      int    `json:"season"`
	Episode     int    `json:"episode"`
	EpisodeName string `json:"episode_name"`
	TmdbID      int    `json:"tmdb_id"`
	PosterURL   string `json:"poster_url"`
}

// formatEpisodeGroup turns N buffered single-episode rows into one tidy
// WhatsApp message. Mentions the requester of the series (if any) once
// per group, not per episode. Returns the poster URL alongside so the
// caller can pick SendImage vs SendText.
func (b *Bot) formatEpisodeGroup(ctx context.Context, _ string, items []store.PendingImport) (body string, mentions []string, ids []int64, poster string, tmdbID int, seriesTitle string, eps []journarr.Episode) {
	if len(items) == 0 {
		return "", nil, nil, "", 0, "", nil
	}
	ids = make([]int64, 0, len(items))
	episodes := make([]string, 0, len(items))
	season := 0
	for _, it := range items {
		ids = append(ids, it.ID)
		var p pendingPayload
		_ = json.Unmarshal([]byte(it.PayloadJSON), &p)
		if tmdbID == 0 {
			tmdbID = p.TmdbID
		}
		if seriesTitle == "" {
			seriesTitle = p.SeriesTitle
		}
		if season == 0 {
			season = p.Season
		}
		if poster == "" {
			poster = p.PosterURL
		}
		eps = append(eps, journarr.Episode{Season: p.Season, Episode: p.Episode})
		episodes = append(episodes,
			fmt.Sprintf("  • S%02dE%02d — %s", p.Season, p.Episode, truncate(p.EpisodeName, 50)))
	}
	if seriesTitle == "" {
		seriesTitle = items[0].DisplayName
	}

	// Resolve the Jellyfin item once so we can both build the deep-link
	// and look up the season-specific poster. Series-level webhook poster
	// is the fallback when Jellyfin hasn't scanned the new season yet.
	link := b.Cfg.JellyfinExternalURL
	if it, err := b.Jellyfin.FindByTMDB(ctx, tmdbID, "tv"); err == nil {
		if u := jellyfin.ItemWebURL(b.Cfg.JellyfinExternalURL, it.ID, it.ServerID); u != "" {
			link = u
		}
		if sp := b.Jellyfin.SeasonPosterURL(ctx, it.ID, season); sp != "" {
			poster = sp
		}
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("📺 *Serie:* %s — Staffel %d (%d %s)",
		seriesTitle, season, len(items), pluralEp(len(items))))
	if len(items) < 6 {
		lines = append(lines, "", strings.Join(episodes, "\n"))
	}
	lines = append(lines, "", "🍿 "+link)
	body = strings.Join(lines, "\n")
	return body, nil, ids, poster, tmdbID, seriesTitle, eps
}

func pluralEp(n int) string {
	if n == 1 {
		return "Episode"
	}
	return "Episoden"
}

// ─── requester @mention ───────────────────────────────────────────────────

// requesterMention finds the WhatsApp jid for the user who requested a
// given tmdb id. Returns ("@<phone>", "<phone>@c.us") for SendText's
// mentions slice, or ("","") when we can't resolve.
func (b *Bot) requesterMention(ctx context.Context, tmdbID int) (text string, jid string) {
	if tmdbID == 0 {
		return "", ""
	}
	r, err := b.Seerr.FindRequestByTMDB(ctx, tmdbID)
	if errors.Is(err, seerr.ErrNotFound) || err != nil {
		return "", ""
	}
	username := strings.ToLower(r.RequestedBy.JellyfinUserName)
	if username == "" {
		username = strings.ToLower(r.RequestedBy.Username)
	}
	phone := b.Cfg.PhoneMap[username]
	if phone == "" {
		return "", ""
	}
	return "@" + phone, waha.FormatJID(phone)
}

// pickPoster returns the first image URL of the requested coverType, or "".
func pickPoster(images []struct {
	RemoteURL string `json:"remoteUrl"`
	Type      string `json:"coverType"`
}, want string) string {
	for _, im := range images {
		if strings.EqualFold(im.Type, want) {
			return im.RemoteURL
		}
	}
	return ""
}
