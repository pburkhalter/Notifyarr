package store

import (
	"context"
	"testing"
	"time"
)

func TestSendsRoundtripAndReap(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	if _, err := s.LookupSend(ctx, "k1"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound before recording, got %v", err)
	}
	if err := s.RecordSend(ctx, "k1", "msg-1"); err != nil {
		t.Fatal(err)
	}
	if id, err := s.LookupSend(ctx, "k1"); err != nil || id != "msg-1" {
		t.Fatalf("LookupSend = %q, %v", id, err)
	}
	// Nothing is old enough yet.
	if n, err := s.ReapSends(ctx, time.Hour); err != nil || n != 0 {
		t.Fatalf("reap = %d, %v; want 0", n, err)
	}
	// Age the row past the window.
	if _, err := s.db.ExecContext(ctx, `UPDATE sends SET sent_at = ?`, time.Now().UTC().Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ReapSends(ctx, time.Hour); err != nil || n != 1 {
		t.Fatalf("reap = %d, %v; want 1", n, err)
	}
	if _, err := s.LookupSend(ctx, "k1"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after reap, got %v", err)
	}
}
