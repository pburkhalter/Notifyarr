# Notifyarr — Streaming Group Bot

A small Go service that wires a WhatsApp group (via WAHA) to the homelab
streaming stack: Jellyseerr, Sonarr, Radarr, Jellyfin, arrarr, TorBox.

## What it does

Three classes of behavior:

1. **Welcome** — when a user joins the WhatsApp "Streaming" group, post a
   short greeting that explains what the group is for, where to watch, and how
   to talk to the bot.

2. **Commands** — react to messages that @-mention the bot. Each command
   maps to one or more upstream API calls. The reply is plaintext (with light
   emoji) so it renders well in WhatsApp.

3. **Scheduled posts** — periodic digests + weekly polls, fired by an
   internal scheduler.

The bot also enhances existing notifications: instead of
Sonarr/Radarr's raw "X.GERMAN.WEB-DL has been imported" lines, post
formatted messages with poster, title, requester @mention, and (optionally)
group multiple episodes of the same show into a single message.

## Architecture

```
WhatsApp Group ── WAHA NOWEB engine ── HTTP API
                       │ ▲
              webhooks │ │ outgoing
                       ▼ │
                  ┌──────────────┐
                  │  notifyarr   │
                  ├──────────────┤
                  │ intents      │   parse "@bot suche dune"
                  │ dispatcher   │   route to handler
                  │ scheduler    │   weekly polls, daily digests
                  │ store (sqlite)│  active polls, dedup state
                  └──────┬───────┘
                         │ HTTP
       ┌──────────┬──────┼───────┬──────────┐
       ▼          ▼      ▼       ▼          ▼
   Jellyseerr  Sonarr  Radarr  Jellyfin   TorBox
```

The bot is a single process. State is intentionally minimal — Jellyseerr is
the source of truth for requests, Sonarr/Radarr for queue, Jellyfin for
library. The bot's own DB stores only:

- active polls (poll id → upstream message id → option → tmdb id mapping)
- welcome-message dedup (chat id + user id + timestamp, to suppress repeats
  on rejoin)
- per-user command rate limit counters (best-effort, in-memory)

## Commands

All commands require an @-mention of the bot's WhatsApp number. The leading
@bot is stripped before parsing. Case-insensitive verb. Reply goes to the
same chat the command came from.

| Command | Description | Calls |
|---|---|---|
| `help` | List available commands | (none) |
| `suche <q>` / `search <q>` | TMDB search via Seerr, top 3 results with TMDB ids | Seerr `/search` |
| `request <q>` | Search → if exactly 1 match, request it; else show top 3 | Seerr `/search` + `/request` |
| `1` / `2` / `3` (in reply to a `suche` result) | Request that result | Seerr `/request` |
| `status` | Active downloads with progress + ETA | Sonarr+Radarr `/queue` |
| `neu` / `new` | 10 most-recently-added titles | Jellyfin `/Items?SortBy=DateCreated` |
| `library` | Direct link to Jellyfin | (static) |
| `wer hat <q>?` | Who requested this | Seerr DB lookup |
| `stats` | Total library size, requests this week, top requester | Jellyfin `/Items/Counts` + Seerr |
| `wartet` | What's still in the Sonarr/Radarr queue waiting | queues |
| `ich` | Your Seerr account: # requests, quota left | Seerr `/user/me` |

Reply formatting uses simple WhatsApp-friendly text:

```
🔎 Drei Treffer für "Dune":

1️⃣ Dune (2021) 🎬
2️⃣ Dune: Part Two (2024) 🎬
3️⃣ Dune (1984) 🎬

Reply mit Nummer zum Requesten.
```

Numeric replies are matched against the most recent `suche` from the same
sender in the same chat (60 s window). If no recent search, the message is
ignored.

## Welcome message

WAHA fires a `group.v2.join` event on participant add. The bot replies in
the same chat:

```
👋 Willkommen, @user!

🎬 In dieser Gruppe poste ich automatisch, wenn ein Film oder eine Episode
bei uns auf Jellyfin landet.

🍿 Schauen: https://jellyfin.home.pburkhalter.ch
🎯 Wünschen: https://seerr.home.pburkhalter.ch (Login via Jellyfin)

🤖 Tippe @<bot> help für meine Commands.
```

Suppressed if the same `(chat_id, user_id)` joined in the last 24 h
(handles rejoin flicker).

## Scheduled posts

- **Sonntag 09:00** — "Diese Woche neu auf Jellyfin"
- **Freitag 19:00** — Poll: "Was schauen wir heute Abend?" (4 options from
  the last 14 days of recently-added)
- **Daily 08:00** — silent: scrape Seerr blocklist + Radarr/Sonarr queue
  for stuck items, only post if there's something actionable

Schedules are configurable via env vars (`CRON_DIGEST`, `CRON_POLL`, etc.).
Empty value disables that schedule.

## Enhanced notifications

Replaces the existing Sonarr/Radarr custom-script that posts raw lines.

Episode grouping: when ≥3 episodes of the same show arrive within 10
minutes, the bot debounces and posts a single "Show S03: 5 Episoden
hinzugefügt" instead of 5 separate notifications. Implemented as a write
buffer in the bot DB with a flush timer.

Requester @mention: look up the Seerr request by tmdb id, get the
Jellyseerr user, look up their WhatsApp phone number (config-mapped). When
found, include `@<phone>` in the message so WhatsApp pings them. When not
mapped, fall back to the username.

Cover image: WAHA's `sendImage` endpoint takes a URL. Use TMDB poster URL
from the Seerr search response, or Jellyfin's `/Items/{id}/Images/Primary`
when the title is already in the library.

## Module layout

```
cmd/notifyarr/main.go     — wiring + signal handling
internal/config/          — env parsing, validation
internal/logger/          — slog wrapper (same style as arrarr)
internal/store/           — sqlite, schema, polls + welcome + dedup tables
internal/waha/            — WAHA HTTP client + webhook receiver
internal/intents/         — parse @bot messages → Command struct
internal/seerr/           — Jellyseerr API client
internal/sonarr/          — Sonarr v3 API client (queue + recently-added)
internal/radarr/          — Radarr v3 API client
internal/jellyfin/        — Jellyfin API client (recently-added, items)
internal/torbox/          — optional, for direct cache checks
internal/scheduler/       — cron-style runner for digests + polls
internal/handlers/        — per-command handlers (help, suche, status, ...)
```

## Tech notes

- Go 1.25, single binary, distroless image.
- No web framework — net/http + chi for the webhook receiver, http.Client
  for outgoing.
- SQLite (modernc) for state. Same setup as arrarr.
- Scheduler: lightweight in-process. `github.com/robfig/cron/v3` if we need
  cron expressions; otherwise just `time.Ticker` per schedule.
- Distroless `nonroot` (uid 65532) — but in this deploy run as uid 568 to
  match the apps group (same lesson as arrarr).
- TrueNAS Custom App, image pulled from `ghcr.io/pburkhalter/notifyarr`.

## Config (env)

```
WAHA_URL                 http://waha-nas:3000
WAHA_API_KEY             ...
WAHA_SESSION             default
WAHA_CHAT_ID             1203...@g.us           # Streaming group jid
WAHA_BOT_PHONE           +41...                  # the bot's own number, used to detect @mentions

SEERR_URL                http://seerr-nas:5055
SEERR_API_KEY            ...
SONARR_URL               http://sonarr:8989
SONARR_API_KEY           ...
RADARR_URL               http://radarr:7878
RADARR_API_KEY           ...
JELLYFIN_URL             http://jellyfin:8096
JELLYFIN_API_KEY         ...

JELLYFIN_EXTERNAL_URL    https://jellyfin.home.pburkhalter.ch
SEERR_EXTERNAL_URL       https://seerr.home.pburkhalter.ch

# Optional schedule overrides (cron). Empty disables.
CRON_WEEKLY_DIGEST       0 9 * * 0
CRON_WEEKLY_POLL         0 19 * * 5
CRON_DAILY_HEALTH        0 8 * * *

# Per-user phone mapping for @mention on requests
PHONE_MAP_PATRIK         41...
PHONE_MAP_ADRIAN         41...

LISTEN                   :8080
DB_PATH                  /data/notifyarr.db
LOG_LEVEL                info
LOG_FORMAT               json
```

## What ships in MVP vs later

**MVP (first release)**
- Welcome message on join
- `help`, `library`, `status`, `neu`, `ich` commands
- WAHA webhook receiver wired
- Enhanced notification: episode grouping + @requester

**Phase 2**
- `suche` / `request` flow with numeric reply
- `wer hat`, `wartet`, `stats`
- Weekly digest

**Phase 3**
- Weekly poll
- Cover image attachments
- Daily health check

## Non-goals

- No own web UI. Jellyseerr already does that.
- No persistence of WhatsApp messages or user metadata beyond what's needed
  for the welcome dedup + active poll state.
- No multi-tenant. Single group, single TorBox account, single homelab.
- No moderation features. WhatsApp's group-admin tools handle that.
