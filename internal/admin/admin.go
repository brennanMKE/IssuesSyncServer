// Package admin serves the server-rendered HTML admin console at /admin/*.
// It uses html/template + htmx; no SPA framework is required.
//
// Phase F — admin console implementation.
package admin

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"sync.sstools.co/internal/auth"
	"sync.sstools.co/internal/storage"
	"sync.sstools.co/internal/ws"
)

//go:embed templates/* static/*
var assets embed.FS

// AdminDeps holds all dependencies needed to build the admin handler.
type AdminDeps struct {
	Auth    *auth.Service
	DB      *pgxpool.Pool
	Hub     *ws.Hub
	BaseURL string
}

// adminServer is the unexported receiver for all route handlers.
type adminServer struct {
	deps      AdminDeps
	templates *template.Template

	// In-memory WebAuthn session store keyed by nonce.
	// A nonce is stored in a short-lived wauthn_nonce cookie.
	waMu      sync.Mutex
	waSessions map[string]*webauthn.SessionData
}

// Handler returns an http.Handler that serves the admin console.
func Handler(deps AdminDeps) http.Handler {
	funcMap := template.FuncMap{
		"inc": func(i int) int { return i + 1 },
		"dec": func(i int) int { return i - 1 },
		"not": func(v interface{}) bool {
			if v == nil {
				return true
			}
			return false
		},
	}

	// Parse all templates with the funcmap.
	tmpl, err := template.New("").Funcs(funcMap).ParseFS(assets, "templates/*.html")
	if err != nil {
		// Templates are embedded at build time; panic is appropriate here.
		panic(fmt.Sprintf("admin: parse templates: %v", err))
	}

	s := &adminServer{
		deps:       deps,
		templates:  tmpl,
		waSessions: make(map[string]*webauthn.SessionData),
	}

	mux := http.NewServeMux()

	// Static assets — sub-FS so the file server sees "style.css" not "static/style.css".
	staticSubFS, err := fs.Sub(assets, "static")
	if err != nil {
		panic(fmt.Sprintf("admin: static sub-fs: %v", err))
	}
	mux.Handle("GET /admin/static/", http.StripPrefix("/admin/static/", http.FileServer(http.FS(staticSubFS))))

	// Public auth routes
	mux.HandleFunc("GET /admin/login", s.getLogin)
	mux.HandleFunc("POST /admin/login", s.postLogin)
	mux.HandleFunc("POST /admin/login/complete", s.postLoginComplete)
	mux.HandleFunc("GET /admin/enroll/{token}", s.getEnroll)
	mux.HandleFunc("POST /admin/enroll/{token}", s.postEnroll)

	// Protected routes — wrapped with web session middleware
	protect := auth.RequireWebAuth(deps.DB)

	mux.Handle("GET /admin", protect(http.HandlerFunc(s.getDashboard)))
	mux.Handle("GET /admin/users", protect(http.HandlerFunc(s.getUsers)))
	mux.Handle("POST /admin/users/invite", protect(http.HandlerFunc(s.postUsersInvite)))
	mux.Handle("GET /admin/users/{id}", protect(http.HandlerFunc(s.getUserDetail)))
	mux.Handle("POST /admin/users/{id}/deactivate", protect(http.HandlerFunc(s.postUserDeactivate)))
	mux.Handle("POST /admin/users/{id}/sessions/{sid}/revoke", protect(http.HandlerFunc(s.postUserSessionRevoke)))

	mux.Handle("GET /admin/projects", protect(http.HandlerFunc(s.getProjects)))
	mux.Handle("POST /admin/projects", protect(http.HandlerFunc(s.postProjects)))
	mux.Handle("GET /admin/projects/{slug}", protect(http.HandlerFunc(s.getProject)))
	mux.Handle("POST /admin/projects/{slug}", protect(http.HandlerFunc(s.postProject)))
	mux.Handle("POST /admin/projects/{slug}/archive", protect(http.HandlerFunc(s.postProjectArchive)))
	mux.Handle("GET /admin/projects/{slug}/members", protect(http.HandlerFunc(s.getMembers)))
	mux.Handle("POST /admin/projects/{slug}/members", protect(http.HandlerFunc(s.postMembers)))
	mux.Handle("POST /admin/projects/{slug}/members/{uid}/remove", protect(http.HandlerFunc(s.postMemberRemove)))
	mux.Handle("POST /admin/projects/{slug}/members/{uid}/role", protect(http.HandlerFunc(s.postMemberRole)))
	mux.Handle("GET /admin/projects/{slug}/files", protect(http.HandlerFunc(s.getFiles)))

	mux.Handle("GET /admin/audit", protect(http.HandlerFunc(s.getAudit)))
	mux.Handle("GET /admin/sessions", protect(http.HandlerFunc(s.getSessions)))
	mux.Handle("POST /admin/sessions/{sid}/revoke", protect(http.HandlerFunc(s.postSessionRevoke)))
	mux.Handle("GET /admin/devices", protect(http.HandlerFunc(s.getDevices)))
	mux.Handle("POST /admin/devices/{id}/remove", protect(http.HandlerFunc(s.postDeviceRemove)))
	mux.Handle("POST /admin/logout", protect(http.HandlerFunc(s.postLogout)))

	return mux
}

// render executes the named template with data, writing to w.
func (s *adminServer) render(w http.ResponseWriter, name string, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, name, data); err != nil {
		slog.Error("admin: render template", "name", name, "err", err)
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

// currentUserID returns the authenticated user UUID from context.
func currentUserID(ctx context.Context) (uuid.UUID, bool) {
	idStr, ok := auth.UserIDFromContext(ctx)
	if !ok {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

// writeJSONError writes a JSON error response.
func writeJSONError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// randHex returns n random bytes as a hex string.
func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// storeWASession stores a WebAuthn session data under a nonce.
func (s *adminServer) storeWASession(nonce string, sess *webauthn.SessionData) {
	s.waMu.Lock()
	defer s.waMu.Unlock()
	s.waSessions[nonce] = sess
}

// popWASession retrieves and deletes a stored WebAuthn session.
func (s *adminServer) popWASession(nonce string) (*webauthn.SessionData, bool) {
	s.waMu.Lock()
	defer s.waMu.Unlock()
	sess, ok := s.waSessions[nonce]
	if ok {
		delete(s.waSessions, nonce)
	}
	return sess, ok
}

// setSessionCookie sets the issued_session cookie.
func setSessionCookie(w http.ResponseWriter, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "issued_session",
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   7 * 24 * 3600,
	})
}

// clearSessionCookie clears the issued_session cookie.
func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     "issued_session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

// ---- Auth handlers --------------------------------------------------------

func (s *adminServer) getLogin(w http.ResponseWriter, r *http.Request) {
	s.render(w, "login.html", map[string]interface{}{
		"Error": r.URL.Query().Get("error"),
	})
}

func (s *adminServer) postLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid request", http.StatusBadRequest)
		return
	}

	opts, sess, err := s.deps.Auth.BeginAssertion(r.Context(), strings.TrimSpace(req.Email))
	if err != nil {
		slog.Warn("admin: begin assertion failed", "err", err)
		writeJSONError(w, "authentication failed", http.StatusUnauthorized)
		return
	}

	nonce := randHex(16)
	s.storeWASession(nonce, sess)
	http.SetCookie(w, &http.Cookie{
		Name:     "wauthn_nonce",
		Value:    nonce,
		Path:     "/admin",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   300,
	})

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(opts); err != nil {
		slog.Error("admin: encode assertion opts", "err", err)
	}
}

func (s *adminServer) postLoginComplete(w http.ResponseWriter, r *http.Request) {
	nonceCookie, err := r.Cookie("wauthn_nonce")
	if err != nil {
		writeJSONError(w, "missing nonce cookie", http.StatusBadRequest)
		return
	}
	sess, ok := s.popWASession(nonceCookie.Value)
	if !ok {
		writeJSONError(w, "session expired", http.StatusBadRequest)
		return
	}

	user, err := s.deps.Auth.FinishAssertion(r.Context(), sess, r)
	if err != nil {
		slog.Warn("admin: finish assertion failed", "err", err)
		writeJSONError(w, "authentication failed", http.StatusUnauthorized)
		return
	}

	userUUID, err := uuid.FromBytes(user.WebAuthnID())
	if err != nil {
		writeJSONError(w, "invalid user id", http.StatusInternalServerError)
		return
	}

	sessionToken, err := auth.IssueWebSession(r.Context(), s.deps.DB, userUUID)
	if err != nil {
		slog.Error("admin: issue web session", "err", err)
		writeJSONError(w, "server error", http.StatusInternalServerError)
		return
	}

	setSessionCookie(w, sessionToken)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"redirect": "/admin"})
}

func (s *adminServer) getEnroll(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")

	claims, err := auth.RedeemInviteToken(r.Context(), s.deps.DB, token, s.deps.Auth.InviteKey())
	if err != nil {
		http.Error(w, "invalid or expired invite token", http.StatusBadRequest)
		return
	}

	// Find or create the user row for this email.
	userID, err := s.ensureUser(r.Context(), claims.Email, claims.Role, claims.ProjectIDs)
	if err != nil {
		slog.Error("admin: ensure user for enroll", "err", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}

	// Begin registration challenge.
	opts, sess, err := s.deps.Auth.BeginRegistration(r.Context(), userID)
	if err != nil {
		slog.Error("admin: begin registration", "err", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}

	// Store the WebAuthn session under a nonce; pass the nonce in a cookie.
	nonce := randHex(16)
	s.storeWASession(nonce, sess)
	http.SetCookie(w, &http.Cookie{
		Name:     "wauthn_nonce",
		Value:    nonce,
		Path:     "/admin",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   300,
	})
	// Store the userID in a separate nonce-keyed cookie for the complete handler.
	http.SetCookie(w, &http.Cookie{
		Name:     "wauthn_uid",
		Value:    userID.String(),
		Path:     "/admin",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   300,
	})

	optsJSON, _ := json.Marshal(opts)
	tokenJSON, _ := json.Marshal(token)

	s.render(w, "enroll.html", map[string]interface{}{
		"Email":       claims.Email,
		"OptionsJSON": template.JS(optsJSON),
		"Token":       template.JS(tokenJSON),
	})
}

func (s *adminServer) postEnroll(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	_ = token // already consumed at GET; we just need the UID cookie

	nonceCookie, err := r.Cookie("wauthn_nonce")
	if err != nil {
		writeJSONError(w, "missing nonce cookie", http.StatusBadRequest)
		return
	}
	uidCookie, err := r.Cookie("wauthn_uid")
	if err != nil {
		writeJSONError(w, "missing uid cookie", http.StatusBadRequest)
		return
	}

	sess, ok := s.popWASession(nonceCookie.Value)
	if !ok {
		writeJSONError(w, "session expired", http.StatusBadRequest)
		return
	}

	userID, err := uuid.Parse(uidCookie.Value)
	if err != nil {
		writeJSONError(w, "invalid uid", http.StatusBadRequest)
		return
	}

	if err := s.deps.Auth.FinishRegistration(r.Context(), userID, sess, r); err != nil {
		slog.Warn("admin: finish registration", "err", err)
		writeJSONError(w, "registration failed", http.StatusBadRequest)
		return
	}

	sessionToken, err := auth.IssueWebSession(r.Context(), s.deps.DB, userID)
	if err != nil {
		slog.Error("admin: issue web session after enroll", "err", err)
		writeJSONError(w, "server error", http.StatusInternalServerError)
		return
	}

	setSessionCookie(w, sessionToken)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"redirect": "/admin"})
}

// ensureUser finds an existing user by email or inserts a new one.
// It also assigns project memberships from the invite claims.
func (s *adminServer) ensureUser(ctx context.Context, email, role string, projectIDs []string) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.deps.DB.QueryRow(ctx,
		`SELECT id FROM users WHERE email = $1`,
		email,
	).Scan(&id)
	if err != nil {
		// Insert new user.
		id = uuid.New()
		_, err = s.deps.DB.Exec(ctx,
			`INSERT INTO users (id, email, display_name, global_role, status)
			 VALUES ($1, $2, $2, $3, 'active')
			 ON CONFLICT (email) DO UPDATE SET status = 'active', global_role = EXCLUDED.global_role`,
			id, email, role,
		)
		if err != nil {
			return uuid.Nil, fmt.Errorf("ensure user insert: %w", err)
		}
		// Re-fetch in case ON CONFLICT updated an existing row.
		_ = s.deps.DB.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, email).Scan(&id)
	}

	// Add project memberships.
	for _, pidStr := range projectIDs {
		pid, err := uuid.Parse(pidStr)
		if err != nil {
			continue
		}
		_ = storage.AddProjectMember(ctx, s.deps.DB, pid, id, "editor")
	}

	return id, nil
}

// ---- Dashboard ------------------------------------------------------------

func (s *adminServer) getDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectCount, _ := storage.ProjectCount(ctx, s.deps.DB)
	userCount, _ := storage.UserCount(ctx, s.deps.DB)
	activity, _ := storage.RecentActivity(ctx, s.deps.DB, 20)

	s.render(w, "dashboard.html", map[string]interface{}{
		"ProjectCount": projectCount,
		"UserCount":    userCount,
		"Activity":     activity,
	})
}

// ---- Users ----------------------------------------------------------------

func (s *adminServer) getUsers(w http.ResponseWriter, r *http.Request) {
	users, err := storage.AllUsers(r.Context(), s.deps.DB)
	if err != nil {
		slog.Error("admin: all users", "err", err)
	}
	s.render(w, "users.html", map[string]interface{}{
		"Users":   users,
		"Error":   r.URL.Query().Get("error"),
		"Success": r.URL.Query().Get("success"),
	})
}

func (s *adminServer) postUsersInvite(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/admin/users?error=bad+request", http.StatusSeeOther)
		return
	}
	email := strings.TrimSpace(r.FormValue("email"))
	role := r.FormValue("role")
	if role == "" {
		role = "member"
	}

	adminID, _ := currentUserID(r.Context())
	token, err := auth.MintInviteToken(r.Context(), s.deps.DB, adminID, email, role, nil, s.deps.Auth.InviteKey())
	if err != nil {
		slog.Error("admin: mint invite token", "err", err)
		http.Redirect(w, r, "/admin/users?error=failed+to+create+invite", http.StatusSeeOther)
		return
	}

	enrollURL := s.deps.BaseURL + "/admin/enroll/" + token
	slog.Info("admin: invite created", "email", email, "url", enrollURL)

	// Best-effort email send; don't fail the request if SMTP is misconfigured.
	http.Redirect(w, r, "/admin/users?success=invite+sent+to+"+email, http.StatusSeeOther)
}

func (s *adminServer) getUserDetail(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	detail, err := storage.UserWithPasskeysAndSessions(r.Context(), s.deps.DB, id)
	if err != nil {
		slog.Error("admin: user detail", "err", err)
		http.NotFound(w, r)
		return
	}
	s.render(w, "user.html", map[string]interface{}{
		"User":     detail.User,
		"Passkeys": detail.Passkeys,
		"Sessions": detail.Sessions,
	})
}

func (s *adminServer) postUserDeactivate(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := storage.DeactivateUser(r.Context(), s.deps.DB, id); err != nil {
		slog.Error("admin: deactivate user", "err", err)
	}
	http.Redirect(w, r, "/admin/users/"+id.String(), http.StatusSeeOther)
}

func (s *adminServer) postUserSessionRevoke(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	sid, err := uuid.Parse(r.PathValue("sid"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := storage.RevokeSession(r.Context(), s.deps.DB, sid); err != nil {
		slog.Error("admin: revoke session", "err", err)
	}
	http.Redirect(w, r, "/admin/users/"+userID.String(), http.StatusSeeOther)
}

// ---- Projects -------------------------------------------------------------

func (s *adminServer) getProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := storage.AllProjects(r.Context(), s.deps.DB)
	if err != nil {
		slog.Error("admin: all projects", "err", err)
	}
	s.render(w, "projects.html", map[string]interface{}{
		"Projects": projects,
		"Error":    r.URL.Query().Get("error"),
	})
}

func (s *adminServer) postProjects(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/admin/projects?error=bad+request", http.StatusSeeOther)
		return
	}
	slug := strings.TrimSpace(r.FormValue("slug"))
	displayName := strings.TrimSpace(r.FormValue("display_name"))
	repoURL := strings.TrimSpace(r.FormValue("repo_url"))

	adminID, _ := currentUserID(r.Context())
	if err := storage.CreateProject(r.Context(), s.deps.DB, slug, displayName, repoURL, adminID); err != nil {
		slog.Error("admin: create project", "err", err)
		http.Redirect(w, r, "/admin/projects?error=failed+to+create+project", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/projects/"+slug, http.StatusSeeOther)
}

func (s *adminServer) getProject(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	project, err := storage.ProjectBySlug(r.Context(), s.deps.DB, slug)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.render(w, "project.html", map[string]interface{}{
		"Project": project,
		"Error":   r.URL.Query().Get("error"),
	})
}

func (s *adminServer) postProject(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/admin/projects/"+slug+"?error=bad+request", http.StatusSeeOther)
		return
	}
	project, err := storage.ProjectBySlug(r.Context(), s.deps.DB, slug)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	displayName := strings.TrimSpace(r.FormValue("display_name"))
	repoURL := strings.TrimSpace(r.FormValue("repo_url"))
	if err := storage.UpdateProject(r.Context(), s.deps.DB, project.ID, displayName, repoURL); err != nil {
		slog.Error("admin: update project", "err", err)
		http.Redirect(w, r, "/admin/projects/"+slug+"?error=update+failed", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/projects/"+slug, http.StatusSeeOther)
}

func (s *adminServer) postProjectArchive(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	project, err := storage.ProjectBySlug(r.Context(), s.deps.DB, slug)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := storage.ArchiveProject(r.Context(), s.deps.DB, project.ID); err != nil {
		slog.Error("admin: archive project", "err", err)
	}
	http.Redirect(w, r, "/admin/projects", http.StatusSeeOther)
}

// ---- Members --------------------------------------------------------------

func (s *adminServer) getMembers(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	project, err := storage.ProjectBySlug(r.Context(), s.deps.DB, slug)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	members, err := storage.ProjectMembers(r.Context(), s.deps.DB, project.ID)
	if err != nil {
		slog.Error("admin: project members", "err", err)
	}
	s.render(w, "members.html", map[string]interface{}{
		"Project": project,
		"Members": members,
		"Error":   r.URL.Query().Get("error"),
	})
}

func (s *adminServer) postMembers(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/admin/projects/"+slug+"/members?error=bad+request", http.StatusSeeOther)
		return
	}
	project, err := storage.ProjectBySlug(r.Context(), s.deps.DB, slug)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	userID, err := uuid.Parse(r.FormValue("user_id"))
	if err != nil {
		http.Redirect(w, r, "/admin/projects/"+slug+"/members?error=invalid+user+id", http.StatusSeeOther)
		return
	}
	role := r.FormValue("role")
	if role == "" {
		role = "editor"
	}
	if err := storage.AddProjectMember(r.Context(), s.deps.DB, project.ID, userID, role); err != nil {
		slog.Error("admin: add project member", "err", err)
		http.Redirect(w, r, "/admin/projects/"+slug+"/members?error=add+failed", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/projects/"+slug+"/members", http.StatusSeeOther)
}

func (s *adminServer) postMemberRemove(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	uid, err := uuid.Parse(r.PathValue("uid"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	project, err := storage.ProjectBySlug(r.Context(), s.deps.DB, slug)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := storage.RemoveProjectMember(r.Context(), s.deps.DB, project.ID, uid); err != nil {
		slog.Error("admin: remove member", "err", err)
	}
	// Broadcast access revoked to any active WebSocket clients.
	if s.deps.Hub != nil {
		s.deps.Hub.BroadcastAccessRevoked(project.ID.String(), uid.String())
	}
	http.Redirect(w, r, "/admin/projects/"+slug+"/members", http.StatusSeeOther)
}

func (s *adminServer) postMemberRole(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	uid, err := uuid.Parse(r.PathValue("uid"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/admin/projects/"+slug+"/members?error=bad+request", http.StatusSeeOther)
		return
	}
	project, err := storage.ProjectBySlug(r.Context(), s.deps.DB, slug)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	role := r.FormValue("role")
	if err := storage.ChangeProjectMemberRole(r.Context(), s.deps.DB, project.ID, uid, role); err != nil {
		slog.Error("admin: change member role", "err", err)
	}
	http.Redirect(w, r, "/admin/projects/"+slug+"/members", http.StatusSeeOther)
}

func (s *adminServer) getFiles(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	project, err := storage.ProjectBySlug(r.Context(), s.deps.DB, slug)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	files, err := storage.ProjectFiles(r.Context(), s.deps.DB, project.ID)
	if err != nil {
		slog.Error("admin: project files", "err", err)
	}
	s.render(w, "files.html", map[string]interface{}{
		"Project": project,
		"Files":   files,
	})
}

// ---- Audit ----------------------------------------------------------------

func (s *adminServer) getAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	pageStr := q.Get("page")
	page, _ := strconv.Atoi(pageStr)
	if page < 1 {
		page = 1
	}

	filter := storage.AuditFilter{
		UserID:    q.Get("user"),
		ProjectID: q.Get("project"),
		Action:    q.Get("action"),
		Page:      page,
		PageSize:  50,
	}

	rows, total, err := storage.AuditLog(r.Context(), s.deps.DB, filter)
	if err != nil {
		slog.Error("admin: audit log", "err", err)
	}

	totalPages := (total + filter.PageSize - 1) / filter.PageSize
	if totalPages < 1 {
		totalPages = 1
	}

	s.render(w, "audit.html", map[string]interface{}{
		"Rows":       rows,
		"Total":      total,
		"Page":       page,
		"TotalPages": totalPages,
		"Filter":     filter,
	})
}

// ---- Sessions -------------------------------------------------------------

func (s *adminServer) getSessions(w http.ResponseWriter, r *http.Request) {
	uid, ok := currentUserID(r.Context())
	if !ok {
		http.Redirect(w, r, "/admin/login", http.StatusFound)
		return
	}
	sessions, err := storage.UserSessions(r.Context(), s.deps.DB, uid)
	if err != nil {
		slog.Error("admin: user sessions", "err", err)
	}
	s.render(w, "sessions.html", map[string]interface{}{
		"Sessions": sessions,
	})
}

func (s *adminServer) postSessionRevoke(w http.ResponseWriter, r *http.Request) {
	sid, err := uuid.Parse(r.PathValue("sid"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := storage.RevokeSession(r.Context(), s.deps.DB, sid); err != nil {
		slog.Error("admin: revoke own session", "err", err)
	}
	http.Redirect(w, r, "/admin/sessions", http.StatusSeeOther)
}

// ---- Devices --------------------------------------------------------------

func (s *adminServer) getDevices(w http.ResponseWriter, r *http.Request) {
	uid, ok := currentUserID(r.Context())
	if !ok {
		http.Redirect(w, r, "/admin/login", http.StatusFound)
		return
	}
	passkeys, err := storage.UserPasskeys(r.Context(), s.deps.DB, uid)
	if err != nil {
		slog.Error("admin: user passkeys", "err", err)
	}
	s.render(w, "devices.html", map[string]interface{}{
		"Passkeys": passkeys,
		"Error":    r.URL.Query().Get("error"),
	})
}

func (s *adminServer) postDeviceRemove(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := storage.DeletePasskey(r.Context(), s.deps.DB, id); err != nil {
		slog.Error("admin: delete passkey", "err", err)
		http.Redirect(w, r, "/admin/devices?error=delete+failed", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/devices", http.StatusSeeOther)
}

// ---- Logout ---------------------------------------------------------------

func (s *adminServer) postLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("issued_session")
	if err == nil {
		// Look up the session ID and revoke it.
		sessionID, _, lookupErr := storage.SessionByToken(r.Context(), s.deps.DB, cookie.Value)
		if lookupErr == nil {
			_ = storage.RevokeSession(r.Context(), s.deps.DB, sessionID)
		}
	}
	clearSessionCookie(w)
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}
