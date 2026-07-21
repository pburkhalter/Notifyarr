package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSearchRoundtrip(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	results := []SearchResult{
		{Slot: 1, TMDBID: 100, MediaType: "movie", Title: "A"},
		{Slot: 2, TMDBID: 200, MediaType: "tv", Title: "B"},
	}
	if err := s.SaveSearch(ctx, "chat", "user", results, time.Minute); err != nil {
		t.Fatalf("SaveSearch: %v", err)
	}
	got, err := s.LookupSearch(ctx, "chat", "user", 2)
	if err != nil {
		t.Fatalf("LookupSearch: %v", err)
	}
	if got.TMDBID != 200 || got.Title != "B" {
		t.Errorf("got %+v", got)
	}
	// Slot the sender didn't pick → ErrNotFound.
	if _, err := s.LookupSearch(ctx, "chat", "user", 7); err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
	// SaveSearch should *replace* — second save with one entry should leave
	// slot 2 missing from the lookup.
	if err := s.SaveSearch(ctx, "chat", "user", []SearchResult{{Slot: 1, TMDBID: 999, MediaType: "movie", Title: "Z"}}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupSearch(ctx, "chat", "user", 2); err != ErrNotFound {
		t.Errorf("expected slot 2 to be gone after replace, got %v", err)
	}
}

func TestWelcomeCooldown(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	ok, err := s.MarkWelcomed(ctx, "chat", "alice", 24*time.Hour)
	if err != nil || !ok {
		t.Fatalf("first welcome should fire: ok=%v err=%v", ok, err)
	}
	ok2, err := s.MarkWelcomed(ctx, "chat", "alice", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if ok2 {
		t.Error("second welcome within cooldown should suppress")
	}
	ok3, err := s.MarkWelcomed(ctx, "chat", "alice", time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	if !ok3 {
		t.Error("welcome after cooldown elapsed should fire again")
	}
}

func TestPollLookup(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	opts := []PollOption{
		{Index: 0, TMDBID: 11, MediaType: "movie", Title: "Sintel"},
		{Index: 1, TMDBID: 22, MediaType: "tv", Title: "Spider-Noir"},
	}
	if err := s.SavePoll(ctx, "p1", opts); err != nil {
		t.Fatal(err)
	}
	got, err := s.LookupPoll(ctx, "p1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.TMDBID != 22 {
		t.Errorf("got %+v", got)
	}
	if _, err := s.LookupPoll(ctx, "p1", 9); err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}
