// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package proxyhttp

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/alexedwards/scs/v2"
	"github.com/go-chi/chi/v5"

	"github.com/dcoric/git-proxy-go/internal/auth"
	"github.com/dcoric/git-proxy-go/internal/db"
)

// sessionKeyUsername is the session field holding the logged-in username (the
// scs equivalent of passport's serialized user).
const sessionKeyUsername = "username"

// authHandler serves /api/auth: the Go port of src/service/routes/auth.ts.
// Authentication uses the session (scs) rather than passport's req.user.
type authHandler struct {
	store    db.Store
	sessions *scs.SessionManager
	registry *auth.Registry
}

// publicUser is the non-sensitive user view returned to the UI (toPublicUser).
type publicUser struct {
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Email       string `json:"email"`
	Title       string `json:"title"`
	GitAccount  string `json:"gitAccount"`
	Admin       bool   `json:"admin"`
}

func toPublicUser(u *db.User) publicUser {
	deref := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	return publicUser{
		Username:    u.Username,
		DisplayName: deref(u.DisplayName),
		Email:       u.Email,
		Title:       deref(u.Title),
		GitAccount:  u.GitAccount,
		Admin:       u.Admin,
	}
}

// mount registers the auth routes on r.
func (h *authHandler) mount(r chi.Router) {
	r.Route("/api/auth", func(r chi.Router) {
		r.Get("/", h.index)
		r.Get("/config", h.config)
		r.Post("/login", h.login)
		r.Post("/logout", h.logout)
		r.Get("/profile", h.profile)
	})
}

func (h *authHandler) index(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"login":   map[string]string{"action": "post", "uri": "/api/auth/login"},
		"profile": map[string]string{"action": "get", "uri": "/api/auth/profile"},
		"logout":  map[string]string{"action": "post", "uri": "/api/auth/logout"},
	})
}

func (h *authHandler) config(w http.ResponseWriter, _ *http.Request) {
	loginType := h.registry.LoginType()
	var usernamePassword any
	if loginType != "" {
		usernamePassword = loginType
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"usernamePasswordMethod": usernamePassword,
		"otherMethods":           h.registry.OtherMethods(),
	})
}

func (h *authHandler) login(w http.ResponseWriter, r *http.Request) {
	strategy := h.registry.LoginStrategy()
	if strategy == nil {
		http.Error(w, "Username and Password based Login is not enabled at this time", http.StatusForbidden)
		return
	}

	var creds struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&creds); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	user, err := strategy.Authenticate(r.Context(), creds.Username, creds.Password)
	if err != nil {
		slog.Error("authentication error", "err", err)
		http.Error(w, "authentication error", http.StatusInternalServerError)
		return
	}
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Incorrect username or password."})
		return
	}

	// Establish the session (renew the token to prevent fixation).
	if err := h.sessions.RenewToken(r.Context()); err != nil {
		slog.Error("session renew failed", "err", err)
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	h.sessions.Put(r.Context(), sessionKeyUsername, user.Username)

	writeJSON(w, http.StatusOK, map[string]any{"message": "success", "user": toPublicUser(user)})
}

func (h *authHandler) logout(w http.ResponseWriter, r *http.Request) {
	if err := h.sessions.Destroy(r.Context()); err != nil {
		slog.Error("session destroy failed", "err", err)
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"isAuth": false})
}

func (h *authHandler) profile(w http.ResponseWriter, r *http.Request) {
	username := h.sessions.GetString(r.Context(), sessionKeyUsername)
	if username == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Not logged in"})
		return
	}
	user, err := h.store.FindUser(r.Context(), username)
	if err != nil {
		slog.Error("profile lookup failed", "err", err)
		http.Error(w, "lookup error", http.StatusInternalServerError)
		return
	}
	if user == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "User not found"})
		return
	}
	writeJSON(w, http.StatusOK, toPublicUser(user))
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
