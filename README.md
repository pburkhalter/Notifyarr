# notifyarr

WhatsApp bot for the homelab streaming group. Wires [WAHA](https://waha.devlike.pro)
to Jellyseerr, Sonarr, Radarr, and Jellyfin so the group gets:

- **Welcome message** on join — personalised, links to Jellyfin + Seerr
- **`@bot` commands** for search, request, status, recently-added, etc.
- **Scheduled posts** — weekly digest of new content, weekly "what should we
  watch?" poll, optional daily health-check on stuck imports
- **Enhanced notifications** — episode-batched (one message per season
  burst), @mention the requester, poster image inline

Designed alongside [arrarr](https://github.com/pburkhalter/Arrarr) and shares
its operational style — single Go binary, distroless image, TrueNAS Custom
App deploy.

See [docs/DESIGN.md](docs/DESIGN.md) for the architecture and the full
command surface.

## Commands

Every command requires a leading `@<bot-phone>` mention.

| Command | What it does |
|---|---|
| `help` / `hilfe` | List commands |
| `suche <q>` / `search <q>` | Top 3 TMDB matches; reply with 1/2/3 to request |
| `request <q>` | Search → if exactly 1 match, request it; else fall back to suche |
| `status` | Current Sonarr + Radarr queues with progress + ETA |
| `wartet` | Items waiting / blocked (`importBlocked`, queued, warning) |
| `neu` / `new` | Last 10 added to Jellyfin |
| `wer hat <q>?` | Who requested this title |
| `stats` | Library counts + recent-request tally |
| `ich` | Your account snapshot (by phone-map lookup) |
| `library` | Jellyfin + Seerr URLs |
| `1`/`2`/`3` (in reply to a suche) | Request that result |

## Configuration

All env-var driven. See [`deploy/docker-compose.example.yaml`](deploy/docker-compose.example.yaml)
for the full list with sensible defaults. Required minimum:

```
WAHA_URL              http://waha-nas:3000
WAHA_CHAT_ID          1203...@g.us
WAHA_BOT_PHONE        +41...
SEERR_URL + SEERR_API_KEY
SONARR_URL + SONARR_API_KEY
RADARR_URL + RADARR_API_KEY
JELLYFIN_URL + JELLYFIN_API_KEY + JELLYFIN_USER_ID
JELLYFIN_EXTERNAL_URL + SEERR_EXTERNAL_URL
```

Security-relevant, both required in practice:

```
WAHA_WEBHOOK_HMAC_KEY   must equal WAHA's WHATSAPP_HOOK_HMAC_KEY; every event on
                        /waha-webhook is verified (X-Webhook-Hmac, sha512). Unset ⇒
                        the endpoint answers 503 and the bot is off. Only
                        WAHA_WEBHOOK_INSECURE=true accepts unsigned events.
NOTIFY_SEND_TOKEN       shared secret Journarr sends as X-Notify-Token on /notify/send
                        (NOTIFY_MODE=journarr requires it).
```

Bot commands are accepted only from `WAHA_CHAT_ID` and from direct chats of
phones listed in `PHONE_MAP_*`; anything else is logged and ignored.

Per-user phone mapping uses prefixed env vars so adding a user is one
line of compose:

```
PHONE_MAP_PATRIK=41791112233
PHONE_MAP_ADRIAN=41799998877
```

The lowercased suffix has to match the Jellyseerr `username` /
`jellyfinUsername` field.

## Webhook wiring

Four inbound surfaces, all on port 8080:

| Path | Source |
|---|---|
| `/waha-webhook` | WAHA — incoming WhatsApp messages, group joins, poll votes (HMAC-signed, see above) |
| `/notify/send` | Journarr — completion notices to render and relay to WhatsApp (`X-Notify-Token`; an `X-Idempotency-Key` makes a retried delivery return the message already sent) |
| `/streaming-status.json` | Journarr health poller — issues, WAHA session state, grab quota |
| `/healthz` | Container healthcheck |

Sonarr/Radarr do not talk to notifyarr directly any more: Journarr owns the
completion notices (grouping per request, @-mentions resolved here).

## Build & deploy

```sh
go test ./...
go build ./cmd/notifyarr
docker build -f deploy/Dockerfile -t notifyarr .
```

Tagged pushes (`vX.Y.Z`) trigger the release workflow to publish
`ghcr.io/pburkhalter/notifyarr`.

## License

MIT.
