// Notifyarr: WhatsApp notifier + reactive bot for the homelab streaming group.
//
// Receives Journarr-owned completion notifications (POST /notify/send) and
// relays them to WhatsApp via WAHA, plus a reactive @bot command surface
// (search/request, library, status) over Jellyseerr/Sonarr/Radarr/Jellyfin.
// It does no active self-checks of its own.
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

	"github.com/pburkhalter/notifyarr/internal/config"
	"github.com/pburkhalter/notifyarr/internal/handlers"
	"github.com/pburkhalter/notifyarr/internal/jellyfin"
	"github.com/pburkhalter/notifyarr/internal/logger"
	"github.com/pburkhalter/notifyarr/internal/radarr"
	"github.com/pburkhalter/notifyarr/internal/seerr"
	"github.com/pburkhalter/notifyarr/internal/sonarr"
	"github.com/pburkhalter/notifyarr/internal/store"
	"github.com/pburkhalter/notifyarr/internal/waha"
)

var versionStr = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version", "-v", "--version":
			fmt.Println("notifyarr", versionStr)
			return
		case "healthcheck":
			os.Exit(healthcheck())
		case "help", "-h", "--help":
			fmt.Println(`notifyarr — WhatsApp notifier + bot for the streaming group.

Usage:
  notifyarr              Run the daemon.
  notifyarr version      Print build version.
  notifyarr healthcheck  Probe /healthz on the local listener.

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
	log.Info("starting notifyarr",
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
	//   /notify/send           ← Journarr-owned completion notifications (relayed to WhatsApp)
	//   /waha-webhook          ← WAHA event push (bot: messages, joins, votes)
	//   /streaming-status.json ← dashboard aggregator (issues + WAHA status)
	//   /healthz               ← container healthcheck
	mux := http.NewServeMux()
	mux.Handle("/notify/send", bot.NotifyHandler()) // Journarr-owned notifications (NOTIFY_MODE=journarr)
	mux.Handle("/waha-webhook", (&waha.Receiver{
		Handler: bot,
		Logger:  log.With("component", "waha"),
	}).HTTPHandler())
	mux.Handle("/streaming-status.json", bot.StreamingStatusHandler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"ok"}`)
	})

	srv := &http.Server{Addr: cfg.Listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	errCh := make(chan error, 1)
	go func() {
		log.Info("http listening", "addr", cfg.Listen)
		errCh <- srv.ListenAndServe()
	}()

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
