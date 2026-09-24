package waha

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"hash"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Event struct {
	Event   string          `json:"event"`
	Session string          `json:"session"`
	Payload json.RawMessage `json:"payload"`
}

type MessageEvent struct {
	ID           string   `json:"id"`
	From         string   `json:"from"`
	FromMe       bool     `json:"fromMe"`
	Body         string   `json:"body"`
	Timestamp    int64    `json:"timestamp"`
	HasMedia     bool     `json:"hasMedia"`
	Participant  string   `json:"participant"`
	MentionedIDs []string `json:"mentionedIds"`
}

type GroupJoinParticipant struct {
	ID string `json:"id"`
}

type GroupJoinEvent struct {
	ID           string                 `json:"id"`
	Participants []GroupJoinParticipant `json:"participants"`
	By           string                 `json:"by"`
	Type         string                 `json:"type"`
}

type PollVoteEvent struct {
	PollID          string `json:"pollId"`
	VoterID         string `json:"voterId"`
	SelectedOptions []int  `json:"selectedOptions"`
}

// Handler is the routing surface for parsed WAHA events. Returning err is
// logged but does not fail the HTTP response — WAHA retries on non-2xx and
// would storm us during e.g. a poll dispatch outage.
type Handler interface {
	OnMessage(ctx context.Context, ev MessageEvent) error
	OnGroupJoin(ctx context.Context, ev GroupJoinEvent) error
	OnPollVote(ctx context.Context, ev PollVoteEvent) error
}

type Receiver struct {
	Handler Handler
	Logger  *slog.Logger

	// HMACKey is WAHA's WHATSAPP_HOOK_HMAC_KEY. Every POST must carry
	// X-Webhook-Hmac, the hex HMAC of the raw body (sha512 by default,
	// X-Webhook-Hmac-Algorithm may say sha256). Without a key the receiver
	// answers 503 — anything on the LAN could otherwise inject bot commands
	// that end up as Seerr requests.
	HMACKey string
	// AllowInsecure accepts unsigned posts when no key is configured. For
	// local experiments only.
	AllowInsecure bool

	// AsyncTimeout caps each dispatched goroutine's context. Zero uses 30s,
	// which is well above any WAHA retry interval but short enough that a
	// stuck handler can't pin memory forever.
	AsyncTimeout time.Duration

	seen recentIDs
}

// recentIDs remembers the last message ids so a message WAHA delivers twice
// (it fires both "message" and "message.any" for the same chat message) is
// handled once.
type recentIDs struct {
	mu    sync.Mutex
	order []string
	set   map[string]struct{}
}

const recentIDsCap = 1000

// seen reports whether id was already recorded, recording it otherwise.
func (r *recentIDs) seen(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.set == nil {
		r.set = map[string]struct{}{}
	}
	if _, ok := r.set[id]; ok {
		return true
	}
	r.set[id] = struct{}{}
	r.order = append(r.order, id)
	if len(r.order) > recentIDsCap {
		delete(r.set, r.order[0])
		r.order = r.order[1:]
	}
	return false
}

// verifyHMAC checks WAHA's signature over the raw body.
func verifyHMAC(key string, body []byte, sig, algo string) bool {
	var mac hash.Hash
	switch strings.ToLower(strings.TrimSpace(algo)) {
	case "", "sha512":
		mac = hmac.New(sha512.New, []byte(key))
	case "sha256":
		mac = hmac.New(sha256.New, []byte(key))
	default:
		return false
	}
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(strings.ToLower(strings.TrimSpace(sig))), []byte(want))
}

func (r *Receiver) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}

func (r *Receiver) timeout() time.Duration {
	if r.AsyncTimeout > 0 {
		return r.AsyncTimeout
	}
	return 30 * time.Second
}

// HTTPHandler returns an http.Handler that parses incoming WAHA POSTs and
// dispatches them. We always reply 200 OK before doing real work so WAHA
// doesn't enter a retry-storm if a downstream system (Sonarr/Radarr) lags.
//
// Named HTTPHandler (not Handler) to avoid collision with the Handler field.
func (r *Receiver) HTTPHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
		if err != nil {
			r.logger().Warn("waha webhook: read body", "err", err)
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		switch {
		case r.HMACKey != "":
			if !verifyHMAC(r.HMACKey, raw, req.Header.Get("X-Webhook-Hmac"), req.Header.Get("X-Webhook-Hmac-Algorithm")) {
				r.logger().Warn("waha webhook: bad or missing signature", "remote", req.RemoteAddr)
				http.Error(w, "invalid signature", http.StatusUnauthorized)
				return
			}
		case !r.AllowInsecure:
			r.logger().Warn("waha webhook: rejected, WAHA_WEBHOOK_HMAC_KEY not configured", "remote", req.RemoteAddr)
			http.Error(w, "webhook signing not configured", http.StatusServiceUnavailable)
			return
		}
		var ev Event
		if err := json.Unmarshal(raw, &ev); err != nil {
			r.logger().Warn("waha webhook: decode envelope", "err", err)
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)

		if r.Handler == nil {
			return
		}
		r.dispatch(ev)
	})
}

func (r *Receiver) dispatch(ev Event) {
	switch ev.Event {
	case "message", "message.any":
		var m MessageEvent
		if err := json.Unmarshal(ev.Payload, &m); err != nil {
			r.logger().Warn("waha webhook: decode message", "err", err)
			return
		}
		if m.FromMe {
			return
		}
		if m.ID != "" && r.seen.seen(m.ID) {
			r.logger().Debug("waha webhook: duplicate message event", "id", m.ID, "event", ev.Event)
			return
		}
		go r.run(ev.Event, func(ctx context.Context) error {
			return r.Handler.OnMessage(ctx, m)
		})

	case "group.v2.join", "group.join":
		var g GroupJoinEvent
		if err := json.Unmarshal(ev.Payload, &g); err != nil {
			r.logger().Warn("waha webhook: decode group join", "err", err)
			return
		}
		go r.run(ev.Event, func(ctx context.Context) error {
			return r.Handler.OnGroupJoin(ctx, g)
		})

	case "poll.vote":
		var p PollVoteEvent
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			r.logger().Warn("waha webhook: decode poll vote", "err", err)
			return
		}
		go r.run(ev.Event, func(ctx context.Context) error {
			return r.Handler.OnPollVote(ctx, p)
		})

	default:
		r.logger().Debug("waha webhook: unknown event", "event", ev.Event)
	}
}

func (r *Receiver) run(event string, fn func(context.Context) error) {
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout())
	defer cancel()
	if err := fn(ctx); err != nil {
		r.logger().Warn("waha webhook: handler error", "event", event, "err", err)
	}
}
