package waha

import (
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func sign(key, body string) string {
	mac := hmac.New(sha512.New, []byte(key))
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

func postSigned(t *testing.T, rcv *Receiver, body, sig string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/waha-webhook", strings.NewReader(body))
	if sig != "" {
		req.Header.Set("X-Webhook-Hmac", sig)
		req.Header.Set("X-Webhook-Hmac-Algorithm", "sha512")
	}
	rec := httptest.NewRecorder()
	rcv.HTTPHandler().ServeHTTP(rec, req)
	return rec
}

const msgBody = `{"event":"message","session":"default","payload":{"id":"m1","from":"1@g.us","body":"hi"}}`

// Anything on the LAN could post fake chat messages that the bot turns into
// Seerr requests. Without a configured key the receiver is off, not open.
func TestWebhookRejectsUnsignedWithoutKey(t *testing.T) {
	h := &stubHandler{}
	rcv := &Receiver{Handler: h}
	if rec := postSigned(t, rcv, msgBody, ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestWebhookVerifiesHMAC(t *testing.T) {
	h := &stubHandler{}
	rcv := &Receiver{Handler: h, HMACKey: "secret"}

	if rec := postSigned(t, rcv, msgBody, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing signature: status = %d, want 401", rec.Code)
	}
	if rec := postSigned(t, rcv, msgBody, sign("wrong", msgBody)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong key: status = %d, want 401", rec.Code)
	}
	h.wgMsg.Add(1)
	if rec := postSigned(t, rcv, msgBody, sign("secret", msgBody)); rec.Code != http.StatusOK {
		t.Fatalf("valid signature: status = %d, want 200", rec.Code)
	}
	waitOr(t, &h.wgMsg, time.Second, "message dispatch")
	if len(h.msgs) != 1 || h.msgs[0].ID != "m1" {
		t.Fatalf("dispatched %+v, want the signed message", h.msgs)
	}
}

// WAHA fires "message" and "message.any" for the same chat message; the bot
// must answer once.
func TestWebhookDedupesMessageAny(t *testing.T) {
	h := &stubHandler{}
	rcv := &Receiver{Handler: h, AllowInsecure: true}
	h.wgMsg.Add(1)
	postSigned(t, rcv, msgBody, "")
	postSigned(t, rcv, strings.Replace(msgBody, `"event":"message"`, `"event":"message.any"`, 1), "")
	waitOr(t, &h.wgMsg, time.Second, "message dispatch")
	time.Sleep(50 * time.Millisecond) // a duplicate dispatch would panic the WaitGroup
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.msgs) != 1 {
		t.Fatalf("dispatched %d times, want 1", len(h.msgs))
	}
}
