// Package sonarr is a thin Sonarr v3 client. The bot uses it read-only for
// "what's downloading" and "what landed recently" formatting.
package sonarr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

func NewClient(baseURL, apiKey string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		HTTP:    &http.Client{Timeout: timeout},
	}
}

type APIError struct {
	Status int
	Detail string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("sonarr %d: %s", e.Status, e.Detail)
}

type QueueItem struct {
	ID                   int    `json:"id"`
	SeriesID             int    `json:"seriesId"`
	EpisodeID            int    `json:"episodeId"`
	Title                string `json:"title"`
	Status               string `json:"status"`
	TrackedDownloadState string `json:"trackedDownloadState"`
	Size                 int64  `json:"size"`
	SizeLeft             int64  `json:"sizeleft"`
	Timeleft             string `json:"timeleft"`
	Protocol             string `json:"protocol"`
	DownloadClient       string `json:"downloadClient"`
}

// queueEnvelope is Sonarr's paginated wrapper around the queue records.
type queueEnvelope struct {
	Records []QueueItem `json:"records"`
}

func (c *Client) Queue(ctx context.Context) ([]QueueItem, error) {
	q := url.Values{}
	q.Set("pageSize", "50")
	q.Set("includeMovie", "true")
	q.Set("includeSeries", "true")
	var env queueEnvelope
	if err := c.get(ctx, "/queue?"+q.Encode(), &env); err != nil {
		return nil, err
	}
	return env.Records, nil
}

type HistoryItem struct {
	ID          int       `json:"id"`
	SeriesID    int       `json:"seriesId"`
	EpisodeID   int       `json:"episodeId"`
	Date        time.Time `json:"date"`
	EventType   string    `json:"eventType"`
	SourceTitle string    `json:"sourceTitle"`
	Series      struct {
		Title    string `json:"title"`
		TvdbID   int    `json:"tvdbId"`
		TmdbID   int    `json:"tmdbId"`
		ImageURL string `json:"-"`
	} `json:"series"`
	Episode struct {
		Title         string `json:"title"`
		SeasonNumber  int    `json:"seasonNumber"`
		EpisodeNumber int    `json:"episodeNumber"`
	} `json:"episode"`
}

type historyEnvelope struct {
	Records []HistoryItem `json:"records"`
}

func (c *Client) RecentImports(ctx context.Context, take int) ([]HistoryItem, error) {
	if take <= 0 {
		take = 20
	}
	q := url.Values{}
	q.Set("pageSize", strconv.Itoa(take))
	q.Set("sortKey", "date")
	q.Set("sortDirection", "descending")
	q.Set("eventType", "3")
	var env historyEnvelope
	if err := c.get(ctx, "/history?"+q.Encode(), &env); err != nil {
		return nil, err
	}
	// Sonarr serves cover art under /MediaCover/<seriesId>/poster.jpg behind the
	// same instance — compute it once here so callers don't repeat the join.
	for i := range env.Records {
		env.Records[i].Series.ImageURL = fmt.Sprintf("%s/MediaCover/%d/poster.jpg", c.BaseURL, env.Records[i].SeriesID)
	}
	return env.Records, nil
}

// SearchMissing kicks off a search for every monitored-but-missing episode.
// Sonarr runs the command asynchronously; this returns once it's queued.
func (c *Client) SearchMissing(ctx context.Context) error {
	body := strings.NewReader(`{"name":"MissingEpisodeSearch"}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/v3/command", body)
	if err != nil {
		return err
	}
	c.auth(req)
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, nil)
}

func (c *Client) auth(req *http.Request) {
	req.Header.Set("X-Api-Key", c.APIKey)
	req.Header.Set("Accept", "application/json")
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/v3"+path, nil)
	if err != nil {
		return err
	}
	c.auth(req)
	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return &APIError{Status: resp.StatusCode, Detail: truncate(string(raw), 200)}
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode: %w (body=%s)", err, truncate(string(raw), 200))
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ─── Qualität einer importierten Folge ────────────────────────────────────

// FileQuality beschreibt die importierte Episodendatei — genug, um daraus eine
// laienverständliche Qualitätsstufe abzuleiten.
type FileQuality struct {
	QualityName   string
	CustomFormats []string
	Languages     []string
}

type episodeFile struct {
	Quality struct {
		Quality struct {
			Name string `json:"name"`
		} `json:"quality"`
	} `json:"quality"`
	CustomFormats []struct {
		Name string `json:"name"`
	} `json:"customFormats"`
	Languages []struct {
		Name string `json:"name"`
	} `json:"languages"`
}

type episode struct {
	SeasonNumber  int         `json:"seasonNumber"`
	EpisodeNumber int         `json:"episodeNumber"`
	HasFile       bool        `json:"hasFile"`
	EpisodeFile   episodeFile `json:"episodeFile"`
}

type series struct {
	ID     int `json:"id"`
	TmdbID int `json:"tmdbId"`
}

// QualityByTMDB liefert die Qualität einer konkreten Folge zu einer TMDB-Id.
// Sonarr kennt keinen tmdbId-Filter auf /series, deshalb wird clientseitig
// gesucht — bei der Grössenordnung einer Heim-Bibliothek unkritisch.
func (c *Client) QualityByTMDB(ctx context.Context, tmdbID, season, epNum int) (*FileQuality, error) {
	var all []series
	if err := c.get(ctx, "/series", &all); err != nil {
		return nil, err
	}
	seriesID := 0
	for _, s := range all {
		if s.TmdbID == tmdbID {
			seriesID = s.ID
			break
		}
	}
	if seriesID == 0 {
		return nil, nil
	}
	var eps []episode
	if err := c.get(ctx, fmt.Sprintf("/episode?seriesId=%d&seasonNumber=%d&includeEpisodeFile=true", seriesID, season), &eps); err != nil {
		return nil, err
	}
	for _, e := range eps {
		if e.EpisodeNumber != epNum || !e.HasFile {
			continue
		}
		q := &FileQuality{QualityName: e.EpisodeFile.Quality.Quality.Name}
		for _, cf := range e.EpisodeFile.CustomFormats {
			q.CustomFormats = append(q.CustomFormats, cf.Name)
		}
		for _, l := range e.EpisodeFile.Languages {
			q.Languages = append(q.Languages, l.Name)
		}
		return q, nil
	}
	return nil, nil
}
