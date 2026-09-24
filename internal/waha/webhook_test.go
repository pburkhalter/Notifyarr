package waha

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type stubHandler struct {
	mu     sync.Mutex
	msgs   []MessageEvent
	joins  []GroupJoinEvent
	votes  []PollVoteEvent
	wgMsg  sync.WaitGroup
	wgJoin sync.WaitGroup
	wgVote sync.WaitGroup
}

func (s *stubHandler) OnMessage(_ context.Context, ev MessageEvent) error {
	s.mu.Lock()
	s.msgs = append(s.msgs, ev)
	s.mu.Unlock()
	s.wgMsg.Done()
	return nil
}

func (s *stubHandler) OnGroupJoin(_ context.Context, ev GroupJoinEvent) error {
	s.mu.Lock()
	s.joins = append(s.joins, ev)
	s.mu.Unlock()
	s.wgJoin.Done()
	return nil
}

func (s *stubHandler) OnPollVote(_ context.Context, ev PollVoteEvent) error {
	s.mu.Lock()
	s.votes = append(s.votes, ev)
	s.mu.Unlock()
	s.wgVote.Done()
	return nil
}

// waitOr fails the test if the WaitGroup doesn't drain within the timeout.
// The dispatch is async, so we can't read recorded events synchronously.
func waitOr(t *testing.T, wg *sync.WaitGroup, d time.Duration, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("timeout waiting for %s", what)
	}
}

func TestWebhookDispatchesMessage(t *testing.T) {
	h := &stubHandler{}
	h.wgMsg.Add(1)
	rcv := &Receiver{Handler: h, AllowInsecure: true}
	srv := httptest.NewServer(rcv.HTTPHandler())
	defer srv.Close()

	body := `{
		"event": "message",
		"session": "default",
		"payload": {
			"id": "true_g@g.us_3EB0",
			"from": "g@g.us",
			"fromMe": false,
			"body": "@bot search foo",
			"timestamp": 1700000000,
			"participant": "4179@c.us",
			"mentionedIds": ["12345@c.us"]
		}
	}`
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status=%d", resp.StatusCode)
	}
	waitOr(t, &h.wgMsg, 2*time.Second, "OnMessage")
	if len(h.msgs) != 1 {
		t.Fatalf("msgs=%d", len(h.msgs))
	}
	got := h.msgs[0]
	if got.ID != "true_g@g.us_3EB0" || got.From != "g@g.us" || got.Body != "@bot search foo" {
		t.Errorf("msg=%+v", got)
	}
	if got.Participant != "4179@c.us" {
		t.Errorf("participant=%q", got.Participant)
	}
	if len(got.MentionedIDs) != 1 || got.MentionedIDs[0] != "12345@c.us" {
		t.Errorf("mentionedIds=%v", got.MentionedIDs)
	}
}

func TestWebhookFiltersFromMe(t *testing.T) {
	h := &stubHandler{}
	rcv := &Receiver{Handler: h, AllowInsecure: true}
	srv := httptest.NewServer(rcv.HTTPHandler())
	defer srv.Close()

	body := `{"event":"message","session":"default","payload":{"id":"x","from":"g@g.us","fromMe":true,"body":"echo"}}`
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	// Give a brief moment for a (wrong) dispatch goroutine to fire.
	time.Sleep(50 * time.Millisecond)
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.msgs) != 0 {
		t.Errorf("fromMe=true must be filtered; got %d msgs", len(h.msgs))
	}
}

func TestWebhookDispatchesGroupJoin(t *testing.T) {
	h := &stubHandler{}
	h.wgJoin.Add(1)
	rcv := &Receiver{Handler: h, AllowInsecure: true}
	srv := httptest.NewServer(rcv.HTTPHandler())
	defer srv.Close()

	body := `{
		"event": "group.v2.join",
		"session": "default",
		"payload": {
			"id": "120363@g.us",
			"participants": [{"id":"4179111@c.us"}, {"id":"4179222@c.us"}],
			"by": "4179333@c.us",
			"type": "add"
		}
	}`
	resp, _ := http.Post(srv.URL, "application/json", strings.NewReader(body))
	resp.Body.Close()
	waitOr(t, &h.wgJoin, 2*time.Second, "OnGroupJoin")
	if len(h.joins) != 1 {
		t.Fatalf("joins=%d", len(h.joins))
	}
	g := h.joins[0]
	if g.ID != "120363@g.us" || g.Type != "add" || g.By != "4179333@c.us" {
		t.Errorf("join=%+v", g)
	}
	if len(g.Participants) != 2 || g.Participants[0].ID != "4179111@c.us" {
		t.Errorf("participants=%v", g.Participants)
	}
}

func TestWebhookDispatchesPollVote(t *testing.T) {
	h := &stubHandler{}
	h.wgVote.Add(1)
	rcv := &Receiver{Handler: h, AllowInsecure: true}
	srv := httptest.NewServer(rcv.HTTPHandler())
	defer srv.Close()

	body := `{
		"event": "poll.vote",
		"session": "default",
		"payload": {
			"pollId": "true_g@g.us_pollA",
			"voterId": "4179111@c.us",
			"selectedOptions": [0, 2]
		}
	}`
	resp, _ := http.Post(srv.URL, "application/json", strings.NewReader(body))
	resp.Body.Close()
	waitOr(t, &h.wgVote, 2*time.Second, "OnPollVote")
	if len(h.votes) != 1 {
		t.Fatalf("votes=%d", len(h.votes))
	}
	v := h.votes[0]
	if v.PollID != "true_g@g.us_pollA" || v.VoterID != "4179111@c.us" {
		t.Errorf("vote=%+v", v)
	}
	if len(v.SelectedOptions) != 2 || v.SelectedOptions[0] != 0 || v.SelectedOptions[1] != 2 {
		t.Errorf("selectedOptions=%v", v.SelectedOptions)
	}
}

func TestWebhookUnknownEventReturns200(t *testing.T) {
	h := &stubHandler{}
	rcv := &Receiver{Handler: h, AllowInsecure: true}
	srv := httptest.NewServer(rcv.HTTPHandler())
	defer srv.Close()

	body := `{"event":"never.heard.of.this","session":"default","payload":{}}`
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status=%d want 200 (unknown events must not retry-storm)", resp.StatusCode)
	}
	time.Sleep(20 * time.Millisecond)
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.msgs)+len(h.joins)+len(h.votes) != 0 {
		t.Errorf("no handler should fire for unknown event")
	}
}

func TestWebhookRejectsNonPOST(t *testing.T) {
	rcv := &Receiver{AllowInsecure: true}
	srv := httptest.NewServer(rcv.HTTPHandler())
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status=%d want 405", resp.StatusCode)
	}
}

func TestWebhookBadJSONReturns400(t *testing.T) {
	rcv := &Receiver{AllowInsecure: true}
	srv := httptest.NewServer(rcv.HTTPHandler())
	defer srv.Close()

	resp, _ := http.Post(srv.URL, "application/json", strings.NewReader("{not json"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status=%d want 400", resp.StatusCode)
	}
}
