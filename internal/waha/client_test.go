package waha

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSendTextWithAuthHeader(t *testing.T) {
	var captured struct {
		auth   string
		ct     string
		method string
		path   string
		body   map[string]any
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.auth = r.Header.Get("X-Api-Key")
		captured.ct = r.Header.Get("Content-Type")
		captured.method = r.Method
		captured.path = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &captured.body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"true_4179@c.us_3EB0ABC"}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "secret", "default", 5*time.Second)
	id, err := c.SendText(context.Background(), "4179@c.us", "hi @4179", []string{"4179@c.us"})
	if err != nil {
		t.Fatal(err)
	}
	if id != "true_4179@c.us_3EB0ABC" {
		t.Errorf("id=%q", id)
	}
	if captured.auth != "secret" {
		t.Errorf("X-Api-Key=%q want %q", captured.auth, "secret")
	}
	if captured.ct != "application/json" {
		t.Errorf("ct=%q", captured.ct)
	}
	if captured.method != http.MethodPost {
		t.Errorf("method=%q", captured.method)
	}
	if captured.path != "/api/sendText" {
		t.Errorf("path=%q", captured.path)
	}
	if captured.body["session"] != "default" {
		t.Errorf("session=%v", captured.body["session"])
	}
	if captured.body["chatId"] != "4179@c.us" {
		t.Errorf("chatId=%v", captured.body["chatId"])
	}
	mentions, ok := captured.body["mentions"].([]any)
	if !ok || len(mentions) != 1 || mentions[0] != "4179@c.us" {
		t.Errorf("mentions=%v", captured.body["mentions"])
	}
}

func TestSendTextWithoutAPIKeyOmitsHeader(t *testing.T) {
	var auth string
	var hasHeader bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("X-Api-Key")
		_, hasHeader = r.Header["X-Api-Key"]
		_, _ = io.WriteString(w, `{"id":"m1"}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "default", 5*time.Second)
	if _, err := c.SendText(context.Background(), "4179@c.us", "hi", nil); err != nil {
		t.Fatal(err)
	}
	if hasHeader || auth != "" {
		t.Errorf("X-Api-Key must be absent when APIKey empty (present=%v value=%q)", hasHeader, auth)
	}
}

func TestSendTextOmitsMentionsWhenNil(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = io.WriteString(w, `{"id":"m1"}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "k", "default", 5*time.Second)
	if _, err := c.SendText(context.Background(), "g@g.us", "hello", nil); err != nil {
		t.Fatal(err)
	}
	if _, present := body["mentions"]; present {
		t.Errorf("mentions must be omitted when nil; body=%v", body)
	}
}

func TestSendImagePayloadShape(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sendImage" {
			t.Errorf("path=%q", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = io.WriteString(w, `{"id":"m2"}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "k", "default", 5*time.Second)
	id, err := c.SendImage(context.Background(), "g@g.us", "https://posters/x.jpg", "caption", nil)
	if err != nil {
		t.Fatal(err)
	}
	if id != "m2" {
		t.Errorf("id=%q", id)
	}
	file, ok := body["file"].(map[string]any)
	if !ok {
		t.Fatalf("file not object: %v", body["file"])
	}
	if file["url"] != "https://posters/x.jpg" {
		t.Errorf("file.url=%v", file["url"])
	}
	if body["caption"] != "caption" {
		t.Errorf("caption=%v", body["caption"])
	}
}

func TestSendPollWraps(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sendPoll" {
			t.Errorf("path=%q", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = io.WriteString(w, `{"id":{"_serialized":"poll_xyz"}}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "k", "default", 5*time.Second)
	id, err := c.SendPoll(context.Background(), "g@g.us", "Pick one", []string{"A", "B", "C"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if id != "poll_xyz" {
		t.Errorf("id=%q want poll_xyz (nested-object id form)", id)
	}
	poll, ok := body["poll"].(map[string]any)
	if !ok {
		t.Fatalf("poll not object: %v", body["poll"])
	}
	if poll["name"] != "Pick one" {
		t.Errorf("poll.name=%v", poll["name"])
	}
	opts, ok := poll["options"].([]any)
	if !ok || len(opts) != 3 || opts[0] != "A" {
		t.Errorf("poll.options=%v", poll["options"])
	}
	if poll["multipleAnswers"] != true {
		t.Errorf("poll.multipleAnswers=%v", poll["multipleAnswers"])
	}
}

func TestSendPollRejectsEmptyAndTooMany(t *testing.T) {
	c := NewClient("http://unused", "k", "default", time.Second)
	if _, err := c.SendPoll(context.Background(), "g@g.us", "n", nil, false); err == nil {
		t.Error("expected error on empty options")
	}
	tooMany := make([]string, 13)
	for i := range tooMany {
		tooMany[i] = "x"
	}
	if _, err := c.SendPoll(context.Background(), "g@g.us", "n", tooMany, false); err == nil {
		t.Error("expected error on >12 options")
	}
}

func TestReactSendsPayload(t *testing.T) {
	var body map[string]any
	var method string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "k", "default", 5*time.Second)
	if err := c.React(context.Background(), "true_4179_xyz", "👍"); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPut {
		t.Errorf("method=%q want PUT", method)
	}
	if body["messageId"] != "true_4179_xyz" || body["reaction"] != "👍" {
		t.Errorf("body=%v", body)
	}
}

func TestListGroupsFlattensParticipants(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/groups") {
			t.Errorf("path=%q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[
			{
				"id": {"_serialized": "120363@g.us"},
				"name": "Streaming",
				"participants": [
					{"id": {"_serialized": "4179111@c.us"}},
					"4179222@c.us"
				]
			}
		]`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "k", "default", 5*time.Second)
	groups, err := c.ListGroups(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("groups=%d", len(groups))
	}
	g := groups[0]
	if g.ID != "120363@g.us" {
		t.Errorf("id=%q", g.ID)
	}
	if g.Name != "Streaming" {
		t.Errorf("name=%q", g.Name)
	}
	if len(g.Participants) != 2 || g.Participants[0] != "4179111@c.us" || g.Participants[1] != "4179222@c.us" {
		t.Errorf("participants=%v", g.Participants)
	}
}

func TestNon2xxReturnsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"message":"chatId not found"}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "k", "default", 5*time.Second)
	_, err := c.SendText(context.Background(), "bad@c.us", "x", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *APIError, got %T (%v)", err, err)
	}
	if apiErr.Status != http.StatusUnprocessableEntity {
		t.Errorf("status=%d", apiErr.Status)
	}
	if !strings.Contains(apiErr.Detail, "chatId not found") {
		t.Errorf("detail=%q", apiErr.Detail)
	}
	if apiErr.Retryable() {
		t.Error("422 must not be retryable")
	}
}

func TestErrorBodyTruncatedTo200Chars(t *testing.T) {
	long := strings.Repeat("a", 500)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, long)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "k", "default", 5*time.Second)
	_, err := c.SendText(context.Background(), "x", "y", nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want APIError, got %T", err)
	}
	// 200 chars + ellipsis rune
	if !strings.HasSuffix(apiErr.Detail, "…") {
		t.Errorf("detail should be truncated with ellipsis, got %q", apiErr.Detail)
	}
}
