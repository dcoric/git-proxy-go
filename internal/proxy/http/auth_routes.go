// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package proxyhttp

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/alexedwards/scs/v2"
	"github.com/go-chi/chi/v5"

	"github.com/dcoric/git-proxy-go/internal/auth"
	"github.com/dcoric/git-proxy-go/internal/db"
)

// Session field names: the scs equivalents of passport's serialized user
// (username) and the OIDC anti-forgery state held between the start redirect
// and the callback.
const (
	sessionKeyUsername  = "username"
	sessionKeyOIDCState = "oidc_state"
)

// authHandler serves /api/auth: the Go port of src/service/routes/auth.ts.
// Authentication uses the session (scs) rather than passport's req.user.
type authHandler struct {
	store    db.Store
	sessions *scs.SessionManager
	registry *auth.Registry
	// oidcRedirectURL is where a successful OIDC login lands the browser
	// (the UI profile page); empty when OIDC is disabled.
	oidcRedirectURL string
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
		if h.registry.OIDC() != nil {
			r.Get("/openidconnect", h.oidcStart)
			r.Get("/openidconnect/callback", h.oidcCallback)
		}
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

// oidcStart begins the OIDC authorization-code flow: it mints a random state,
// stashes it in the session, and redirects the browser to the provider's
// authorization endpoint. Port of GET /openidconnect.
func (h *authHandler) oidcStart(w http.ResponseWriter, r *http.Request) {
	state, err := randomState()
	if err != nil {
		slog.Error("oidc state generation failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.sessions.Put(r.Context(), sessionKeyOIDCState, state)
	http.Redirect(w, r, h.registry.OIDC().AuthCodeURL(state), http.StatusFound)
}

// oidcCallback completes the flow: it verifies the state, exchanges the code for
// tokens, provisions/looks up the user, establishes the session and redirects to
// the UI profile page. Port of GET /openidconnect/callback.
func (h *authHandler) oidcCallback(w http.ResponseWriter, r *http.Request) {
	want := h.sessions.GetString(r.Context(), sessionKeyOIDCState)
	got := r.URL.Query().Get("state")
	if want == "" || got == "" || subtle.ConstantTimeCompare([]byte(want), []byte(got)) != 1 {
		http.Error(w, "invalid OIDC state", http.StatusBadRequest)
		return
	}
	h.sessions.Remove(r.Context(), sessionKeyOIDCState)

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing authorization code", http.StatusBadRequest)
		return
	}

	user, err := h.registry.OIDC().Exchange(r.Context(), code)
	if err != nil {
		slog.Error("oidc authentication error", "err", err)
		http.Error(w, "authentication error", http.StatusInternalServerError)
		return
	}
	if user == nil {
		http.Error(w, "no user found", http.StatusUnauthorized)
		return
	}

	// Establish the session (renew the token to prevent fixation).
	if err := h.sessions.RenewToken(r.Context()); err != nil {
		slog.Error("session renew failed", "err", err)
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	h.sessions.Put(r.Context(), sessionKeyUsername, user.Username)

	http.Redirect(w, r, h.oidcRedirectURL, http.StatusFound)
}

// randomState returns a 256-bit URL-safe random string for the OIDC state.
func randomState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
