# IssuesSyncServer

`issued` is the backend sync server for [Issues](https://github.com/brennanMKE/Issues), a local-file issue tracker for Mac. It provides a central, always-on store so multiple developers can work on the same project concurrently — each Mac keeps real `NNNN.md` files on disk; the server holds the canonical copy and fans out changes in real time.

**Status:** early development. See [RemoteSync.md](RemoteSync.md) for the full design.

## How it works

- Issue files live in an S3-compatible bucket (AWS S3, Backblaze B2, Cloudflare R2, MinIO, etc.)
- Metadata and access control live in Postgres
- The Mac app syncs over a REST + WebSocket API that extends the existing [Issues wire protocol](RemoteSync.md#wire-protocol)
- Auth is PassKey / WebAuthn only — no passwords stored
- An admin console at `/admin` handles user invites, project setup, and audit logs

## Requirements

- Go 1.22+
- PostgreSQL 15+
- An S3-compatible object store
- An SMTP relay for invite emails
- A reverse proxy with TLS (Apache 2 or nginx); Let's Encrypt via certbot works well

## Building

```sh
go build -o issued ./cmd/issued
```

The binary embeds admin HTML/CSS/JS via `embed.FS` and has no runtime file dependencies.

## Configuration

All configuration is via environment variables. Copy `.env.example` to `.env` and fill in the values.

| Variable | Required | Notes |
|---|---|---|
| `BASE_URL` | yes | Public HTTPS base URL, e.g. `https://sync.example.com` |
| `RP_ID` | yes | WebAuthn relying party ID — typically the bare hostname |
| `RP_DISPLAY_NAME` | yes | Name shown in PassKey prompts |
| `ADMIN_EMAIL` | yes | Seeded as the first global admin on first boot |
| `DATABASE_URL` | yes | Postgres connection string |
| `S3_BUCKET` | yes | Bucket name |
| `S3_REGION` | yes | Region string; use `auto` for Cloudflare R2 |
| `S3_ENDPOINT` | no | Custom endpoint for S3-compatible providers; omit for AWS S3 |
| `S3_ACCESS_KEY_ID` | no | Static credentials; omit to use IAM role on AWS |
| `S3_SECRET_ACCESS_KEY` | no | Static credentials; omit to use IAM role on AWS |
| `S3_PATH_STYLE` | no | Set `true` for path-style addressing (e.g. MinIO) |
| `JWT_SIGNING_KEY` | yes | Secret for JWT access tokens |
| `INVITE_SIGNING_KEY` | yes | Secret for invite tokens |
| `SMTP_HOST` | yes | SMTP server hostname |
| `SMTP_PORT` | yes | SMTP port (typically `587` for STARTTLS) |
| `SMTP_USER` | yes | SMTP username |
| `SMTP_PASS` | yes | SMTP password |
| `SMTP_FROM` | yes | From address for outbound email |

## First boot

On first boot, `issued` seeds the `ADMIN_EMAIL` user and prints a one-shot enrollment URL:

```
issued: first-boot enrollment URL (valid 24h):
  https://sync.example.com/admin/enroll/<token>
```

Visit the URL to verify your email and register a PassKey. All subsequent users enter via admin-issued invites — there is no open signup.

The enrollment URL is also available at any time via:

```sh
issued admin enroll-url
```

## Deployment

A typical production setup on a single Linux host:

**systemd unit** (`/etc/systemd/system/issued.service`):

```ini
[Unit]
Description=issued sync server
After=network.target postgresql.service

[Service]
User=issued
EnvironmentFile=/etc/issued/env
ExecStart=/usr/local/bin/issued
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

**Apache 2 VirtualHost** (TLS terminated by certbot):

```apache
<VirtualHost *:443>
    ServerName sync.example.com

    # WebSocket endpoint — must appear before the catch-all ProxyPass
    ProxyPass /v1/events ws://127.0.0.1:8080/v1/events
    ProxyPassReverse /v1/events ws://127.0.0.1:8080/v1/events

    ProxyPass / http://127.0.0.1:8080/
    ProxyPassReverse / http://127.0.0.1:8080/

    ProxyTimeout 3600

    # ... certbot-managed TLS directives
</VirtualHost>
```

Required Apache modules: `mod_proxy`, `mod_proxy_http`, `mod_proxy_wstunnel`.

## Running the tests

```sh
go test ./...
```

Integration tests that require Postgres and S3 are gated behind a build tag and skipped by default:

```sh
go test -tags integration ./...
```

## License

MIT
