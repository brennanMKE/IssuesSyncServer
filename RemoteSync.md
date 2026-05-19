# Remote Sync — design plan

Working draft. Multi-user, multi-device synchronization of issue folders through a central Go backend hosted on the existing EC2 box. The Issues.app stays a local-files viewer; the backend handles identity, authorization, durable storage, and live fan-out across viewers.

This is the counterpart to `RemoteAccess.md`. That document describes the Mac-as-host peer-to-peer topology (shipped under #0076). This document describes a different topology — a shared, always-on backend — meant to support multiple developers working on the same project concurrently. The two can coexist; if the backend topology proves out, the Mac-as-host code becomes a candidate for retirement.

## Goals

- **Central source of truth on the backend.** A Go service on EC2 holds the canonical bytes for every project; Macs are working copies that pull on change and push on edit.
- **Multi-developer concurrency.** Two or more users can edit the same project at the same time. Optimistic ETag concurrency prevents silent overwrites; conflicts surface in the app instead of disappearing.
- **Local files remain the in-app source of truth.** Issues.app continues to render from `NNNN.md` on disk and trigger off `FolderWatcher`. The sync engine is layered *under* the watcher — it writes files when remote changes arrive and uploads files when local changes are detected. The app's read path is unchanged.
- **Reuse the existing viewer wire protocol** (`RemoteProtocol.swift`, `RemoteClient.swift`, `RemoteWebSocket`). The Go backend exposes the same `/v1/host`, `/v1/folders`, `/v1/folders/{id}/issues[...]`, and `/v1/events` surface. New write endpoints are additive.
- **Password-free auth.** Email + PassKeys (WebAuthn / FIDO2) for both the web admin console and the native app. No passwords stored, no password reset flow, no shared secrets.
- **Roles per project.** `admin`, `editor`, `viewer`. Editors can push file changes; viewers cannot. Admins manage users and projects.
- **Standards-aligned TLS.** Public DNS + Let's Encrypt via certbot (already on the EC2 box). Native client uses normal CA chain validation — no fingerprint pinning for the backend path.
- **One operator, low-ops.** Single binary, systemd, nginx in front, Postgres on box or RDS, S3 for bytes. No Kubernetes, no service mesh.

## Non-goals (v1)

- **No real-time co-editing.** No OT/CRDT, no character-level merging. Conflict = 409 + a "remote changed" prompt; editor fetches, re-applies, retries.
- **No offline editing with merge replay.** If the network is gone the app keeps reading local files. Edits made while disconnected upload when the connection returns, but only if the server's ETag still matches; otherwise the user resolves the conflict.
- **No third-party SSO.** Email + PassKey only. No Google / GitHub / Apple sign-in until there's a real reason to add the surface.
- **No open signup.** Every user enters the system via an admin-issued invite.
- **No mobile clients yet.** PassKey + sync work is designed to be portable to iOS, but the iPhone target tracked under #0108 is out of scope for this plan.
- **No replacement of the Mac-as-host code yet.** #0076's topology stays shipped; this plan adds a parallel path. Deprecation is a separate decision, after the backend is real.

## Topology

```
                ┌────────────────────────────────────────────┐
                │  EC2 (Ubuntu, public DNS, Let's Encrypt)   │
                │                                            │
                │  nginx ── TLS terminate ──┐                │
                │                           │ 127.0.0.1:8080 │
                │                    ┌──────▼───────┐        │
                │                    │  issued (Go) │        │
                │                    │  - REST      │        │
                │                    │  - WS fanout │        │
                │                    │  - Admin UI  │        │
                │                    │  - WebAuthn  │        │
                │                    └──┬──────┬────┘        │
                │                       │      │             │
                │                ┌──────▼──┐ ┌─▼─────┐       │
                │                │Postgres │ │  S3   │       │
                │                │(meta)   │ │(bytes)│       │
                │                └─────────┘ └───────┘       │
                └────────────────────────────────────────────┘
                                    ▲     ▲
                                    │     │  (HTTPS + WSS,
                                    │     │   bearer session)
                ┌───────────────────┴──┐  └────────────────────┐
                │  Mac (Issues.app)    │                       │
                │  ┌────────────────┐  │     ┌──────────────┐  │
                │  │ local folder   │  │     │ Web browser  │  │
                │  │ NNNN.md files  │  │     │ /admin       │  │
                │  └────┬───────────┘  │     │ (operators)  │  │
                │       │FSEvents      │     └──────────────┘  │
                │  ┌────▼──────────┐   │
                │  │ SyncEngine    │   │
                │  │ - uploads     │   │
                │  │ - fetches     │   │
                │  │ - WS listener │   │
                │  └───────────────┘   │
                └──────────────────────┘
```

Three actors:

- **Backend** — Go binary on EC2. Authoritative store; mediates all multi-user interactions; serves both the native API and the admin web console from the same process.
- **Native client** — Issues.app on Mac. Configured with a server URL in Settings; signs in via PassKey; binds a chosen empty local folder to a remote project; the app's existing reader operates over that folder unchanged.
- **Admin console** — same Go binary, server-rendered HTML at `/admin/*`. Used to invite users, create projects, assign roles, inspect the audit log.

## Auth model

### Identities
- One `user` per email address. Email is the login identifier and the channel for invites.
- Each user has one or more registered `passkey` credentials (laptop, phone, hardware key).
- Users have a global role (`admin` | `member`) and a per-project role (`admin` | `editor` | `viewer`). The per-project role is what gates API calls; the global `admin` bit unlocks the admin console and acts as an implicit `admin` on every project.

### Enrollment
- **First admin** is bootstrapped from env: `ADMIN_EMAIL=brennan@…`. On first boot the backend creates the user row with global role `admin`, marks email unverified, and prints a one-shot enrollment URL (also retrievable via `issued admin enroll-url`). Visiting the URL within 24 h triggers email verification + PassKey registration.
- **Subsequent users** receive an invite from an existing admin: admin enters email + role + project assignments → backend mints a signed invite token (JWT, 7-day expiry, single-use) → backend sends a one-link email. The invitee clicks the link, the page verifies the email implicitly (they received it), and they register a PassKey. No password ever exists.
- **Adding a second device** for an existing user: signed-in user goes to "Devices" in admin console, clicks "Add device", scans the displayed QR code (encodes a short-lived registration token) on the new device, registers a PassKey on the new device.

### Sessions
- **Web admin**: cookie session, `HttpOnly; Secure; SameSite=Strict`. Server-side session record in Postgres so logout/revoke is real.
- **Native app**: PassKey assertion against a per-app challenge endpoint → server issues a short-lived **access token** (JWT, 15 min) and a long-lived **refresh token** (opaque, 30 days, rotated on use). Both stored in the Mac Keychain. The existing `RemoteClient`'s `Authorization: Bearer …` mechanism is reused — the only change is what the token contains.
- **WebAuthn relying party** (`RP_ID`) is the public DNS name. The same RP works for the web admin and for the native app (Apple's `ASAuthorizationPlatformPublicKeyCredentialProvider` accepts an `rpID`).

### Authorization
- Every API call is authorized against `(user, project, action)`.
  - `viewer` → all `GET`s.
  - `editor` → `viewer` + `PUT` / `POST` / `DELETE` on issues and attachments within the project.
  - `admin` → editor + project metadata mutations + member changes for that project.
  - Global `admin` → all of the above on all projects + user management + audit log access.
- Failed authorization returns `403 forbidden`. Authentication failures (missing/invalid/expired token) return `401 unauthorized`; the native client already maps this to a refresh attempt followed by a re-prompt for PassKey assertion if the refresh fails.

## Wire protocol

The existing read endpoints are kept intact so the Mac viewer code (`RemoteClient`, `RemoteHostIssueSource`) ports with minimal change.

### Reused (read)
| Method | Path | Notes |
|---|---|---|
| `GET` | `/v1/host` | `displayName` = server's configured name; `folderCount` is the count of projects this user has access to. |
| `GET` | `/v1/folders` | List of `FolderInfo` for projects the authenticated user is a member of. |
| `GET` | `/v1/folders/{folderId}` | One `FolderInfo`. |
| `GET` | `/v1/folders/{folderId}/issues` | `[IssueMetadata]`, each carrying an `etag` (new field; tolerated as nullable on older clients). |
| `GET` | `/v1/folders/{folderId}/issues/{id}` | `IssueDetail` with an `ETag` response header set to the file's SHA-256. |
| `GET` | `/v1/folders/{folderId}/issues/{id}/attachments/{name}` | Streamed bytes (current behavior). |
| `WS`  | `/v1/events` | Subscribe/unsubscribe per folder; receives events on change. |

### New (write)
| Method | Path | Auth | Body / Headers | Returns |
|---|---|---|---|---|
| `PUT` | `/v1/folders/{folderId}/issues/{id}` | editor+ | Raw markdown body, `If-Match: <etag>` (or `If-None-Match: *` for create). | `200` + new `ETag`; `409` on mismatch; `412` if `If-Match` missing. |
| `POST` | `/v1/folders/{folderId}/issues` | editor+ | `{ id, body }`; server validates id pattern `^\d{4}$` and uniqueness. | `201` + `ETag`; `409` if id already exists. |
| `DELETE` | `/v1/folders/{folderId}/issues/{id}` | editor+ | `If-Match: <etag>`. | `204`; `409` on mismatch. |
| `PUT` | `/v1/folders/{folderId}/issues/{id}/attachments/{name}` | editor+ | Raw bytes, `If-Match` for replace, `If-None-Match: *` for create. | `201` / `200` + `ETag`. |
| `DELETE` | `/v1/folders/{folderId}/issues/{id}/attachments/{name}` | editor+ | — | `204`. |
| `PUT` | `/v1/folders/{folderId}/project.json` | admin+ | Raw bytes, `If-Match`. | `200` + `ETag`. |

### ETag definition
- ETag is the lowercase hex SHA-256 of the file's bytes. Content-addressed and self-verifying.
- The server stores the ETag in `files.etag`; the S3 object is also tagged with it as metadata so an out-of-band restore from S3 can rebuild the index.

### Events
WebSocket message types (additive on top of the existing event stream):

```jsonc
// Server → client
{ "type": "subscribed", "folderId": "…" }
{ "type": "issueChanged", "folderId": "…", "issueId": "0042",
  "op": "created" | "updated" | "deleted",
  "etag": "ab12…",          // null for deleted
  "actor": "user-uuid",     // who caused it; client suppresses echoes of own writes
  "ts": "2026-05-18T12:34:56.789Z" }
{ "type": "attachmentChanged", "folderId": "…", "issueId": "0042",
  "name": "screenshot.png", "op": "created" | "updated" | "deleted",
  "etag": "…", "actor": "…", "ts": "…" }
{ "type": "projectMetaChanged", "folderId": "…", "etag": "…", "actor": "…", "ts": "…" }
{ "type": "accessRevoked", "folderId": "…" }   // member removed mid-session

// Client → server (existing)
{ "type": "subscribe", "folderId": "…" }
{ "type": "unsubscribe", "folderId": "…" }
```

`actor` lets the client suppress the echo of its own upload: after `PUT … /issues/0042` returns ETag `E2`, the client records `(folder, id) → E2` as "expecting"; when the matching `issueChanged` event arrives the client compares ETag and skips the fetch. Any *other* ETag triggers the normal fetch-and-replace path.

## Sync engine on the Mac

A new `SyncEngine` module sits between `FolderBookmarkService` (the local folder) and the remote backend.

### Binding a local folder to a remote project
1. User opens Settings → enters server URL (`https://issues.sstools.co`) → signs in (PassKey prompt).
2. Settings shows the project list (`GET /v1/folders`); user picks one they have access to.
3. User selects an **empty** folder on disk (security-scoped bookmark, same flow as today). If the folder is non-empty the picker refuses with "Pick an empty folder — the app needs to download a fresh copy."
4. App downloads every issue file, every attachment subdirectory, and `project.json` into that folder. Watcher is paused for the duration of the initial download; un-paused once disk state matches the server.
5. App stores `(serverURL, projectId, etagPerFile, lastSeenEventId)` so a future launch resumes without redownloading.

### Steady state
- **Local edit detected by FolderWatcher**
  - Debounce 150 ms (same as today).
  - For each changed file, compute SHA-256.
  - If the new hash differs from the server-known ETag → `PUT … If-Match: <known-etag>`.
    - `200` → record new ETag; the broadcast WS event will be suppressed as our echo.
    - `409` → conflict; see below.
  - If the file was deleted → `DELETE … If-Match: <known-etag>` (same conflict handling).
  - If the file is new (no known ETag) → `POST` or `PUT … If-None-Match: *`.
- **Remote change announced over WS**
  - If the ETag matches the "expecting" record from our own upload, skip.
  - Otherwise, `GET` the file, write it to disk (atomic temp-write + rename so the watcher sees one event), record the new ETag.
  - For `deleted`, remove the local file.
- **Watcher feedback loop**
  - Files we just wrote will fire FSEvents on the next pass. The SyncEngine recognizes them by ETag match against `etagPerFile` and skips them — no upload.

### Conflict handling (v1)
A `409` on upload means the server's ETag has moved since we last fetched. v1 policy:
1. Rename the local file to `NNNN.md.local-conflict-<yyyyMMdd-HHmmss>`.
2. `GET` the server's current version, write to `NNNN.md`.
3. Surface a one-line banner in the app: "Issue 0042 changed on the server; your edit was saved as `0042.md.local-conflict-…`." Banner links to a side-by-side diff sheet.
4. User merges manually, edits `0042.md`, FSEvents fires, normal upload path takes over.

Heavy-handed but safe: no edit is ever silently lost. Mergier flows can come later.

### Offline + reconnect
- WS dropped → exponential backoff reconnect (already exists for the peer topology, port verbatim).
- On reconnect, the client sends `{ "type": "subscribe", "folderId": "…", "since": "<lastSeenEventId>" }`. The server replays missed events since that id. If the gap is too large (server has GC'd older events) the server responds with a `resyncRequired` event and the client falls back to a full `GET /v1/folders/{id}/issues` reconcile.
- Local edits made while offline are queued in memory + a small on-disk log; replayed on reconnect with the conflict path above.

## Backend storage

### Postgres schema (outline)
```sql
users             (id uuid pk, email citext unique, display_name text,
                   global_role text check in ('admin','member'),
                   status text check in ('active','disabled'),
                   created_at, updated_at)
passkeys          (id uuid pk, user_id fk, credential_id bytea unique,
                   public_key bytea, sign_count bigint, transports text[],
                   label text, last_used_at, created_at)
invites           (id uuid pk, email citext, role text, project_ids uuid[],
                   token_hash bytea unique, created_by fk, expires_at, used_at)
sessions          (id uuid pk, user_id fk, kind text check in ('web','native'),
                   refresh_token_hash bytea unique, client_label text,
                   created_at, last_seen_at, expires_at, revoked_at)
projects          (id uuid pk, slug text unique, display_name text,
                   repo_url text, created_by fk, created_at, archived_at)
project_members   (project_id fk, user_id fk, role text, primary key (project_id, user_id))
files             (project_id fk, path text, etag text not null,
                   size bigint, content_type text,
                   modified_at, modified_by fk,
                   primary key (project_id, path))
events            (id bigserial pk, project_id fk, payload jsonb,
                   actor fk, created_at)  -- WS replay log; GC after N hours
audit_log         (id bigserial pk, actor fk, action text, project_id fk,
                   path text, etag_before text, etag_after text,
                   ip inet, user_agent text, created_at)
```

### S3 layout
```
<bucket>/
    projects/{projectId}/issues/{issueId}.md
    projects/{projectId}/issues/{issueId}/{attachmentName}
    projects/{projectId}/project.json
```
- The server supports any S3-compatible object store: AWS S3, Backblaze B2, Cloudflare R2, MinIO, DigitalOcean Spaces, Wasabi, etc. Configure via `S3_ENDPOINT` (omit for AWS), `S3_REGION`, and optionally `S3_ACCESS_KEY_ID`/`S3_SECRET_ACCESS_KEY`. On AWS, omitting the static credential vars falls back to IAM role auto-detection.
- Enable versioning + lifecycle (`NoncurrentVersionExpiration: 90 days`) on the bucket where the provider supports it (AWS S3, Backblaze B2); this gives free rollback without extra effort.
- Object metadata includes `etag-sha256` and `modified-by-user`.
- Set `S3_PATH_STYLE=true` for providers that require path-style addressing (MinIO, some self-hosted setups).

### Caching
- A small LRU in-process cache of recently-read file bytes (~64 MB) cuts S3 round-trips for the hot path. Invalidate on `PUT`/`DELETE` for the affected key.

## Admin console

Same Go binary, server-rendered HTML, no SPA. Routes:

| Path | Auth | Purpose |
|---|---|---|
| `/admin` | session | Dashboard: project list, recent activity. |
| `/admin/login` | — | Email entry → PassKey assertion. |
| `/admin/enroll/{token}` | invite token | First-time PassKey registration. |
| `/admin/users` | global admin | List, invite, change role, deactivate. |
| `/admin/users/{id}` | global admin | User detail; revoke individual sessions; reset PassKey set (forces re-enrollment). |
| `/admin/projects` | global admin | List, create, archive. |
| `/admin/projects/{slug}` | project admin | Edit metadata; member list. |
| `/admin/projects/{slug}/members` | project admin | Add/remove members; change per-project roles. |
| `/admin/projects/{slug}/files` | project admin | Read-only browser of current files (sanity inspection). |
| `/admin/audit` | global admin | Paginated audit log, filterable by user/project/action/time. |
| `/admin/sessions` | session | Your own active sessions; revoke. |
| `/admin/devices` | session | List your PassKeys; remove one; start "add device" QR flow. |

Tech choices:
- Go `html/template` + a thin partials helper. **htmx** for the small interactive bits (form submits, list refreshes) so we avoid a JS build pipeline.
- WebAuthn JS via `@simplewebauthn/browser` from a CDN (or vendored — small file). Server side uses `github.com/go-webauthn/webauthn`.
- CSS: a single stylesheet, system fonts, no framework. Match the app's visual tone (neutral, readable, dense).

## TLS and cert handling

- Backend already has Let's Encrypt via certbot + nginx. No changes to the renewal flow.
- **Native client does not pin the backend cert.** Public CA chain validation is correct here; pinning a 60-day-rotated Let's Encrypt cert would mean either constant fingerprint churn or pinning the issuer chain (fragile).
- The existing `PinnedHostSessionDelegate` (#0114) stays in place for the Mac-as-host path. Settings stores a `trustMode` per server entry: `publicCA` (default for the new backend) | `pinnedSelfSigned` (Mac-as-host). `RemoteClient` switches delegates based on this field.

## Deployment

- **Binary**: `issued`, statically linked, ~15 MB. Embeds the admin HTML/CSS/JS via `embed.FS`.
- **systemd unit**: `issued.service` — `Restart=on-failure`, `User=issued`, listens on `127.0.0.1:8080`.
- **Apache 2**: terminates TLS on `:443` (certbot already in place). Requires `mod_proxy`, `mod_proxy_http`, and `mod_proxy_wstunnel`. Proxies all traffic to `127.0.0.1:8080`; WebSocket upgrade handled by `mod_proxy_wstunnel` with `ProxyPass /v1/events ws://127.0.0.1:8080/v1/events` ahead of the catch-all `ProxyPass / http://127.0.0.1:8080/`. Set `ProxyTimeout` to `3600` to keep WS connections alive.
- **Postgres**: on-box for v1 (small footprint). Migrate to RDS only if uptime/HA requires it.
- **S3**: one bucket per environment; versioning + lifecycle as above; IAM role attached to the instance.
- **Config (env)**: `BASE_URL`, `RP_ID`, `RP_DISPLAY_NAME`, `ADMIN_EMAIL`, `DATABASE_URL`, `S3_BUCKET`, `S3_ENDPOINT` (omit for AWS), `S3_REGION`, `S3_ACCESS_KEY_ID`, `S3_SECRET_ACCESS_KEY`, `S3_PATH_STYLE`, `JWT_SIGNING_KEY`, `INVITE_SIGNING_KEY`, `SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, `SMTP_PASS`, `SMTP_FROM`.
- **Email**: SMTP (any provider). Templates are plain text + minimal HTML. On AWS, SES can be used as the SMTP relay via the standard SMTP credentials it issues — no SDK or IAM integration required.
- **Backups**: nightly `pg_dump` → S3 bucket (lifecycle: keep 30 daily, 12 monthly). File bytes already in S3 with versioning.
- **Observability**: structured JSON logs to stdout (`slog`), `journalctl` for retention; `/healthz` returns 200 with build SHA; `/metrics` Prometheus endpoint optional later.
- **Repo**: separate repo from this one (`issues-sync-server`), Go modules, single `cmd/issued/main.go` + packages for `auth`, `storage`, `api`, `ws`, `admin`, `db`. The Swift `RemoteProtocol.swift` is the wire contract; the Go side has an equivalent `wire/` package that must match field names exactly. A simple round-trip test on both sides catches drift.

## Roles in practice

| Role | Read issues | Create / edit / delete issues | Manage members | Create projects | Invite users |
|---|---|---|---|---|---|
| viewer | ✓ | — | — | — | — |
| editor | ✓ | ✓ | — | — | — |
| project admin | ✓ | ✓ | ✓ | — | — |
| global admin | ✓ | ✓ | ✓ | ✓ | ✓ |

`viewer` is useful for stakeholders watching progress without write access. `editor` is the common case (developers, plus the IssuesSkill running on their behalf). `global admin` is operator-only.

## Phased rollout

Each phase is independently deployable. Phases A–F are backend-only; the Mac app keeps working against Mac-as-host hosts the whole time.

- **Phase A — Backend skeleton.** Go service, Postgres migrations, S3 wiring, health endpoint, structured logs, systemd unit, nginx config.
- **Phase B — Auth.** WebAuthn registration + assertion; session issuance (web + native); admin bootstrap from env; invite token mint/redeem; SES email.
- **Phase C — Read API parity.** Port `/v1/host`, `/v1/folders`, `/v1/folders/{id}/issues[...]`, attachment streaming. Smoke-test against the existing Mac viewer by pointing `Settings → server URL` at the backend. (No write yet — viewer behaves as today.)
- **Phase D — Write API.** `PUT` / `POST` / `DELETE` with ETag concurrency. Audit log. Server-side validation of `NNNN.md` pattern, title format, etc.
- **Phase E — Event fanout.** `/v1/events` WS with subscribe/unsubscribe, `since` replay log, fan-out on every write, `accessRevoked` on member removal.
- **Phase F — Admin console.** HTML pages for users, invites, projects, members, audit log, sessions, devices.
- **Phase G — Mac Settings + sign-in.** New "Remote sync server" section in Settings; PassKey assertion; project list; "Connect this folder" picker (empty-folder requirement).
- **Phase H — SyncEngine.** Initial download into the chosen folder; FolderWatcher → upload; WS → fetch + atomic write; echo suppression; offline queue + reconnect.
- **Phase I — Conflict UI.** `.local-conflict-<ts>` write-aside; banner; diff sheet.
- **Phase J — Polish.** "Add device" QR flow; per-project subscription persistence on launch; "remove local copy" action; rotation of refresh tokens; production hardening (rate limits, request size caps, structured error responses).

## Decisions locked

- **Auth: PassKey + email only.** No passwords, no OAuth providers in v1.
- **Storage: any S3-compatible store for bytes, Postgres for metadata.** Single bucket with versioning + lifecycle where the provider supports it.
- **Concurrency: optimistic ETag (SHA-256 of bytes).** 409 on mismatch; client surfaces `.local-conflict-<ts>` and prompts.
- **Bootstrap: env-seeded first admin; subsequent users via signed email invite.** No open signup.
- **TLS: public CA chain validation for the backend path.** Pinning stays only for the Mac-as-host path.
- **Local folder must be empty on bind.** Reconciliation against existing folders is out of scope for v1.
- **Mac-as-host code (#0076 tree) stays shipped.** Deprecation revisited only after the backend topology is real and proven.

## Open questions

1. **Project creation flow** — admin web only, or also from the Mac app? *Lean: admin web only for v1; the Mac side is read+write of issues within a project, not a project management surface.*
2. **Conflict diff UI fidelity** — line-level diff sheet, or just open both files in the user's editor? *Lean: line-level diff sheet (small SwiftUI view) so users don't have to leave the app.*
3. **Attachment size cap** — current peer-to-peer streams arbitrary sizes. With S3 in the loop, a per-file cap (say 25 MB) keeps single-request semantics simple. Larger files would need multipart upload — defer.
4. **Audit log retention** — 90 days? 1 year? Forever in S3 archive? *Lean: 90 days hot in Postgres, daily roll-up exported to S3 indefinitely.*
5. **Refresh token rotation** — rotate on every use (more secure, more churn) or on a schedule? *Lean: rotate on use; revoke the prior on rotation; track a 30-second "grace" window for racing requests.*
6. **Email deliverability** — SES from the EC2 box's region; need DKIM/SPF on the public domain. Trivial but not free.
7. **Mac sign-out behavior** — wipe the local folder, or leave files in place? *Lean: leave in place; surface a "remove local copy" button. The files are user data.*
8. **What happens to in-flight edits when a user is demoted from editor to viewer?** Server rejects the next `PUT` with 403; client surfaces "your role changed" and parks the edit as a local conflict file. Same conflict UI as #0009 reuse.
9. **Repo layout** — backend in this repo under `server/`, or a separate `issues-sync-server` repo? *Lean: separate repo; this one stays a SwiftUI app, that one stays a Go service. Wire contract drift is caught by a generated JSON fixture in both.*

## Relationship to existing tickets

- `RemoteAccess.md` and #0076 describe the Mac-as-host topology, fully shipped. This plan does *not* modify that surface.
- `RemoteProtocol.swift` is the wire contract; this plan reuses it and extends it. New fields (`etag` on `IssueMetadata`, `actor` on events) must be added as `Optional` so older Mac-as-host responses still decode.
- A new umbrella tracking issue should be filed once this plan is reviewed, mirroring the structure of #0076 (one phase per section above, one ticket per concrete deliverable). That ticket comes after the design lands, not before.
