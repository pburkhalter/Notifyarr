package handlers

import (
	"context"
	"fmt"

	"github.com/pburkhalter/notifyarr/internal/waha"
)

// stats summarises the library state. Per-library counts are pulled from
// the Filme + Serien VirtualFolders so orphan rows from destroyed
// libraries don't inflate the numbers. Falls back to global /Items/Counts
// when the operator hasn't supplied JELLYFIN_*_LIBRARY_ID values.
func (b *Bot) stats(ctx context.Context, ev waha.MessageEvent) error {
	movies, series, episodes, ok := b.libraryCounts(ctx)
	if !ok {
		// Last-resort fallback. The numbers may be off (orphan items) but
		// it's better than no answer.
		counts, err := b.Jellyfin.Counts(ctx)
		if err != nil {
			return b.reply(ctx, ev, "⚠️ Jellyfin counts fehlgeschlagen.")
		}
		movies, series, episodes = counts.Movie, counts.Series, counts.Episode
	}
	body := fmt.Sprintf(`📊 *Library:*
  🎬 %d Filme
  📺 %d Serien (%d Episoden)`, movies, series, episodes)
	return b.reply(ctx, ev, body)
}

// libraryCounts queries each configured library individually. Returns
// false when no library ids are configured so the caller can fall back.
func (b *Bot) libraryCounts(ctx context.Context) (movies, series, episodes int, ok bool) {
	movID := b.Cfg.JellyfinMovieLibraryID
	serID := b.Cfg.JellyfinSeriesLibraryID
	if movID == "" || serID == "" {
		return 0, 0, 0, false
	}
	if n, err := b.Jellyfin.CountLibrary(ctx, movID, "Movie"); err == nil {
		movies = n
	}
	if n, err := b.Jellyfin.CountLibrary(ctx, serID, "Series"); err == nil {
		series = n
	}
	if n, err := b.Jellyfin.CountLibrary(ctx, serID, "Episode"); err == nil {
		episodes = n
	}
	return movies, series, episodes, true
}
