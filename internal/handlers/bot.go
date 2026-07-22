// Package handlers wires intents + WAHA events to the upstream APIs and
// formats the WhatsApp replies. It implements waha.Handler so the webhook
// receiver can dispatch directly into it.
package handlers

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/pburkhalter/notifyarr/internal/config"
	"github.com/pburkhalter/notifyarr/internal/intents"
	"github.com/pburkhalter/notifyarr/internal/jellyfin"
	"github.com/pburkhalter/notifyarr/internal/prowlarr"
	"github.com/pburkhalter/notifyarr/internal/radarr"
	"github.com/pburkhalter/notifyarr/internal/seerr"
	"github.com/pburkhalter/notifyarr/internal/sonarr"
	"github.com/pburkhalter/notifyarr/internal/store"
	"github.com/pburkhalter/notifyarr/internal/waha"
)

// Bot is the dispatcher. Construct it once at startup and pass to
// waha.Receiver as the Handler.
type Bot struct {
	Cfg      *config.Config
	Log      *slog.Logger
	WAHA     *waha.Client
	Seerr    *seerr.Client
	Sonarr   *sonarr.Client
	Radarr   *radarr.Client
	Jellyfin *jellyfin.Client
	Store    *store.Store

	// Prowlarr is nil unless PROWLARR_URL + PROWLARR_API_KEY are set. Used
	// only by the /streaming-status.json aggregator for grab-quota headroom.
	Prowlarr *prowlarr.Client

	// Version is the running notifyarr build (main.versionStr), surfaced on
	// /streaming-status.json so Journarr can show it + check for updates.
	Version string

	// SearchTTL caps how long a numeric reply ("1") remains bound to the
	// most recent suche from the same sender. Keep short to avoid stale
	// replies firing requests for the wrong title.
	SearchTTL time.Duration

	// WelcomeCooldown suppresses repeat welcomes for the same user on a
	// rejoin within this window. WhatsApp groups occasionally fire join
	// events spuriously (e.g. on participant.changed flicker).
	WelcomeCooldown time.Duration
}

// New returns a Bot with sensible default TTLs.
func New(cfg *config.Config, log *slog.Logger, w *waha.Client, sr *seerr.Client,
	so *sonarr.Client, ra *radarr.Client, jf *jellyfin.Client, st *store.Store) *Bot {
	b := &Bot{
		Cfg: cfg, Log: log, WAHA: w, Seerr: sr, Sonarr: so, Radarr: ra,
		Jellyfin: jf, Store: st,
		SearchTTL:       2 * time.Minute,
		WelcomeCooldown: 24 * time.Hour,
	}
	if cfg.ProwlarrURL != "" && cfg.ProwlarrAPIKey != "" {
		b.Prowlarr = prowlarr.NewClient(cfg.ProwlarrURL, cfg.ProwlarrAPIKey, cfg.HTTPTimeout)
	}
	return b
}

// OnMessage routes inbound messages through the intents parser.
func (b *Bot) OnMessage(ctx context.Context, ev waha.MessageEvent) error {
	selfJID := waha.FormatJID(b.Cfg.WAHABotPhone)
	selfLID := b.Cfg.WAHABotLID + "@lid"
	mentionedSelf := false
	for _, m := range ev.MentionedIDs {
		if m == selfJID || m == selfLID {
			mentionedSelf = true
			break
		}
	}
	cmd := intents.Parse(ev.Body, b.Cfg.MentionTokens(), mentionedSelf)
	if cmd.Kind == intents.KindNone {
		// Useful when debugging "why didn't the bot respond" — shows the
		// body the parser saw plus whether the mentions[] hint fired.
		b.Log.Debug("message ignored (no intent)",
			"body", truncate(ev.Body, 80),
			"mentioned_self", mentionedSelf,
			"mentions", ev.MentionedIDs,
			"chat", ev.From)
		return nil
	}
	b.Log.Info("intent",
		"kind", cmd.Kind.String(),
		"arg", cmd.Arg,
		"chat", ev.From,
		"sender", ev.Participant)
	switch cmd.Kind {
	case intents.KindHelp:
		return b.help(ctx, ev)
	case intents.KindLibrary:
		return b.library(ctx, ev)
	case intents.KindStatus:
		return b.status(ctx, ev)
	case intents.KindNeu:
		return b.neu(ctx, ev)
	case intents.KindSuche:
		return b.suche(ctx, ev, cmd.Arg)
	case intents.KindRequest:
		return b.request(ctx, ev, cmd.Arg)
	case intents.KindNumericReply:
		return b.numericReply(ctx, ev, cmd.Arg)
	case intents.KindStats:
		return b.stats(ctx, ev)
	case intents.KindWartet:
		return b.wartet(ctx, ev)
	}
	return nil
}

// OnGroupJoin fires the welcome message (unless we already greeted this
// user within WelcomeCooldown).
func (b *Bot) OnGroupJoin(ctx context.Context, ev waha.GroupJoinEvent) error {
	for _, p := range ev.Participants {
		ok, err := b.Store.MarkWelcomed(ctx, ev.ID, p.ID, b.WelcomeCooldown)
		if err != nil {
			b.Log.Warn("welcome dedup write failed", "err", err, "user", p.ID)
			continue
		}
		if !ok {
			b.Log.Debug("welcome suppressed by cooldown", "chat", ev.ID, "user", p.ID)
			continue
		}
		if err := b.sendWelcome(ctx, ev.ID, p.ID); err != nil {
			b.Log.Error("welcome send failed", "err", err, "user", p.ID)
		}
	}
	return nil
}

// OnPollVote is wired for the weekly-poll workflow. When a vote arrives on
// a poll the bot remembers, log the choice — actual auto-request behaviour
// lands in the scheduler/poll handler in a follow-up.
func (b *Bot) OnPollVote(ctx context.Context, ev waha.PollVoteEvent) error {
	for _, idx := range ev.SelectedOptions {
		opt, err := b.Store.LookupPoll(ctx, ev.PollID, idx)
		if err != nil {
			continue // not our poll
		}
		b.Log.Info("poll vote", "poll_id", ev.PollID, "voter", ev.VoterID, "title", opt.Title)
	}
	return nil
}

// reply sends a text message back to the same chat the trigger came from.
// Any "@<bot-phone>" tokens in text are automatically promoted to true
// WhatsApp mentions so they render as the bot's contact/push name rather
// than as raw digits.
func (b *Bot) reply(ctx context.Context, ev waha.MessageEvent, text string) error {
	mentions := b.selfMentionIfPresent(text, nil)
	_, err := b.WAHA.SendText(ctx, ev.From, text, mentions)
	if err != nil {
		b.Log.Error("reply failed", "err", err, "chat", ev.From)
	}
	return err
}

// selfMentionIfPresent returns `extra` plus the bot's own jid if text
// contains the bot's mention token. WhatsApp needs the jid in mentions[]
// for the "@<phone>" substring to render as @<name>.
func (b *Bot) selfMentionIfPresent(text string, extra []string) []string {
	if !strings.Contains(text, b.Cfg.MentionToken()) {
		return extra
	}
	out := make([]string, 0, len(extra)+1)
	out = append(out, extra...)
	out = append(out, waha.FormatJID(b.Cfg.WAHABotPhone))
	return out
}

// truncate clips long titles/queries in WhatsApp replies.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// fmtPercent computes "(size-left / size)" inverted as a 0..100 progress
// string like "73%". Returns "" when size is zero (unknown).
func fmtPercent(size, sizeLeft int64) string {
	if size <= 0 {
		return ""
	}
	pct := int(100 * (size - sizeLeft) / size)
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return strconv.Itoa(pct) + "%"
}

// trimQuery normalises query-style arguments before hitting upstream APIs.
func trimQuery(s string) string { return strings.TrimSpace(s) }
