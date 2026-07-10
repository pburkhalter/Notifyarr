// Concierge: WhatsApp bot for the homelab streaming group.
//
// Wires WAHA (WhatsApp HTTP API), Jellyseerr, Sonarr, Radarr, and Jellyfin
// together so the group gets welcome messages, slash-style commands via
// @bot mentions, scheduled digests + polls, and richer download notifications.
//
// See docs/DESIGN.md for the architecture and command surface.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pburkhalter/waha-concierge/internal/config"
	"github.com/pburkhalter/waha-concierge/internal/handlers"
	"github.com/pburkhalter/waha-concierge/internal/jellyfin"
	"github.com/pburkhalter/waha-concierge/internal/logger"
	"github.com/pburkhalter/waha-concierge/internal/radarr"
	"github.com/pburkhalter/waha-concierge/internal/scheduler"
	"github.com/pburkhalter/waha-concierge/internal/seerr"
	"github.com/pburkhalter/waha-concierge/internal/sonarr"
	"github.com/pburkhalter/waha-concierge/internal/store"
	"github.com/pburkhalter/waha-concierge/internal/waha"
)

var versionStr = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version", "-v", "--version":
			fmt.Println("concierge", versionStr)
			return
		case "healthcheck":
			os.Exit(healthcheck())
		case "help", "-h", "--help":
			fmt.Println(`concierge — WhatsApp bot for the streaming group.

Usage:
  concierge              Run the bot daemon.
  concierge version      Print build version.
  concierge healthcheck  Probe /healthz on the local listener.

Configuration is via environment variables; see README.md.`)
			return
		}
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadFromOS()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	log := logger.New(cfg.LogLevel, cfg.LogFormat)
	log.Info("starting concierge",
		"version", versionStr,
		"listen", cfg.Listen,
		"waha", cfg.WAHAURL,
		"bot_phone", cfg.WAHABotPhone)

	rootCtx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	st, err := store.Open(rootCtx, cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	wahaClient := waha.NewClient(cfg.WAHAURL, cfg.WAHAAPIKey, cfg.WAHASession, cfg.HTTPTimeout)
	seerrClient := seerr.NewClient(cfg.SeerrURL, cfg.SeerrAPIKey, cfg.HTTPTimeout)
	sonarrClient := sonarr.NewClient(cfg.SonarrURL, cfg.SonarrAPIKey, cfg.HTTPTimeout)
	radarrClient := radarr.NewClient(cfg.RadarrURL, cfg.RadarrAPIKey, cfg.HTTPTimeout)
	jellyClient := jellyfin.NewClient(cfg.JellyfinURL, cfg.JellyfinAPIKey, cfg.JellyfinUserID, cfg.HTTPTimeout)

	bot := handlers.New(cfg, log, wahaClient, seerrClient, sonarrClient, radarrClient, jellyClient, st)
	bot.Version = versionStr

	// HTTP router. Surfaces:
	//   /waha-webhook          ← WAHA event push (messages, joins, votes)
	//   /webhook/sonarr        ← Sonarr "Connect" outbound webhook
	//   /webhook/radarr        ← Radarr "Connect" outbound webhook
	//   /streaming-status.json ← dashboard aggregator (issues + SceneNZB quota)
	//   /healthz               ← container healthcheck
	mux := http.NewServeMux()
	mux.Handle("/waha-webhook", (&waha.Receiver{
		Handler: bot,
		Logger:  log.With("component", "waha"),
	}).HTTPHandler())
	mux.Handle("/webhook/sonarr", bot.WebhookHandler("sonarr"))
	mux.Handle("/webhook/radarr", bot.WebhookHandler("radarr"))
	mux.Handle("/streaming-status.json", bot.StreamingStatusHandler())
	mux.Handle("/trigger", bot.TriggerSearchHandler())
	mux.Handle("/notify/send", bot.NotifyHandler()) // Journarr-owned notifications (NOTIFY_MODE=journarr)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"ok"}`)
	})

	// Scheduler. Jobs with empty cron specs are no-ops.
	sched := scheduler.New(log.With("component", "scheduler"))
	if err := sched.Add(scheduler.Job{Name: "weekly_digest", Spec: cfg.CronWeeklyDigest, Run: bot.WeeklyDigest}); err != nil {
		return fmt.Errorf("schedule digest: %w", err)
	}
	if err := sched.Add(scheduler.Job{Name: "weekly_poll", Spec: cfg.CronWeeklyPoll, Run: bot.WeeklyPoll}); err != nil {
		return fmt.Errorf("schedule poll: %w", err)
	}
	if err := sched.Add(scheduler.Job{Name: "daily_health", Spec: cfg.CronDailyHealth, Run: bot.DailyHealth}); err != nil {
		return fmt.Errorf("schedule health: %w", err)
	}
	sched.Start()
	defer sched.Stop()

	srv := &http.Server{Addr: cfg.Listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	errCh := make(chan error, 1)
	go func() {
		log.Info("http listening", "addr", cfg.Listen)
		errCh <- srv.ListenAndServe()
	}()

	// Background flush: episode-grouped notifications wait `flushAfter` for
	// at least one row to mature AND `flushQuiet` of silence (no new rows for
	// the show) so a slowly-trickling season import lands as one message.
	flushAfter := 10 * time.Minute
	flushQuiet := 5 * time.Minute
	go flushLoop(rootCtx, bot, flushAfter, flushQuiet, log.With("component", "flush"))

	// Background search-reaper: keeps the searches table small even when
	// users open a suche and never reply.
	go reapLoop(rootCtx, st, log.With("component", "reap"))

	select {
	case <-rootCtx.Done():
		shutdownCtx, c2 := context.WithTimeout(context.Background(), 10*time.Second)
		defer c2()
		_ = srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http: %w", err)
		}
	}
	log.Info("shutdown complete")
	return nil
}

func flushLoop(ctx context.Context, bot *handlers.Bot, wait, quietPeriod time.Duration, log *slog.Logger) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := bot.FlushPending(ctx, wait, quietPeriod); err != nil {
				log.Warn("flush failed", "err", err)
			}
		}
	}
}

func reapLoop(ctx context.Context, st *store.Store, log *slog.Logger) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := st.ReapSearches(ctx); err != nil {
				log.Warn("reap failed", "err", err)
			}
		}
	}
}

func healthcheck() int {
	addr := os.Getenv("LISTEN")
	if addr == "" {
		addr = ":8080"
	}
	if addr[0] == ':' {
		addr = "127.0.0.1" + addr
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + addr + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthcheck: status", resp.StatusCode)
		return 1
	}
	return 0
}
