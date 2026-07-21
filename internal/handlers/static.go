package handlers

import (
	"context"
	"strings"

	"github.com/pburkhalter/notifyarr/internal/waha"
)

// help is the canonical command listing. Keep it short — WhatsApp users
// won't scroll a wall of text.
func (b *Bot) help(ctx context.Context, ev waha.MessageEvent) error {
	mention := b.Cfg.MentionToken()
	body := strings.Join([]string{
		"🤖 *Commands:*",
		"",
		mention + " *suche* `<titel>` — Top-3 Treffer",
		mention + " *request* `<titel>` — direkt anfragen",
		mention + " *status* — was gerade läuft",
		mention + " *neu* — letzte 10 hinzugefügt",
		mention + " *wartet* — offene Seerr-Requests",
		mention + " *stats* — Library-Zahlen",
		mention + " *library* — Jellyfin-Link",
		"",
		"Auf eine Suche kannst du mit *1*, *2* oder *3* antworten.",
	}, "\n")
	return b.reply(ctx, ev, body)
}

// library posts the Jellyfin URL. Useful for new users + as a quick
// "where do I watch" answer.
func (b *Bot) library(ctx context.Context, ev waha.MessageEvent) error {
	body := "🍿 *Jellyfin:* " + b.Cfg.JellyfinExternalURL +
		"\n🎯 *Wünschen:* " + b.Cfg.SeerrExternalURL
	return b.reply(ctx, ev, body)
}
