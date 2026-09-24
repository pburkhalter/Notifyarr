package handlers

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pburkhalter/notifyarr/internal/config"
	"github.com/pburkhalter/notifyarr/internal/jellyfin"
	"github.com/pburkhalter/notifyarr/internal/radarr"
	"github.com/pburkhalter/notifyarr/internal/seerr"
	"github.com/pburkhalter/notifyarr/internal/sonarr"
	"github.com/pburkhalter/notifyarr/internal/store"
	"github.com/pburkhalter/notifyarr/internal/waha"
)

// fakeUpstream answers WAHA sends with a message id and every other API with
// 404, which the renderer tolerates (no quality line, bare library link).
type fakeUpstream struct {
	sends atomic.Int64
	srv   *httptest.Server
}

func newFakeUpstream(t *testing.T) *fakeUpstream {
	t.Helper()
	f := &fakeUpstream{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/send") {
			f.sends.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "msg-1"})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func testBot(t *testing.T, up *fakeUpstream, extraEnv ...string) *Bot {
	t.Helper()
	env := []string{
		"WAHA_URL=" + up.srv.URL, "WAHA_CHAT_ID=120363@g.us", "WAHA_BOT_PHONE=41791112233",
		"SEERR_URL=" + up.srv.URL, "SEERR_API_KEY=k",
		"SONARR_URL=" + up.srv.URL, "SONARR_API_KEY=k",
		"RADARR_URL=" + up.srv.URL, "RADARR_API_KEY=k",
		"JELLYFIN_URL=" + up.srv.URL, "JELLYFIN_API_KEY=k", "JELLYFIN_USER_ID=u",
		"JELLYFIN_EXTERNAL_URL=https://jf.test", "SEERR_EXTERNAL_URL=https://seerr.test",
		"PHONE_MAP_PATRIK=41790000000",
		"NOTIFY_MODE=journarr", "NOTIFY_SEND_TOKEN=tok",
	}
	env = append(env, extraEnv...)
	cfg, err := config.Load(env)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	to := 2 * time.Second
	return New(cfg, log,
		waha.NewClient(up.srv.URL, "", "default", to),
		seerr.NewClient(up.srv.URL, "k", to),
		sonarr.NewClient(up.srv.URL, "k", to),
		radarr.NewClient(up.srv.URL, "k", to),
		jellyfin.NewClient(up.srv.URL, "k", "u", to),
		st)
}

// Commands are only taken from the configured group and from household
// phones; anyone else writing to the bot number is ignored without a reply.
func TestOnMessageIgnoresUnknownChats(t *testing.T) {
	up := newFakeUpstream(t)
	b := testBot(t, up)
	ctx := context.Background()

	cases := []struct {
		from  string
		sends int64
	}{
		{"999999@g.us", 0},      // foreign group
		{"41770000000@c.us", 0}, // stranger in a direct chat
		{"41790000000@c.us", 1}, // household phone from PHONE_MAP
		{"120363@g.us", 2},      // the configured group
	}
	for _, c := range cases {
		if err := b.OnMessage(ctx, waha.MessageEvent{ID: "x", From: c.from, Body: "@41791112233 help"}); err != nil {
			t.Fatalf("OnMessage(%s): %v", c.from, err)
		}
		if got := up.sends.Load(); got != c.sends {
			t.Fatalf("after %s: %d sends, want %d", c.from, got, c.sends)
		}
	}
}

// A retried /notify/send with the same idempotency key must not reach
// WhatsApp twice.
func TestNotifySendIsIdempotent(t *testing.T) {
	up := newFakeUpstream(t)
	b := testBot(t, up)
	h := b.NotifyHandler()

	post := func(key string) (int, map[string]any) {
		req := httptest.NewRequest(http.MethodPost, "/notify/send",
			strings.NewReader(`{"media_type":"movie","tmdb_id":5,"title":"Film","year":2026}`))
		req.Header.Set("X-Notify-Token", "tok")
		if key != "" {
			req.Header.Set("X-Idempotency-Key", key)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body
	}

	if code, body := post("notify:req:1:abc"); code != 200 || body["message_id"] != "msg-1" {
		t.Fatalf("first send: %d %v", code, body)
	}
	if code, body := post("notify:req:1:abc"); code != 200 || body["message_id"] != "msg-1" || body["replayed"] != true {
		t.Fatalf("retry: %d %v, want a replay of msg-1", code, body)
	}
	if got := up.sends.Load(); got != 1 {
		t.Fatalf("WhatsApp sends = %d after a retry, want 1", got)
	}
	if code, _ := post("notify:req:1:def"); code != 200 {
		t.Fatalf("new key: %d", code)
	}
	if got := up.sends.Load(); got != 2 {
		t.Fatalf("WhatsApp sends = %d after a new key, want 2", got)
	}
	if code, _ := post(""); code != 200 || up.sends.Load() != 3 {
		t.Fatalf("without a key every call sends: %d sends", up.sends.Load())
	}
}
