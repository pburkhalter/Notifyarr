// Package waha is a thin HTTP client + webhook receiver for WAHA
// (https://waha.devlike.pro), the self-hosted WhatsApp HTTP API. We target
// the NOWEB engine because that's what the homelab deploy uses; some
// endpoints (polls in particular) only exist there.
package waha

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	BaseURL string
	APIKey  string
	Session string
	HTTP    *http.Client
}

func NewClient(baseURL, apiKey, session string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if session == "" {
		session = "default"
	}
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		Session: session,
		HTTP:    &http.Client{Timeout: timeout},
	}
}

type APIError struct {
	Status int
	Detail string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("waha %d: %s", e.Status, e.Detail)
}

// Retryable mirrors arrarr's torbox.APIError.Retryable so callers can use a
// single is-retryable check across upstream HTTP errors.
func (e *APIError) Retryable() bool {
	return e.Status == 429 || e.Status >= 500
}

// rawSentMessage is the union-shape decoder for the response id field. WAHA
// has flipped this representation between releases.
type rawSentMessage struct {
	ID json.RawMessage `json:"id"`
}

func extractMessageID(raw []byte) (string, error) {
	var r rawSentMessage
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", err
	}
	if len(r.ID) == 0 {
		return "", nil
	}
	// flat string form
	var s string
	if err := json.Unmarshal(r.ID, &s); err == nil {
		return s, nil
	}
	// nested object form
	var obj struct {
		Serialized string `json:"_serialized"`
	}
	if err := json.Unmarshal(r.ID, &obj); err == nil {
		return obj.Serialized, nil
	}
	return "", nil
}

type sendTextReq struct {
	Session  string   `json:"session"`
	ChatID   string   `json:"chatId"`
	Text     string   `json:"text"`
	Mentions []string `json:"mentions,omitempty"`
}

func (c *Client) SendText(ctx context.Context, chatID, text string, mentions []string) (string, error) {
	body := sendTextReq{Session: c.Session, ChatID: chatID, Text: text, Mentions: mentions}
	return c.sendAndReturnID(ctx, "/api/sendText", body)
}

type sendImageFile struct {
	URL string `json:"url"`
}

type sendImageReq struct {
	Session  string        `json:"session"`
	ChatID   string        `json:"chatId"`
	File     sendImageFile `json:"file"`
	Caption  string        `json:"caption,omitempty"`
	Mentions []string      `json:"mentions,omitempty"`
}

func (c *Client) SendImage(ctx context.Context, chatID, imageURL, caption string, mentions []string) (string, error) {
	body := sendImageReq{
		Session:  c.Session,
		ChatID:   chatID,
		File:     sendImageFile{URL: imageURL},
		Caption:  caption,
		Mentions: mentions,
	}
	return c.sendAndReturnID(ctx, "/api/sendImage", body)
}

type pollBody struct {
	Name            string   `json:"name"`
	Options         []string `json:"options"`
	MultipleAnswers bool     `json:"multipleAnswers"`
}

type sendPollReq struct {
	Session string   `json:"session"`
	ChatID  string   `json:"chatId"`
	Poll    pollBody `json:"poll"`
}

func (c *Client) SendPoll(ctx context.Context, chatID, name string, options []string, multipleAnswers bool) (string, error) {
	// WhatsApp caps poll options at 12; reject early so callers don't have to
	// guess at WAHA's somewhat noisy 4xx for this case.
	if len(options) == 0 {
		return "", errors.New("waha: poll needs at least one option")
	}
	if len(options) > 12 {
		return "", fmt.Errorf("waha: poll has %d options, max 12", len(options))
	}
	body := sendPollReq{
		Session: c.Session,
		ChatID:  chatID,
		Poll:    pollBody{Name: name, Options: options, MultipleAnswers: multipleAnswers},
	}
	return c.sendAndReturnID(ctx, "/api/sendPoll", body)
}

type reactReq struct {
	Session   string `json:"session"`
	MessageID string `json:"messageId"`
	Reaction  string `json:"reaction"`
}

func (c *Client) React(ctx context.Context, messageID, emoji string) error {
	body := reactReq{Session: c.Session, MessageID: messageID, Reaction: emoji}
	req, err := c.newJSONRequest(ctx, http.MethodPut, "/api/reactions", body)
	if err != nil {
		return err
	}
	_, err = c.do(req)
	return err
}

type Group struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Participants []string `json:"participants"`
}

// rawGroup matches WAHA's wire shape: participants come back as objects with
// a nested id (sometimes string, sometimes {_serialized:"..."}). We flatten
// to a []string so callers don't carry WhatsApp-internal types around.
type rawGroup struct {
	ID           json.RawMessage   `json:"id"`
	Name         string            `json:"name"`
	Subject      string            `json:"subject"`
	Participants []json.RawMessage `json:"participants"`
}

func (c *Client) ListGroups(ctx context.Context) ([]Group, error) {
	path := "/api/" + c.Session + "/groups"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	c.auth(req)
	req.Header.Set("Accept", "application/json")
	raw, err := c.do(req)
	if err != nil {
		return nil, err
	}
	var rawGroups []rawGroup
	if err := json.Unmarshal(raw, &rawGroups); err != nil {
		return nil, fmt.Errorf("decode groups: %w", err)
	}
	out := make([]Group, 0, len(rawGroups))
	for _, g := range rawGroups {
		flat := Group{Name: g.Name}
		if flat.Name == "" {
			flat.Name = g.Subject
		}
		flat.ID = decodeJIDField(g.ID)
		for _, p := range g.Participants {
			if id := decodeParticipant(p); id != "" {
				flat.Participants = append(flat.Participants, id)
			}
		}
		out = append(out, flat)
	}
	return out, nil
}

// decodeParticipant accepts the wire shapes WAHA uses for group members:
// a bare jid string, an object whose `id` is a string, or an object whose
// `id` is a nested `{_serialized: "..."}` blob.
func decodeParticipant(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var obj struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && len(obj.ID) > 0 {
		return decodeJIDField(obj.ID)
	}
	return ""
}

// decodeJIDField handles WAHA's two id shapes: a bare string, or an object
// with _serialized / id fields.
func decodeJIDField(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var obj struct {
		Serialized string `json:"_serialized"`
		ID         string `json:"id"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		if obj.Serialized != "" {
			return obj.Serialized
		}
		return obj.ID
	}
	return ""
}

func (c *Client) sendAndReturnID(ctx context.Context, path string, body any) (string, error) {
	req, err := c.newJSONRequest(ctx, http.MethodPost, path, body)
	if err != nil {
		return "", err
	}
	raw, err := c.do(req)
	if err != nil {
		return "", err
	}
	id, err := extractMessageID(raw)
	if err != nil {
		return "", fmt.Errorf("decode message id: %w", err)
	}
	return id, nil
}

func (c *Client) newJSONRequest(ctx context.Context, method, path string, body any) (*http.Request, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	c.auth(req)
	return req, nil
}

// auth attaches the WAHA api key. Header is omitted when APIKey is empty so
// unauthenticated deployments (lab/dev) keep working.
func (c *Client) auth(req *http.Request) {
	if c.APIKey != "" {
		req.Header.Set("X-Api-Key", c.APIKey)
	}
}

func (c *Client) do(req *http.Request) ([]byte, error) {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, &APIError{Status: resp.StatusCode, Detail: truncate(string(raw), 200)}
	}
	return raw, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
