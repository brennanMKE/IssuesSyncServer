-- 001_initial: full schema for IssuesSyncServer Phase A

CREATE EXTENSION IF NOT EXISTS citext;

CREATE TABLE IF NOT EXISTS users (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    email        citext      UNIQUE NOT NULL,
    display_name text        NOT NULL DEFAULT '',
    global_role  text        NOT NULL CHECK (global_role IN ('admin', 'member')),
    status       text        NOT NULL CHECK (status IN ('active', 'disabled')) DEFAULT 'active',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS passkeys (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    credential_id bytea       UNIQUE NOT NULL,
    public_key    bytea       NOT NULL,
    sign_count    bigint      NOT NULL DEFAULT 0,
    transports    text[]      NOT NULL DEFAULT '{}',
    label         text        NOT NULL DEFAULT '',
    last_used_at  timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS invites (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    email       citext      NOT NULL,
    role        text        NOT NULL,
    project_ids uuid[]      NOT NULL DEFAULT '{}',
    token_hash  bytea       UNIQUE NOT NULL,
    created_by  uuid        NOT NULL REFERENCES users(id),
    expires_at  timestamptz NOT NULL,
    used_at     timestamptz
);

CREATE TABLE IF NOT EXISTS sessions (
    id                 uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id            uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind               text        NOT NULL CHECK (kind IN ('web', 'native')),
    refresh_token_hash bytea       UNIQUE,
    client_label       text        NOT NULL DEFAULT '',
    created_at         timestamptz NOT NULL DEFAULT now(),
    last_seen_at       timestamptz NOT NULL DEFAULT now(),
    expires_at         timestamptz NOT NULL,
    revoked_at         timestamptz
);

CREATE TABLE IF NOT EXISTS projects (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    slug         text        UNIQUE NOT NULL,
    display_name text        NOT NULL,
    repo_url     text        NOT NULL DEFAULT '',
    created_by   uuid        NOT NULL REFERENCES users(id),
    created_at   timestamptz NOT NULL DEFAULT now(),
    archived_at  timestamptz
);

CREATE TABLE IF NOT EXISTS project_members (
    project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users(id)    ON DELETE CASCADE,
    role       text NOT NULL CHECK (role IN ('admin', 'editor', 'viewer')),
    PRIMARY KEY (project_id, user_id)
);

CREATE TABLE IF NOT EXISTS files (
    project_id   uuid        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    path         text        NOT NULL,
    etag         text        NOT NULL,
    size         bigint      NOT NULL DEFAULT 0,
    content_type text        NOT NULL DEFAULT 'text/plain',
    modified_at  timestamptz NOT NULL DEFAULT now(),
    modified_by  uuid        NOT NULL REFERENCES users(id),
    PRIMARY KEY (project_id, path)
);

CREATE TABLE IF NOT EXISTS events (
    id         bigserial   PRIMARY KEY,
    project_id uuid        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    payload    jsonb       NOT NULL,
    actor      uuid        NOT NULL REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS audit_log (
    id          bigserial   PRIMARY KEY,
    actor       uuid        NOT NULL REFERENCES users(id),
    action      text        NOT NULL,
    project_id  uuid        REFERENCES projects(id),
    path        text,
    etag_before text,
    etag_after  text,
    ip          inet,
    user_agent  text,
    created_at  timestamptz NOT NULL DEFAULT now()
);
