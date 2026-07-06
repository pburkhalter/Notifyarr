// Package journarr posts a "notification.sent" callback to the Journarr flow
// tracker after the concierge delivers a WhatsApp notification, so Journarr
// can mark the media items 'notified'. Fire-and-forget: failure is logged and
// dropped, never blocking or failing the notification path.
package journarr

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

type Episode struct {
	Season  int `json:"season"`
	Episode int `json:"episode"`
}

type Client struct {
	url   string
	token string
	log   *slog.Logger
	http  *http.Client
}

// New returns nil when url is empty (integration disabled).
func New(url, token string, log *slog.Logger) *Client {
	if url == "" {
		return nil
	}
	return &Client{url: url, token: token, log: log, http: &http.Client{Timeout: 5 * time.Second}}
}

type payload struct {
	Event     string    `json:"event"`
	MediaType string    `json:"media_type"`
	TmdbID    int       `json:"tmdb_id"`
	Title     string    `json:"title,omitempty"`
	Episodes  []Episode `json:"episodes,omitempty"`
}

// NotifyMovie reports a delivered movie notification (non-blocking).
func (c *Client) NotifyMovie(tmdbID int, title string) {
	if c == nil || tmdbID == 0 {
		return
	}
	c.send(payload{Event: "notification.sent", MediaType: "movie", TmdbID: tmdbID, Title: title})
}

// NotifyEpisodes reports a delivered series-batch notification (non-blocking).
func (c *Client) NotifyEpisodes(tmdbID int, title string, eps []Episode) {
	if c == nil || tmdbID == 0 || len(eps) == 0 {
		return
	}
	c.send(payload{Event: "notification.sent", MediaType: "tv", TmdbID: tmdbID, Title: title, Episodes: eps})
}

func (c *Client) send(p payload) {
	body, err := json.Marshal(p)
	if err != nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if c.token != "" {
			req.Header.Set("X-Webhook-Token", c.token)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			c.log.Debug("journarr callback failed", "err", err)
			return
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 400 {
			c.log.Debug("journarr callback rejected", "status", resp.StatusCode)
		}
	}()
}
