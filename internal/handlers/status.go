package handlers

import (
	"context"
	"fmt"
	"strings"

	"github.com/pburkhalter/notifyarr/internal/seerr"
	"github.com/pburkhalter/notifyarr/internal/waha"
)

// status reports the current Sonarr + Radarr queues. Sonarr items get
// "Show — S01E03 — 73%, 4m left" formatting; Radarr is the same minus the
// episode segment. Items the user can't act on (importBlocked, error) are
// suffixed with an emoji marker so it's easy to scan.
func (b *Bot) status(ctx context.Context, ev waha.MessageEvent) error {
	sq, sErr := b.Sonarr.Queue(ctx)
	rq, rErr := b.Radarr.Queue(ctx)
	if sErr != nil && rErr != nil {
		return b.reply(ctx, ev, "⚠️ Konnte Queue nicht abfragen.")
	}

	var lines []string
	for _, it := range sq {
		lines = append(lines, "  📺 "+formatQueueRow(it.Title, it.Status, it.TrackedDownloadState, it.Timeleft, fmtPercent(it.Size, it.SizeLeft)))
	}
	for _, it := range rq {
		lines = append(lines, "  🎬 "+formatQueueRow(it.Title, it.Status, it.TrackedDownloadState, it.Timeleft, fmtPercent(it.Size, it.SizeLeft)))
	}

	if len(lines) == 0 {
		return b.reply(ctx, ev, "✅ Queue ist leer — nichts läuft gerade.")
	}
	body := fmt.Sprintf("⏳ *Aktuelle Downloads (%d):*\n", len(lines)) + strings.Join(lines, "\n")
	return b.reply(ctx, ev, body)
}

// wartet lists every Seerr request whose media isn't fully Available yet
// (status < 5). The Sonarr/Radarr download queues used to be shown here too
// but rip through items in seconds — the group never sees them. Seerr's
// pending list is the more useful "what's unterwegs" view.
func (b *Bot) wartet(ctx context.Context, ev waha.MessageEvent) error {
	reqs, err := b.Seerr.ListRequests(ctx, 50)
	if err != nil {
		return b.reply(ctx, ev, "⚠️ Seerr nicht erreichbar.")
	}

	var pending []seerr.Request
	for _, r := range reqs {
		if r.Media.Status < 5 {
			pending = append(pending, r)
		}
	}
	if len(pending) == 0 {
		return b.reply(ctx, ev, "✅ Keine offenen Requests.")
	}

	lines := make([]string, 0, len(pending))
	for _, r := range pending {
		title, _ := b.Seerr.MediaTitle(ctx, r.Type, r.Media.TmdbID)
		if title == "" {
			title = fmt.Sprintf("TMDB#%d", r.Media.TmdbID)
		}
		emoji := "🎬"
		if r.Type == "tv" {
			emoji = "📺"
		}
		bits := []string{truncate(title, 40), mediaStatusLabel(r.Media.Status)}
		if who := requesterName(r); who != "" {
			bits = append(bits, who)
		}
		lines = append(lines, "  "+emoji+" "+strings.Join(bits, " · "))
	}

	body := fmt.Sprintf("🕗 *Offene Requests (%d):*\n%s", len(lines), strings.Join(lines, "\n"))
	return b.reply(ctx, ev, body)
}

// mediaStatusLabel maps Seerr's media status enum (1=UNKNOWN, 2=PENDING,
// 3=PROCESSING, 4=PARTIALLY_AVAILABLE, 5=AVAILABLE) to user-facing German.
// 5 is filtered out before this is called.
func mediaStatusLabel(s int) string {
	switch s {
	case 2:
		return "ausstehend"
	case 3:
		return "in Bearbeitung"
	case 4:
		return "teilweise verfügbar"
	}
	return "unbekannt"
}

func requesterName(r seerr.Request) string {
	if r.RequestedBy.DisplayName != "" {
		return r.RequestedBy.DisplayName
	}
	if r.RequestedBy.JellyfinUserName != "" {
		return r.RequestedBy.JellyfinUserName
	}
	return r.RequestedBy.Username
}

// formatQueueRow shapes one line. The state markers and the time-left are
// emoji-prefixed so a long row still scans visually.
func formatQueueRow(title, status, downloadState, timeLeft, pct string) string {
	t := truncate(title, 50)
	var bits []string
	bits = append(bits, t)
	if pct != "" {
		bits = append(bits, pct)
	}
	if timeLeft != "" && timeLeft != "00:00:00" {
		bits = append(bits, timeLeft+" left")
	}
	switch downloadState {
	case "importBlocked":
		bits = append(bits, "⚠️ import blockiert")
	case "importPending":
		bits = append(bits, "⏳ wartet auf Import")
	}
	switch status {
	case "warning":
		bits = append(bits, "⚠️")
	case "error", "failed":
		bits = append(bits, "❌")
	}
	return strings.Join(bits, " · ")
}
