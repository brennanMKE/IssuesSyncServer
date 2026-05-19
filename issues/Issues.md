# IssuesSyncServer

Go backend service (`issued`) that provides a central, always-on sync server for the Issues app. Stores project files in S3, metadata in Postgres, and fans out changes over WebSocket. Deployed on EC2 behind Apache 2 with Let's Encrypt TLS. See `RemoteSync.md` for the full design and `../Issues` for the Mac client.

This file is the local guide for managing issues in this project. The companion Mac app (Issues.app) watches the `issues/` folder and renders the current state. Markdown files (and `project.json`) are the source of truth — there is no generated artifact or index to keep in sync.

The `# IssuesSyncServer` heading above matches the `name` field in `issues/project.json`.

## Folder layout

```
issues/
├── project.json       # canonical project name + repo URL
├── Issues.md          # this file
├── 0001.md            # one file per issue
├── 0001/              # optional sibling folder for screenshots, crash logs, etc.
│   └── screenshot.png
├── 0002.md
└── …
```

## Status values

| File value | Display name | Meaning |
|---|---|---|
| `open` | Open | Filed but not yet started |
| `in-progress` | In Progress | Actively being worked on |
| `resolved` | Resolved | Work is done; awaiting user confirmation |
| `closed` | Closed | User has confirmed the fix |
| `wontfix` | Won't Fix | Acknowledged but won't be addressed |

## Critical rule: never close without explicit confirmation

An issue must **never** be marked `resolved`, `closed`, or `wontfix` based on inference — only when the user has confirmed in plain language. Leave status at `open` (or `in-progress`) until the user says "close this", "mark resolved", or "won't fix".

The deliberate exception: a subagent that finishes a fix may set `resolved`. It must not set `closed` — that's the user's call.

## Build / verify command

```bash
go build ./...
go test ./...
```

For integration tests that require Postgres and S3, see `CLAUDE.md` for local environment setup.

## Module conventions

| Module | Covers |
|---|---|
| `auth` | WebAuthn, session issuance, invite tokens, email |
| `api` | REST handlers (`/v1/*`) |
| `ws` | WebSocket hub and event fan-out |
| `admin` | HTML admin console (`/admin/*`) |
| `storage` | Postgres queries, S3 client, LRU cache |
| `db` | Migrations, schema |
| `wire` | Shared JSON types (must match `RemoteProtocol.swift`) |
| `infra` | Deployment config (systemd, Apache, certbot) |

## Resolving an issue (the standard workflow)

Each open issue is handled by a fresh subagent. The orchestrator picks the lowest-numbered `open` issue; the subagent claims it, does the work, commits, and marks it `resolved`.

Subagent steps: read `Issues.md` + `CLAUDE.md` → read the issue → set `in-progress` (working copy only) → implement → `go build ./... && go test ./...` → code commit (`#NNNN Verb: title`) → update issue markdown (status `resolved`, Closed date, Commit hash, Root cause, Fix, Files changed) → no second commit needed (no git repo yet).

## Attachments

Screenshots and logs go in `issues/NNNN/`. Reference them with paths relative to the `.md` file: `NNNN/filename.ext`.
