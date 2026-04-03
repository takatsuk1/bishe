package auth

import (
	authsvc "ai/pkg/auth"
	"encoding/json"
	"net/http"
	"strings"
)

type API struct {
	svc *authsvc.Service
}

func NewAPI(svc *authsvc.Service) *API {
	return &API{svc: svc}
}

func (a *API) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v1/auth/register", a.handleRegister)
	mux.HandleFunc("/v1/auth/login", a.handleLogin)
	mux.HandleFunc("/v1/auth/refresh", a.handleRefresh)
	mux.HandleFunc("/v1/auth/logout", a.handleLogout)
	mux.HandleFunc("/v1/auth/me", a.handleMe)
	mux.HandleFunc("/v1/auth/profile", a.handleUpdateProfile)
	mux.HandleFunc("/v1/auth/change-password", a.handleChangePassword)
}

type authRequest struct {
	Username        string `json:"username"`
	Password        string `json:"password"`
	DisplayName     string `json:"displayName"`
	RefreshToken    string `json:"refreshToken"`
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

type authResponse struct {
	User        interface{}        `json:"user,omitempty"`
	Tokens      *authsvc.TokenPair `json:"tokens,omitempty"`
	Roles       []string           `json:"roles,omitempty"`
	PrimaryRole string             `json:"primaryRole,omitempty"`
	Error       string             `json:"error,omitempty"`
}

func (a *API) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if a.svc == nil {
		http.Error(w, "auth service unavailable", http.StatusServiceUnavailable)
		return
	}
	var req authRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	user, tokens, err := a.svc.Register(r.Context(), req.Username, req.Password, req.DisplayName)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, authResponse{Error: err.Error()})
		return
	}
	roles, err := a.svc.GetUserRoleCodes(r.Context(), user.UserID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, authResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, authResponse{User: user, Tokens: tokens, Roles: roles, PrimaryRole: pickPrimaryRole(roles)})
}

func (a *API) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if a.svc == nil {
		http.Error(w, "auth service unavailable", http.StatusServiceUnavailable)
		return
	}
	var req authRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	user, tokens, err := a.svc.Login(r.Context(), req.Username, req.Password)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, authResponse{Error: err.Error()})
		return
	}
	roles, err := a.svc.GetUserRoleCodes(r.Context(), user.UserID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, authResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, authResponse{User: user, Tokens: tokens, Roles: roles, PrimaryRole: pickPrimaryRole(roles)})
}

func (a *API) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if a.svc == nil {
		http.Error(w, "auth service unavailable", http.StatusServiceUnavailable)
		return
	}
	var req authRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	user, tokens, err := a.svc.Refresh(r.Context(), req.RefreshToken)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, authResponse{Error: err.Error()})
		return
	}
	roles, err := a.svc.GetUserRoleCodes(r.Context(), user.UserID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, authResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, authResponse{User: user, Tokens: tokens, Roles: roles, PrimaryRole: pickPrimaryRole(roles)})
}

func (a *API) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if a.svc == nil {
		http.Error(w, "auth service unavailable", http.StatusServiceUnavailable)
		return
	}
	token, err := bearerTokenFromHeader(r.Header.Get("Authorization"))
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, authResponse{Error: err.Error()})
		return
	}
	user, err := a.svc.AuthenticateAccessToken(r.Context(), token)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, authResponse{Error: "unauthorized"})
		return
	}
	if err := a.svc.Logout(r.Context(), user.UserID); err != nil {
		writeJSON(w, http.StatusInternalServerError, authResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) handleMe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if a.svc == nil {
		http.Error(w, "auth service unavailable", http.StatusServiceUnavailable)
		return
	}
	token, err := bearerTokenFromHeader(r.Header.Get("Authorization"))
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, authResponse{Error: err.Error()})
		return
	}
	user, err := a.svc.AuthenticateAccessToken(r.Context(), token)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, authResponse{Error: "unauthorized"})
		return
	}
	roles, err := a.svc.GetUserRoleCodes(r.Context(), user.UserID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, authResponse{Error: err.Error()})
		return
	}
	type meUser struct {
		UserID      string `json:"userId"`
		Username    string `json:"username"`
		DisplayName string `json:"displayName"`
	}
	writeJSON(w, http.StatusOK, authResponse{
		User: meUser{
			UserID:      user.UserID,
			Username:    user.Username,
			DisplayName: user.DisplayName,
		},
		Roles:       roles,
		PrimaryRole: pickPrimaryRole(roles),
	})
}

func pickPrimaryRole(roles []string) string {
	if len(roles) == 0 {
		return ""
	}
	priority := map[string]int{"admin": 1, "operator": 2, "user": 3, "viewer": 4}
	best := strings.ToLower(strings.TrimSpace(roles[0]))
	bestScore := 99
	for _, role := range roles {
		r := strings.ToLower(strings.TrimSpace(role))
		score, ok := priority[r]
		if !ok {
			score = 50
		}
		if score < bestScore {
			best = r
			bestScore = score
		}
	}
	return best
}

func (a *API) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if a.svc == nil {
		http.Error(w, "auth service unavailable", http.StatusServiceUnavailable)
		return
	}
	token, err := bearerTokenFromHeader(r.Header.Get("Authorization"))
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, authResponse{Error: err.Error()})
		return
	}
	user, err := a.svc.AuthenticateAccessToken(r.Context(), token)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, authResponse{Error: "unauthorized"})
		return
	}
	var req authRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	updated, err := a.svc.UpdateProfile(r.Context(), user.UserID, req.DisplayName)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, authResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, authResponse{User: updated})
}

func (a *API) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if a.svc == nil {
		http.Error(w, "auth service unavailable", http.StatusServiceUnavailable)
		return
	}
	token, err := bearerTokenFromHeader(r.Header.Get("Authorization"))
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, authResponse{Error: err.Error()})
		return
	}
	user, err := a.svc.AuthenticateAccessToken(r.Context(), token)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, authResponse{Error: "unauthorized"})
		return
	}
	var req authRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.svc.ChangePassword(r.Context(), user.UserID, req.CurrentPassword, req.NewPassword); err != nil {
		writeJSON(w, http.StatusBadRequest, authResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func bearerTokenFromHeader(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", http.ErrNoCookie
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(v, prefix) {
		return "", http.ErrNoCookie
	}
	token := strings.TrimSpace(strings.TrimPrefix(v, prefix))
	if token == "" {
		return "", http.ErrNoCookie
	}
	return token, nil
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
