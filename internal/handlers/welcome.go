package handlers

import (
	"context"
	"fmt"

	"github.com/pburkhalter/notifyarr/internal/waha"
)

// sendWelcome posts the standard greeting to chatID, @mentioning the new
// participant. WAHA needs the mention jid in the `mentions` slice AND the
// matching "@<phone>" token already present in the body text — that's how
// WhatsApp renders it as a clickable ping.
func (b *Bot) sendWelcome(ctx context.Context, chatID, userJID string) error {
	phone := waha.ParsePhoneFromJID(userJID)
	mention := "@" + phone
	if phone == "" {
		mention = "Hi"
	}
	body := fmt.Sprintf(`👋 Willkommen, %s!

🎬 In dieser Gruppe poste ich automatisch, wenn ein Film oder eine Episode auf Jellyfin landet.

🍿 *Schauen:* %s
🎯 *Wünschen:* %s (Login via Jellyfin)

🤖 Tippe %s help für meine Commands.`,
		mention,
		b.Cfg.JellyfinExternalURL,
		b.Cfg.SeerrExternalURL,
		b.Cfg.MentionToken())

	var mentions []string
	if phone != "" {
		mentions = []string{userJID}
	}
	// Promote the bot's own "@<phone>" reference in the body so it renders
	// as the bot's name on the recipients' clients.
	mentions = b.selfMentionIfPresent(body, mentions)
	_, err := b.WAHA.SendText(ctx, chatID, body, mentions)
	return err
}
