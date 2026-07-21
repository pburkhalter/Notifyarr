package handlers

import (
	"context"
	"fmt"
	"strings"

	"github.com/pburkhalter/notifyarr/internal/seerr"
	"github.com/pburkhalter/notifyarr/internal/store"
	"github.com/pburkhalter/notifyarr/internal/waha"
)

// suche runs a TMDB-via-Seerr search and returns the top 3 hits. Results
// are remembered for the sender (SearchTTL window) so a follow-up "1" /
// "2" / "3" maps cleanly to a request.
func (b *Bot) suche(ctx context.Context, ev waha.MessageEvent, query string) error {
	q := trimQuery(query)
	if q == "" {
		return b.reply(ctx, ev, "Beispiel: "+b.Cfg.MentionToken()+" suche Dune")
	}
	results, err := b.Seerr.Search(ctx, q)
	if err != nil {
		b.Log.Warn("seerr search failed", "err", err, "query", q)
		return b.reply(ctx, ev, "⚠️ Suche fehlgeschlagen.")
	}
	top := pickTopMedia(results, 3)
	if len(top) == 0 {
		return b.reply(ctx, ev, "🤷 Keine Treffer für *"+q+"*.")
	}

	// Persist so a numeric reply within SearchTTL can identify the choice.
	rec := make([]store.SearchResult, 0, len(top))
	for i, r := range top {
		rec = append(rec, store.SearchResult{
			Slot:      i + 1,
			TMDBID:    r.ID,
			MediaType: r.MediaType,
			Title:     r.DisplayTitle(),
		})
	}
	if err := b.Store.SaveSearch(ctx, ev.From, ev.Participant, rec, b.SearchTTL); err != nil {
		b.Log.Warn("save search failed", "err", err)
	}

	var lines []string
	for i, r := range top {
		emoji := "🎬"
		if r.MediaType == "tv" {
			emoji = "📺"
		}
		year := r.Year()
		if year != "" {
			year = " (" + year + ")"
		}
		lines = append(lines, fmt.Sprintf("%s %s *%s*%s",
			numberEmoji(i+1), emoji, r.DisplayTitle(), year))
	}
	body := fmt.Sprintf("🔎 Treffer für *%s*:\n\n%s\n\n_Reply mit Nummer zum Anfragen._",
		q, strings.Join(lines, "\n"))
	return b.reply(ctx, ev, body)
}

// request runs a Seerr search and either fires the request directly (when
// exactly one match) or falls back to the suche flow.
func (b *Bot) request(ctx context.Context, ev waha.MessageEvent, query string) error {
	q := trimQuery(query)
	if q == "" {
		return b.reply(ctx, ev, "Beispiel: "+b.Cfg.MentionToken()+" request Mortal Kombat II")
	}
	results, err := b.Seerr.Search(ctx, q)
	if err != nil {
		return b.reply(ctx, ev, "⚠️ Suche fehlgeschlagen.")
	}
	top := pickTopMedia(results, 3)
	if len(top) == 0 {
		return b.reply(ctx, ev, "🤷 Keine Treffer für *"+q+"*.")
	}
	if len(top) == 1 {
		return b.fireRequest(ctx, ev, top[0])
	}
	// Multiple candidates → reuse the suche flow so the user picks.
	return b.suche(ctx, ev, query)
}

// numericReply resolves a "1" / "2" / "3" against the sender's most recent
// search and fires the request. Replies are silent (no echo) if no recent
// search — they're plausibly meant for someone else in the chat.
func (b *Bot) numericReply(ctx context.Context, ev waha.MessageEvent, slot string) error {
	n := slot[0] - '0'
	got, err := b.Store.LookupSearch(ctx, ev.From, ev.Participant, int(n))
	if err == store.ErrNotFound {
		return nil
	}
	if err != nil {
		b.Log.Warn("lookup search", "err", err)
		return nil
	}
	return b.fireRequest(ctx, ev, seerr.SearchResult{
		ID:        got.TMDBID,
		MediaType: got.MediaType,
		Title:     got.Title,
	})
}

// fireRequest creates the Seerr request and confirms in-channel. Failure
// reports the error plainly so the user can retry or escalate.
func (b *Bot) fireRequest(ctx context.Context, ev waha.MessageEvent, r seerr.SearchResult) error {
	emoji := "🎬"
	kind := "Film"
	if r.MediaType == "tv" {
		emoji = "📺"
		kind = "Serie"
	}
	if _, err := b.Seerr.Request(ctx, seerr.RequestSpec{TMDBID: r.ID, MediaType: r.MediaType}); err != nil {
		b.Log.Warn("seerr request failed", "err", err, "tmdb", r.ID, "title", r.DisplayTitle())
		return b.reply(ctx, ev,
			fmt.Sprintf("⚠️ Konnte %s *%s* nicht anfragen: %s",
				kind, r.DisplayTitle(), err.Error()))
	}
	return b.reply(ctx, ev,
		fmt.Sprintf("✅ %s *%s* angefragt. Push wenn fertig. %s",
			kind, r.DisplayTitle(), emoji))
}

// pickTopMedia filters Seerr results down to movies + tv (drops persons,
// collections etc.) and trims to n.
func pickTopMedia(results []seerr.SearchResult, n int) []seerr.SearchResult {
	out := make([]seerr.SearchResult, 0, n)
	for _, r := range results {
		if r.MediaType == "movie" || r.MediaType == "tv" {
			out = append(out, r)
			if len(out) >= n {
				break
			}
		}
	}
	return out
}

// numberEmoji turns 1..9 into the corresponding WhatsApp keycap emoji.
// WhatsApp renders these as nice 1️⃣ 2️⃣ 3️⃣ chips.
func numberEmoji(n int) string {
	switch n {
	case 1:
		return "1️⃣"
	case 2:
		return "2️⃣"
	case 3:
		return "3️⃣"
	case 4:
		return "4️⃣"
	case 5:
		return "5️⃣"
	case 6:
		return "6️⃣"
	case 7:
		return "7️⃣"
	case 8:
		return "8️⃣"
	case 9:
		return "9️⃣"
	}
	return fmt.Sprintf("(%d)", n)
}
