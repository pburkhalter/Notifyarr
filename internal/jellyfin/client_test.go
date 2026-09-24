package jellyfin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
	u := c.PosterURL("xyz")
	if !strings.Contains(u, "/Items/xyz/Images/Primary") || !strings.Contains(u, "api_key=secret") {
		t.Fatalf("poster url: %s", u)
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
