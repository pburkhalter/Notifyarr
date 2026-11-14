package jellyfin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, "k", "u1", time.Second)
}

func TestRecentlyAdded(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/Users/u1/Items") {
			t.Fatalf("path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"Items":[{"Id":"abc","Name":"Pilot","Type":"Episode","SeriesName":"Show","ProductionYear":2024}]}`))
	})
	got, err := c.RecentlyAdded(context.Background(), 3)
	if err != nil || len(got) != 1 || got[0].SeriesName != "Show" || !strings.Contains(got[0].PosterURL, "/Items/abc/Images/Primary") {
		t.Fatalf("recent: %v %#v", err, got)
	}
}

func TestCounts(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Items/Counts" {
			t.Fatalf("path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"MovieCount":10,"SeriesCount":5,"EpisodeCount":120}`))
	})
	got, err := c.Counts(context.Background())
	if err != nil || got.Movie != 10 || got.Series != 5 || got.Episode != 120 {
		t.Fatalf("counts: %v %#v", err, got)
	}
}

func TestPosterURL(t *testing.T) {
	c := NewClient("http://j", "secret", "u", 0)
	if got, want := c.PosterURL("x/y"), "http://j/Items/x%2Fy/Images/Primary?maxWidth=500"; got != want {
		t.Errorf("PosterURL = %q, want %q", got, want)
	}
}

// TestRequestsCarryKeyInAuthorizationHeader runs every method that talks to
// the server. A method added later belongs in this list, so that it cannot
// ship without credentials or fall back to a form Jellyfin 12 rejects.
func TestRequestsCarryKeyInAuthorizationHeader(t *testing.T) {
	const key = "0123456789abcdef0123456789abcdef"
	calls := []struct {
		name string
		call func(context.Context, *Client) error
	}{
		{"RecentlyAdded", func(ctx context.Context, c *Client) error {
			_, err := c.RecentlyAdded(ctx, 3)
			return err
		}},
		{"Counts", func(ctx context.Context, c *Client) error {
			_, err := c.Counts(ctx)
			return err
		}},
		{"CountLibrary", func(ctx context.Context, c *Client) error {
			_, err := c.CountLibrary(ctx, "lib", "Movie")
			return err
		}},
		{"FindByTMDB", func(ctx context.Context, c *Client) error {
			_, err := c.FindByTMDB(ctx, 603, "movie")
			return err
		}},
		{"SeasonPosterURL", func(ctx context.Context, c *Client) error {
			if c.SeasonPosterURL(ctx, "series", 1) == "" {
				return errors.New("no season poster")
			}
			return nil
		}},
	}
	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mu     sync.Mutex
				header http.Header
				query  url.Values
				hits   int
			)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				header, query, hits = r.Header.Clone(), r.URL.Query(), hits+1
				mu.Unlock()
				_, _ = w.Write([]byte(`{"Items":[{"Id":"abc","Type":"Movie","IndexNumber":1,` +
					`"ProviderIds":{"Tmdb":"603"},"ImageTags":{"Primary":"tag"}}]}`))
			}))
			t.Cleanup(srv.Close)
			c := NewClient(srv.URL, key, "u1", time.Second)

			if err := tc.call(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if hits != 1 {
				t.Fatalf("got %d requests, want 1", hits)
			}
			if got, want := header.Get("Authorization"), `MediaBrowser Token="`+key+`"`; got != want {
				t.Errorf("Authorization = %q, want %q", got, want)
			}
			if got := header.Get("X-Emby-Token"); got != "" {
				t.Errorf("X-Emby-Token = %q, want it unset", got)
			}
			for _, param := range []string{"api_key", "ApiKey"} {
				if query.Has(param) {
					t.Errorf("query parameter %s is set, want the key in the header only", param)
				}
			}
		})
	}
}

func TestAPIError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`denied`))
	})
	_, err := c.Counts(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 403 {
		t.Fatalf("want APIError 403, got %v", err)
	}
}
