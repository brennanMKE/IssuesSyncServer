// Package wire contains the shared JSON types that form the wire protocol
// between the Go backend and the Swift Issues app. Field names must match
// RemoteProtocol.swift exactly so that encoding/decoding is symmetric.
package wire

// HostInfo is returned by GET /v1/host.
type HostInfo struct {
	DisplayName string `json:"displayName"`
	FolderCount int    `json:"folderCount"`
}

// FolderInfo is returned by GET /v1/folders and GET /v1/folders/{folderId}.
type FolderInfo struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	DisplayName string `json:"displayName"`
	RepoURL     string `json:"repoUrl"`
}

// IssueMetadata is one element in the array returned by
// GET /v1/folders/{folderId}/issues.
// ETag is omitempty so older Mac-as-host clients that don't send it still decode.
type IssueMetadata struct {
	ID   string  `json:"id"`
	ETag *string `json:"etag,omitempty"`
}

// IssueDetail is returned by GET /v1/folders/{folderId}/issues/{id}.
// The ETag is also set as a response header.
type IssueDetail struct {
	ID   string `json:"id"`
	Body string `json:"body"`
	ETag string `json:"etag"`
}

// ─── WebSocket message types ────────────────────────────────────────────────

// SubscribeMsg is sent client → server to subscribe to a folder's event stream.
type SubscribeMsg struct {
	Type     string `json:"type"`     // "subscribe"
	FolderID string `json:"folderId"`
	Since    *int64 `json:"since,omitempty"` // last seen event id for replay
}

// UnsubscribeMsg is sent client → server to cancel a folder subscription.
type UnsubscribeMsg struct {
	Type     string `json:"type"`     // "unsubscribe"
	FolderID string `json:"folderId"`
}

// IssueChangedEvent is broadcast server → client when an issue is mutated.
type IssueChangedEvent struct {
	Type     string  `json:"type"`     // "issueChanged"
	FolderID string  `json:"folderId"`
	IssueID  string  `json:"issueId"`
	Op       string  `json:"op"`       // "created" | "updated" | "deleted"
	ETag     *string `json:"etag,omitempty"` // null for deleted
	Actor    string  `json:"actor"`
	Ts       string  `json:"ts"` // RFC 3339 with milliseconds
}

// AttachmentChangedEvent is broadcast server → client when an attachment changes.
type AttachmentChangedEvent struct {
	Type     string  `json:"type"`     // "attachmentChanged"
	FolderID string  `json:"folderId"`
	IssueID  string  `json:"issueId"`
	Name     string  `json:"name"`
	Op       string  `json:"op"`       // "created" | "updated" | "deleted"
	ETag     *string `json:"etag,omitempty"`
	Actor    string  `json:"actor"`
	Ts       string  `json:"ts"`
}

// ProjectMetaChangedEvent is broadcast server → client when project metadata changes.
type ProjectMetaChangedEvent struct {
	Type     string  `json:"type"`     // "projectMetaChanged"
	FolderID string  `json:"folderId"`
	ETag     *string `json:"etag,omitempty"`
	Actor    string  `json:"actor"`
	Ts       string  `json:"ts"`
}

// AccessRevokedEvent is broadcast server → client when the user's membership
// is removed for the given folder mid-session.
type AccessRevokedEvent struct {
	Type     string `json:"type"`     // "accessRevoked"
	FolderID string `json:"folderId"`
}
