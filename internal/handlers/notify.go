package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pburkhalter/waha-concierge/internal/jellyfin"
	"github.com/pburkhalter/waha-concierge/internal/journarr"
	"github.com/pburkhalter/waha-concierge/internal/seerr"
	"github.com/pburkhalter/waha-concierge/internal/store"
	"github.com/pburkhalter/waha-concierge/internal/waha"
)

// Direct Sonarr/Radarr "Connect → Webhook" handling was removed: Journarr now
// owns completion notifications (NOTIFY_MODE=journarr) via POST /notify/send.
// The shared rendering helpers below (jellyfinLink, requesterMention, the flush
// batching) remain — they serve /notify/send and the pending-import flush.

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
