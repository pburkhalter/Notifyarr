package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"time"
)

// streamingStatus is the payload of /streaming-status.json — the single source
// the dashboard reads for the error/issues panel and SceneNZB quota headroom.
// Every section is optional so the endpoint degrades (never 500s) when the
// healthcheck file or Prowlarr is unavailable.
type streamingStatus struct {
	GeneratedAt time.Time       `json:"generated_at"`
	Version     string          `json:"version,omitempty"`
	OK          *bool           `json:"ok,omitempty"`
	IssueCount  int             `json:"issue_count"`
	Issues      []string        `json:"issues"`
	Metrics     json.RawMessage `json:"metrics,omitempty"`
	SceneNZB    *quotaHeadroom  `json:"scenenzb,omitempty"`
	Jellyfin    *jellyfinCounts `json:"jellyfin,omitempty"`
}

// jellyfinCounts is per-library (Filme + Serien VirtualFolders), so the
// dashboard can show movies and series on one card without the orphan
// inflation of Jellyfin's global /Items/Counts.
type jellyfinCounts struct {
	Movies   int `json:"movies"`
	Series   int `json:"series"`
	Episodes int `json:"episodes"`
}

type quotaHeadroom struct {
	Indexer   string `json:"indexer"`
	Used      int    `json:"used"`
	Cap       int    `json:"cap"`
	Left      int    `json:"left"`
	Available *bool  `json:"available,omitempty"`
}

// healthDoc mirrors the fields the NAS healthcheck script writes to
// HEALTH_STATUS_FILE. Kept in sync with scripts/healthchecks.sh write_status_json.
type healthDoc struct {
	OK         bool            `json:"ok"`
	IssueCount int             `json:"issue_count"`
	Issues     []string        `json:"issues"`
	Metrics    json.RawMessage `json:"metrics"`
}

func (b *Bot) StreamingStatusHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out := streamingStatus{GeneratedAt: time.Now().UTC(), Version: b.Version, Issues: []string{}}

		// Healthcheck issues (optional — file is mounted read-only).
		if path := b.Cfg.HealthStatusFile; path != "" {
			if raw, err := os.ReadFile(path); err != nil {
				b.Log.Warn("streaming-status: health file unreadable", "path", path, "err", err)
			} else {
				var hd healthDoc
				if err := json.Unmarshal(raw, &hd); err != nil {
					b.Log.Warn("streaming-status: health file parse failed", "err", err)
				} else {
					ok := hd.OK
					out.OK = &ok
					out.IssueCount = hd.IssueCount
					if hd.Issues != nil {
						out.Issues = hd.Issues
					}
					out.Metrics = hd.Metrics
				}
			}
		}

		// SceneNZB grab-quota headroom + availability (optional — needs Prowlarr).
		if b.Prowlarr != nil {
			ctx, cancel := context.WithTimeout(r.Context(), b.Cfg.HTTPTimeout)
			defer cancel()
			indexer := b.Cfg.ProwlarrQuotaIndexer
			dailyCap := b.Cfg.QuotaIndexerDailyCap
			hr := &quotaHeadroom{Indexer: indexer, Cap: dailyCap}
			populated := false

			midnight := time.Now().UTC().Truncate(24 * time.Hour)
			if used, found, err := b.Prowlarr.GrabsSince(ctx, indexer, midnight); err != nil {
				b.Log.Warn("streaming-status: prowlarr stats failed", "err", err)
			} else if found {
				left := dailyCap - used
				if left < 0 {
					left = 0
				}
				hr.Used, hr.Left = used, left
				populated = true
			}

			if avail, found, err := b.Prowlarr.IndexerAvailable(ctx, indexer); err != nil {
				b.Log.Warn("streaming-status: prowlarr indexer status failed", "err", err)
			} else if found {
				a := avail
				hr.Available = &a
				populated = true
			}

			if populated {
				out.SceneNZB = hr
			}
		}

		// Jellyfin per-library counts (optional — needs JELLYFIN_*_LIBRARY_ID).
		if b.Jellyfin != nil {
			ctx, cancel := context.WithTimeout(r.Context(), b.Cfg.HTTPTimeout)
			defer cancel()
			if movies, series, episodes, ok := b.libraryCounts(ctx); ok {
				out.Jellyfin = &jellyfinCounts{Movies: movies, Series: series, Episodes: episodes}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(out)
	})
}
