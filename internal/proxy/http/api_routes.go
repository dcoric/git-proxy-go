// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package proxyhttp

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/go-chi/chi/v5"

	"github.com/dcoric/git-proxy-go/internal/auth"
	"github.com/dcoric/git-proxy-go/internal/config"
	"github.com/dcoric/git-proxy-go/internal/db"
)

// apiHandler serves the management API route groups: the Go port of
// src/service/routes (home, push, repo, users, config). It uses the session for
// the UI and, when enabled, the JWT principal for API clients — the equivalent
// of jwtAuthHandler + req.user.
type apiHandler struct {
	store        db.Store
	sessions     *scs.SessionManager
	cfg          *config.Config
	uiPort       string
	gitProxyPort string
}

// apiUser is the authenticated caller resolved from the session or JWT (the Go
// equivalent of req.user for the API routes).
type apiUser struct {
	Username string
	Admin    bool
}

func (h *apiHandler) mount(r chi.Router) {
	// home: GET /api -> the resource map.
	r.Get("/api", h.home)

	jwt := auth.JWTMiddleware(h.cfg, auth.JWTMiddlewareOptions{
		AlreadyAuthenticated: func(r *http.Request) bool {
			return h.sessions.GetString(r.Context(), sessionKeyUsername) != ""
		},
	})

	r.Route("/api/v1/push", func(r chi.Router) {
		r.Use(jwt)
		r.Get("/", h.listPushes)
		r.Get("/{id}", h.getPush)
		r.Post("/{id}/reject", h.rejectPush)
		r.Post("/{id}/authorise", h.authorisePush)
		r.Post("/{id}/cancel", h.cancelPush)
	})

	r.Route("/api/v1/repo", func(r chi.Router) {
		r.Use(jwt)
		r.Get("/", h.listRepos)
		r.Get("/{id}", h.getRepo)
		r.Patch("/{id}/user/push", h.addUserCanPush)
		r.Patch("/{id}/user/authorise", h.addUserCanAuthorise)
		r.Delete("/{id}/user/authorise/{username}", h.removeUserCanAuthorise)
		r.Delete("/{id}/user/push/{username}", h.removeUserCanPush)
		r.Delete("/{id}/delete", h.deleteRepo)
		r.Post("/", h.createRepo)
	})

	r.Route("/api/v1/user", func(r chi.Router) {
		r.Use(jwt)
		r.Get("/", h.listUsers)
		r.Get("/{id}", h.getUser)
	})

	r.Route("/api/v1/config", func(r chi.Router) {
		r.Get("/attestation", h.configAttestation)
		r.Get("/urlShortener", h.configURLShortener)
		r.Get("/contactEmail", h.configContactEmail)
		r.Get("/uiRouteAuth", h.configUIRouteAuth)
	})
}

// currentUser resolves the authenticated caller: the session user takes
// precedence (the UI), falling back to the JWT principal (API clients).
func (h *apiHandler) currentUser(r *http.Request) (*apiUser, bool) {
	if username := h.sessions.GetString(r.Context(), sessionKeyUsername); username != "" {
		if u, err := h.store.FindUser(r.Context(), username); err == nil && u != nil {
			return &apiUser{Username: u.Username, Admin: u.Admin}, true
		}
		return &apiUser{Username: username}, true
	}
	if p, ok := auth.PrincipalFromContext(r.Context()); ok {
		username, _ := p.Claims["username"].(string)
		return &apiUser{Username: username, Admin: p.Roles["admin"]}, true
	}
	return nil, false
}

// ---- home -----------------------------------------------------------------

func (h *apiHandler) home(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"healthcheck": "/api/v1/healthcheck",
		"push":        "/api/v1/push",
		"auth":        "/api/auth",
	})
}

// ---- push -----------------------------------------------------------------

func (h *apiHandler) listPushes(w http.ResponseWriter, r *http.Request) {
	q := db.PushQuery{Type: strPtr("push")}
	applyPushFilters(&q, r.URL.Query())
	pushes, err := h.store.GetPushes(r.Context(), q)
	if err != nil {
		slog.Error("get pushes failed", "err", err)
		http.Error(w, "lookup error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, pushes)
}

func (h *apiHandler) getPush(w http.ResponseWriter, r *http.Request) {
	push, err := h.store.GetPush(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "lookup error", http.StatusInternalServerError)
		return
	}
	if push == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "not found"})
		return
	}
	writeJSON(w, http.StatusOK, push)
}

func (h *apiHandler) rejectPush(w http.ResponseWriter, r *http.Request) {
	user, ok := h.currentUser(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Not logged in"})
		return
	}
	id := chi.URLParam(r, "id")

	var body struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if strings.TrimSpace(body.Reason) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "Rejection reason is required"})
		return
	}

	push, err := h.store.GetPush(r.Context(), id)
	if err != nil {
		http.Error(w, "lookup error", http.StatusInternalServerError)
		return
	}
	if push == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Push request not found"})
		return
	}
	if push.UserEmail == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "Push request has no user email"})
		return
	}

	committer, ok := h.committerFor(w, r, push.UserEmail)
	if !ok {
		return
	}
	if strings.EqualFold(committer.Username, user.Username) && !committer.Admin {
		writeJSON(w, http.StatusForbidden, map[string]string{"message": "Cannot reject your own changes"})
		return
	}

	allowed, err := h.canApproveReject(r, push, user.Username)
	if err != nil {
		http.Error(w, "lookup error", http.StatusInternalServerError)
		return
	}
	if !allowed {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"message": "User " + user.Username + " is not authorised to reject changes on this project",
		})
		return
	}

	reviewer, ok := h.reviewer(w, r, user.Username)
	if !ok {
		return
	}
	result, err := h.store.Reject(r.Context(), id, db.Rejection{
		Reason:    body.Reason,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Reviewer:  reviewer,
	})
	if err != nil {
		http.Error(w, "reject failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": result})
}

func (h *apiHandler) authorisePush(w http.ResponseWriter, r *http.Request) {
	user, ok := h.currentUser(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Not logged in"})
		return
	}

	var body struct {
		Params struct {
			Attestation []db.AttestationAnswer `json:"attestation"`
		} `json:"params"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	answers := body.Params.Attestation
	if !validateAttestation(answers, h.cfg) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "Attestation is not complete"})
		return
	}

	id := chi.URLParam(r, "id")
	push, err := h.store.GetPush(r.Context(), id)
	if err != nil {
		http.Error(w, "lookup error", http.StatusInternalServerError)
		return
	}
	if push == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Push request not found"})
		return
	}

	committer, ok := h.committerFor(w, r, push.UserEmail)
	if !ok {
		return
	}
	if strings.EqualFold(committer.Username, user.Username) && !committer.Admin {
		writeJSON(w, http.StatusForbidden, map[string]string{"message": "Cannot approve your own changes"})
		return
	}

	allowed, err := h.canApproveReject(r, push, user.Username)
	if err != nil {
		http.Error(w, "lookup error", http.StatusInternalServerError)
		return
	}
	if !allowed {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"message": "User " + user.Username + " not authorised to approve pushes on this project",
		})
		return
	}

	reviewer, ok := h.reviewer(w, r, user.Username)
	if !ok {
		return
	}
	result, err := h.store.Authorise(r.Context(), id, &db.Attestation{
		Answers:   answers,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Reviewer:  reviewer,
	})
	if err != nil {
		http.Error(w, "authorise failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": result})
}

func (h *apiHandler) cancelPush(w http.ResponseWriter, r *http.Request) {
	user, ok := h.currentUser(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Not logged in"})
		return
	}
	id := chi.URLParam(r, "id")

	allowed, err := h.canCancel(r, id, user.Username)
	if err != nil {
		http.Error(w, "lookup error", http.StatusInternalServerError)
		return
	}
	if !allowed {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"message": "User " + user.Username + " not authorised to cancel push requests on this project",
		})
		return
	}
	result, err := h.store.Cancel(r.Context(), id)
	if err != nil {
		http.Error(w, "cancel failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": result})
}

// committerFor looks up the push committer by email, writing the 404 response
// itself when no such user exists. Mirrors the getUsers({email}) lookup.
func (h *apiHandler) committerFor(w http.ResponseWriter, r *http.Request, email string) (*db.User, bool) {
	users, err := h.store.GetUsers(r.Context(), db.UserQuery{Email: &email})
	if err != nil {
		http.Error(w, "lookup error", http.StatusInternalServerError)
		return nil, false
	}
	if len(users) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"message": "No user found with the committer's email address: " + email,
		})
		return nil, false
	}
	return users[0], true
}

// reviewer resolves the reviewer's stored email, writing the 404 response when
// the reviewer has none. Mirrors the reviewer-email lookup in the Node routes.
func (h *apiHandler) reviewer(w http.ResponseWriter, r *http.Request, username string) (db.Reviewer, bool) {
	user, err := h.store.FindUser(r.Context(), username)
	if err != nil {
		http.Error(w, "lookup error", http.StatusInternalServerError)
		return db.Reviewer{}, false
	}
	if user == nil || user.Email == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"message": "There was no registered email address for the reviewer: " + username,
		})
		return db.Reviewer{}, false
	}
	return db.Reviewer{Username: username, Email: user.Email}, true
}

// canApproveReject reports whether the user may approve/reject the push: they
// must be on the repo's canAuthorise list. Port of canUserApproveRejectPush.
func (h *apiHandler) canApproveReject(r *http.Request, push *db.Push, username string) (bool, error) {
	repo, err := h.store.GetRepoByURL(r.Context(), push.URL)
	if err != nil || repo == nil {
		return false, err
	}
	return slices.Contains(repo.Users.CanAuthorise, username), nil
}

// canCancel reports whether the user may cancel the push: they must be on the
// repo's canPush or canAuthorise list. Port of canUserCancelPush +
// isUserPushAllowed (which lower-cases the username).
func (h *apiHandler) canCancel(r *http.Request, pushID, username string) (bool, error) {
	push, err := h.store.GetPush(r.Context(), pushID)
	if err != nil || push == nil {
		return false, err
	}
	repo, err := h.store.GetRepoByURL(r.Context(), push.URL)
	if err != nil || repo == nil {
		return false, err
	}
	u := strings.ToLower(username)
	return slices.Contains(repo.Users.CanPush, u) || slices.Contains(repo.Users.CanAuthorise, u), nil
}

// ---- repo -----------------------------------------------------------------

func (h *apiHandler) listRepos(w http.ResponseWriter, r *http.Request) {
	q := db.RepoQuery{}
	applyRepoFilters(&q, r.URL.Query())
	repos, err := h.store.GetRepos(r.Context(), q)
	if err != nil {
		http.Error(w, "lookup error", http.StatusInternalServerError)
		return
	}
	proxyURL := h.proxyURL(r)
	out := make([]map[string]any, 0, len(repos))
	for _, repo := range repos {
		out = append(out, repoWithProxyURL(repo, proxyURL))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *apiHandler) getRepo(w http.ResponseWriter, r *http.Request) {
	repo, err := h.store.GetRepoByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "lookup error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, repoWithProxyURL(repo, h.proxyURL(r)))
}

func (h *apiHandler) addUserCanPush(w http.ResponseWriter, r *http.Request) {
	h.repoUserMutation(w, r, bodyUsername(r), h.store.AddUserCanPush)
}

func (h *apiHandler) addUserCanAuthorise(w http.ResponseWriter, r *http.Request) {
	h.repoUserMutation(w, r, bodyUsername(r), h.store.AddUserCanAuthorise)
}

func (h *apiHandler) removeUserCanAuthorise(w http.ResponseWriter, r *http.Request) {
	h.repoUserMutation(w, r, chi.URLParam(r, "username"), h.store.RemoveUserCanAuthorise)
}

func (h *apiHandler) removeUserCanPush(w http.ResponseWriter, r *http.Request) {
	h.repoUserMutation(w, r, chi.URLParam(r, "username"), h.store.RemoveUserCanPush)
}

// repoUserMutation is the shared admin-guarded "user exists then mutate access
// list" flow behind the four repo user routes.
func (h *apiHandler) repoUserMutation(w http.ResponseWriter, r *http.Request, username string, mutate func(ctx context.Context, id, user string) error) {
	if !h.requireAdmin(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	user, err := h.store.FindUser(r.Context(), username)
	if err != nil {
		http.Error(w, "lookup error", http.StatusInternalServerError)
		return
	}
	if user == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "User does not exist"})
		return
	}
	if err := mutate(r.Context(), id, username); err != nil {
		http.Error(w, "update failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "created"})
}

func (h *apiHandler) deleteRepo(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	if err := h.store.DeleteRepo(r.Context(), chi.URLParam(r, "id")); err != nil {
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	// NOTE(parity): the Node route restarts the proxy when removing a repo drops
	// a proxied host. The Go proxy resolves its origins at startup; dynamic
	// reload is deferred to a follow-up (tracked with the repo-create path).
	writeJSON(w, http.StatusOK, map[string]string{"message": "deleted"})
}

func (h *apiHandler) createRepo(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	var in db.Repo
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if in.URL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "Repository url is required"})
		return
	}
	existing, err := h.store.GetRepoByURL(r.Context(), in.URL)
	if err != nil {
		http.Error(w, "lookup error", http.StatusInternalServerError)
		return
	}
	if existing != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"message": "Repository " + in.URL + " already exists!"})
		return
	}
	created, err := h.store.CreateRepo(r.Context(), &in)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": err.Error()})
		return
	}
	// NOTE(parity): dynamic proxy reload on a new origin is deferred (see deleteRepo).
	out := repoWithProxyURL(created, h.proxyURL(r))
	out["message"] = "created"
	writeJSON(w, http.StatusOK, out)
}

// requireAdmin enforces the admin check used by the mutating repo routes,
// writing the 401 response itself when the caller is not an admin (mirrors
// isAdminUser).
func (h *apiHandler) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	user, ok := h.currentUser(r)
	if !ok || !user.Admin {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "You are not authorised to perform this action."})
		return false
	}
	return true
}

// proxyURL builds the clone-via-proxy base URL for repo responses: the
// domains.proxy override when set, else the request's scheme+host with the UI
// port swapped for the git proxy port. Port of getProxyURL.
func (h *apiHandler) proxyURL(r *http.Request) string {
	if d := h.cfg.Domains; d != nil && d.Proxy != nil && *d.Proxy != "" {
		return *d.Proxy
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if xf := r.Header.Get("X-Forwarded-Proto"); xf != "" {
		scheme = xf
	}
	base := scheme + "://" + r.Host
	if h.uiPort != "" && h.gitProxyPort != "" {
		base = strings.Replace(base, ":"+h.uiPort, ":"+h.gitProxyPort, 1)
	}
	return base
}

// ---- users ----------------------------------------------------------------

func (h *apiHandler) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.store.GetUsers(r.Context(), db.UserQuery{})
	if err != nil {
		http.Error(w, "lookup error", http.StatusInternalServerError)
		return
	}
	out := make([]publicUser, 0, len(users))
	for _, u := range users {
		out = append(out, toPublicUser(u))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *apiHandler) getUser(w http.ResponseWriter, r *http.Request) {
	username := strings.ToLower(chi.URLParam(r, "id"))
	user, err := h.store.FindUser(r.Context(), username)
	if err != nil {
		http.Error(w, "lookup error", http.StatusInternalServerError)
		return
	}
	if user == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "User " + username + " not found"})
		return
	}
	writeJSON(w, http.StatusOK, toPublicUser(user))
}

// ---- config ---------------------------------------------------------------

func (h *apiHandler) configAttestation(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.cfg.AttestationConfig)
}

func (h *apiHandler) configUIRouteAuth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.cfg.UIRouteAuth)
}

func (h *apiHandler) configURLShortener(w http.ResponseWriter, _ *http.Request) {
	writeText(w, derefString(h.cfg.URLShortener))
}

func (h *apiHandler) configContactEmail(w http.ResponseWriter, _ *http.Request) {
	writeText(w, derefString(h.cfg.ContactEmail))
}

// ---- helpers --------------------------------------------------------------

// applyPushFilters maps the supported query params onto the typed PushQuery
// (type is fixed to "push"; limit/skip are ignored, as in the Node route).
func applyPushFilters(q *db.PushQuery, values map[string][]string) {
	for key := range values {
		switch key {
		case "limit", "skip", "type":
			continue
		case "error":
			q.Error = boolFilter(values[key][0])
		case "blocked":
			q.Blocked = boolFilter(values[key][0])
		case "allowPush":
			q.AllowPush = boolFilter(values[key][0])
		case "authorised":
			q.Authorised = boolFilter(values[key][0])
		case "canceled":
			q.Canceled = boolFilter(values[key][0])
		case "rejected":
			q.Rejected = boolFilter(values[key][0])
		}
	}
}

func applyRepoFilters(q *db.RepoQuery, values map[string][]string) {
	for key := range values {
		switch key {
		case "limit", "skip":
			continue
		case "name":
			q.Name = strPtr(values[key][0])
		case "url":
			q.URL = strPtr(values[key][0])
		case "project":
			q.Project = strPtr(values[key][0])
		}
	}
}

func boolFilter(v string) *bool {
	switch v {
	case "true":
		return boolPtr(true)
	case "false":
		return boolPtr(false)
	default:
		return nil
	}
}

// repoWithProxyURL renders a repo as a JSON object augmented with proxyURL,
// mirroring `{ ...repo, proxyURL }`.
func repoWithProxyURL(repo *db.Repo, proxyURL string) map[string]any {
	m := map[string]any{
		"name":     "",
		"project":  "",
		"url":      "",
		"users":    db.RepoUsers{CanPush: []string{}, CanAuthorise: []string{}},
		"proxyURL": proxyURL,
	}
	if repo != nil {
		if repo.ID != "" {
			m["_id"] = repo.ID
		}
		m["name"] = repo.Name
		m["project"] = repo.Project
		m["url"] = repo.URL
		m["users"] = repo.Users
	}
	return m
}

// validateAttestation reports whether every configured attestation question is
// answered and checked. Port of validateAttestation.
func validateAttestation(answers []db.AttestationAnswer, cfg *config.Config) bool {
	var questionCount int
	labels := map[string]bool{}
	if cfg != nil && cfg.AttestationConfig != nil {
		questionCount = len(cfg.AttestationConfig.Questions)
		for _, q := range cfg.AttestationConfig.Questions {
			labels[q.Label] = true
		}
	}
	if len(answers) != questionCount {
		return false
	}
	for _, a := range answers {
		if !labels[a.Label] || !a.Checked {
			return false
		}
	}
	return true
}

func bodyUsername(r *http.Request) string {
	var body struct {
		Username string `json:"username"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	return body.Username
}

func writeText(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }
func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
