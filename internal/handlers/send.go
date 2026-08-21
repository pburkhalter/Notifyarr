package handlers

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// notifyRequest is the contract Journarr POSTs to /notify/send when it owns
// completion notifications (NOTIFY_MODE=journarr). Notifyarr stays the renderer:
// it resolves the requester @mention and the Jellyfin deep link from tmdb_id.
type notifyRequest struct {
	MediaType string `json:"media_type"` // tv|movie
	TmdbID    int    `json:"tmdb_id"`
	Title     string `json:"title"`
	Year      int    `json:"year"`
	Episodes  []struct {
		Season  int    `json:"season"`
		Episode int    `json:"episode"`
		Title   string `json:"title"`
	} `json:"episodes"`
	PosterURL string `json:"poster_url"`
}

// NotifyHandler serves POST /notify/send, token-guarded by NOTIFY_SEND_TOKEN.
func (b *Bot) NotifyHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b.Cfg.NotifySendToken == "" ||
			subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Notify-Token")), []byte(b.Cfg.NotifySendToken)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var n notifyRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&n); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		msgID, err := b.sendNotification(ctx, n)
		if err != nil {
			b.Log.Warn("notify/send failed", "err", err, "tmdb", n.TmdbID)
			http.Error(w, "send failed", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"message_id": msgID})
	})
}

func (b *Bot) sendNotification(ctx context.Context, n notifyRequest) (string, error) {
	mentionText, jid := b.requesterMention(ctx, n.TmdbID)
	var mentions []string
	if jid != "" {
		mentions = []string{jid}
	}
	link := b.jellyfinLink(ctx, n.TmdbID, n.MediaType)

	// Qualität der importierten Datei — bei mehreren Folgen stellvertretend die
	// erste, sie stammen aus demselben Grab und teilen die Qualität.
	var q qualityInfo
	if n.MediaType == "tv" && len(n.Episodes) > 0 {
		q = b.lookupQuality(ctx, n.MediaType, n.TmdbID, n.Episodes[0].Season, n.Episodes[0].Episode)
	} else {
		q = b.lookupQuality(ctx, n.MediaType, n.TmdbID, 0, 0)
	}

	var body strings.Builder
	if n.MediaType == "tv" && len(n.Episodes) > 0 {
		if len(n.Episodes) == 1 {
			e := n.Episodes[0]
			t := ""
			if e.Title != "" {
				t = " — „" + e.Title + "“"
			}
			fmt.Fprintf(&body, "📺 *%s* · S%02dE%02d%s", n.Title, e.Season, e.Episode, t)
		} else {
			fmt.Fprintf(&body, "📺 *%s* — %d neue Folgen", n.Title, len(n.Episodes))
			for _, e := range n.Episodes {
				t := ""
				if e.Title != "" {
					t = " — " + e.Title
				}
				fmt.Fprintf(&body, "\n• S%02dE%02d%s", e.Season, e.Episode, t)
			}
		}
	} else {
		title := n.Title
		if n.Year > 0 {
			title = fmt.Sprintf("%s (%d)", title, n.Year)
		}
		fmt.Fprintf(&body, "🎬 *%s* ist da", title)
	}
	if q.Available {
		fmt.Fprintf(&body, "\nQualität: *%s*", q.Tier)
		if q.Detail != "" {
			fmt.Fprintf(&body, " · %s", q.Detail)
		}
		if q.Limited {
			body.WriteString("\nWird automatisch ersetzt, sobald eine bessere Fassung erscheint")
		}
	}
	if link != "" {
		body.WriteString("\n" + link)
	}
	if mentionText != "" {
		body.WriteString("\n" + mentionText)
	}
	text := body.String()

	if n.PosterURL != "" {
		if id, err := b.WAHA.SendImage(ctx, b.Cfg.WAHAChatID, n.PosterURL, text, mentions); err == nil {
			return id, nil
		}
		// image can 422 on NOWEB — fall through to text.
	}
	return b.WAHA.SendText(ctx, b.Cfg.WAHAChatID, text, mentions)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
