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
| `/waha-webhook` | WAHA — incoming WhatsApp messages, group joins, poll votes |
| `/webhook/sonarr` | Sonarr "Connect → Webhook" |
| `/webhook/radarr` | Radarr "Connect → Webhook" |
| `/healthz` | Container healthcheck |

The Sonarr + Radarr custom-script notifications that posted directly to
WAHA can be removed once those webhooks are pointed at notifyarr — notifyarr
takes over notification formatting, batching, and @-mentions.

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
