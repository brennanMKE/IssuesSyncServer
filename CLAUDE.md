
# IssuesSyncServer

Go backend (`issued`) that syncs issue folders for the Issues app. See `RemoteSync.md` for the full design. See `../Issues` for the Mac client.

## Deployment URL

`https://sync.sstools.co` — the canonical base URL for the server. This is also the WebAuthn `RP_ID`.

## Go module path

`sync.sstools.co` — set in `go.mod` as `module sync.sstools.co`. All internal imports follow this prefix, e.g. `sync.sstools.co/internal/auth`. Do not use a GitHub URL as the module path.

## Stack

- **Language**: Go, single binary, statically linked
- **Auth**: WebAuthn / PassKey (`github.com/go-webauthn/webauthn`), JWT access tokens, opaque refresh tokens
- **Storage**: Postgres (metadata), any S3-compatible object store (AWS S3, Backblaze B2, Cloudflare R2, MinIO, DigitalOcean Spaces, etc.)
- **Transport**: REST + WebSocket fan-out; Apache 2 terminates TLS (certbot), proxies to `127.0.0.1:8080`
- **Admin UI**: server-rendered HTML (`html/template` + htmx), no SPA

## Package layout

```
cmd/issued/        — main entry point
internal/
  auth/            — WebAuthn, session issuance, invite tokens
  api/             — REST handlers (/v1/*)
  ws/              — WebSocket hub and event fan-out
  admin/           — HTML admin console (/admin/*)
  storage/         — Postgres queries, S3 client, LRU cache
  db/              — migrations, schema
  wire/            — shared JSON types (must match RemoteProtocol.swift field names exactly)
```

## Wire contract

`/v1/*` endpoints must stay compatible with `RemoteProtocol.swift` in the Issues app. New fields on existing responses must be `omitempty` / optional so older Mac-as-host clients still decode them. The `wire/` package is the canonical Go-side type source.

## Key decisions

- ETag = lowercase hex SHA-256 of file bytes; stored in `files.etag` and S3 object metadata.
- Optimistic concurrency: `PUT`/`DELETE` require `If-Match`; mismatch → `409`.
- Auth: PassKey + email only; no passwords; no OAuth.
- First admin bootstrapped from `ADMIN_EMAIL` env var.
- Local folder bound to a remote project must be empty at bind time.

## Config (env)

| Variable | Required | Notes |
|---|---|---|
| `BASE_URL` | yes | Public HTTPS base URL, e.g. `https://sync.sstools.co` |
| `RP_ID` | yes | WebAuthn relying party ID (typically the hostname, e.g. `sync.sstools.co`) |
| `RP_DISPLAY_NAME` | yes | Human-readable name shown in PassKey prompts |
| `ADMIN_EMAIL` | yes | Email address seeded as the first global admin on first boot |
| `DATABASE_URL` | yes | Postgres connection string |
| `S3_BUCKET` | yes | Bucket name |
| `S3_ENDPOINT` | no | Custom endpoint URL for S3-compatible providers (e.g. `https://s3.us-west-004.backblazeb2.com`); omit for AWS S3 |
| `S3_REGION` | yes | Region string (required by SDK; use provider's value or `auto` for R2) |
| `S3_ACCESS_KEY_ID` | no | Static credentials; omit to use IAM role (AWS only) |
| `S3_SECRET_ACCESS_KEY` | no | Static credentials; omit to use IAM role (AWS only) |
| `S3_PATH_STYLE` | no | Set `true` for providers that require path-style addressing (e.g. MinIO) |
| `JWT_SIGNING_KEY` | yes | Secret for signing JWT access tokens |
| `INVITE_SIGNING_KEY` | yes | Secret for signing invite tokens |
| `SMTP_HOST` | yes | SMTP server hostname |
| `SMTP_PORT` | yes | SMTP port (typically `587` for STARTTLS) |
| `SMTP_USER` | yes | SMTP username |
| `SMTP_PASS` | yes | SMTP password |
| `SMTP_FROM` | yes | From address for outbound email |

## Phased rollout

A (skeleton) → B (auth) → C (read API) → D (write API) → E (WS fan-out) → F (admin console) → G (Mac sign-in) → H (SyncEngine) → I (conflict UI) → J (polish)
