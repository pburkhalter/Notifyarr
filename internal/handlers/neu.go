package handlers

import (
	"context"
	"fmt"
	"strings"

	"github.com/pburkhalter/notifyarr/internal/waha"
)

// neu lists the 10 most recently-added titles in Jellyfin. Movies show
// year, episodes show "Series — SxxEyy — Title".
func (b *Bot) neu(ctx context.Context, ev waha.MessageEvent) error {
	items, err := b.Jellyfin.RecentlyAdded(ctx, 10)
	if err != nil {
		b.Log.Warn("jellyfin recent", "err", err)
		return b.reply(ctx, ev, "⚠️ Konnte Jellyfin nicht abfragen.")
	}
	if len(items) == 0 {
		return b.reply(ctx, ev, "🤷 Noch nichts neu in der Library.")
	}
	var lines []string
	for _, it := range items {
		switch it.Type {
		case "Movie":
			year := ""
			if it.ProductionYear > 0 {
				year = fmt.Sprintf(" (%d)", it.ProductionYear)
			}
			lines = append(lines, "  🎬 "+truncate(it.Name, 55)+year)
		case "Episode":
			parent := it.SeriesName
			if parent == "" {
				parent = "?"
			}
			lines = append(lines, "  📺 "+truncate(parent, 35)+" — "+truncate(it.Name, 40))
		default:
			lines = append(lines, "  • "+truncate(it.Name, 55))
		}
	}
	body := "🆕 *Kürzlich hinzugefügt:*\n" + strings.Join(lines, "\n")
	return b.reply(ctx, ev, body)
}
