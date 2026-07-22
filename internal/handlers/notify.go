package handlers

import (
	"context"
	"errors"
	"strings"

	"github.com/pburkhalter/notifyarr/internal/jellyfin"
	"github.com/pburkhalter/notifyarr/internal/seerr"
	"github.com/pburkhalter/notifyarr/internal/waha"
)

// Shared rendering helpers for the notify path. Journarr owns completion
// notifications (NOTIFY_MODE=journarr) and POSTs the already-grouped payload to
// POST /notify/send (see send.go); notifyarr resolves the Jellyfin deep-link +
// requester @mention and relays to WhatsApp. It runs no active checks of its own.

// jellyfinLink resolves the TMDB id to a Jellyfin item and builds the
// web-client deep link. Falls back to the bare library URL when the item
// hasn't been scanned yet.
func (b *Bot) jellyfinLink(ctx context.Context, tmdbID int, mediaType string) string {
	if it, err := b.Jellyfin.FindByTMDB(ctx, tmdbID, mediaType); err == nil {
		if u := jellyfin.ItemWebURL(b.Cfg.JellyfinExternalURL, it.ID, it.ServerID); u != "" {
			return u
		}
	}
	return b.Cfg.JellyfinExternalURL
}

// ─── requester @mention ───────────────────────────────────────────────────

// requesterMention finds the WhatsApp jid for the user who requested a
// given tmdb id. Returns ("@<phone>", "<phone>@c.us") for SendText's
// mentions slice, or ("","") when we can't resolve.
func (b *Bot) requesterMention(ctx context.Context, tmdbID int) (text string, jid string) {
	if tmdbID == 0 {
		return "", ""
	}
	r, err := b.Seerr.FindRequestByTMDB(ctx, tmdbID)
	if errors.Is(err, seerr.ErrNotFound) || err != nil {
		return "", ""
	}
	username := strings.ToLower(r.RequestedBy.JellyfinUserName)
	if username == "" {
		username = strings.ToLower(r.RequestedBy.Username)
	}
	phone := b.Cfg.PhoneMap[username]
	if phone == "" {
		return "", ""
	}
	return "@" + phone, waha.FormatJID(phone)
}
