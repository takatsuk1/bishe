package admin

import (
	"ai/pkg/auth"
	"ai/pkg/authz"
	"ai/pkg/storage"
	"encoding/json"
	"net/http"
	"strings"
)

type API struct {
	storage *storage.MySQLStorage
}

func NewAPI(mysqlStorage *storage.MySQLStorage) *API {
	return &API{storage: mysqlStorage}
}

func (a *API) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v1/admin/users", a.handleUsers)
	mux.HandleFunc("/v1/admin/users/", a.handleUserByID)
}

type adminUserItem struct {
	UserID      string   `json:"userId"`
	Username    string   `json:"username"`
	DisplayName string   `json:"displayName"`
	Status      int8     `json:"status"`
	Roles       []string `json:"roles"`
	PrimaryRole string   `json:"primaryRole"`
}

type updateUserStatusRequest struct {
	Status  *int8 `json:"status,omitempty"`
	Enabled *bool `json:"enabled,omitempty"`
}

type updateUserRolesRequest struct {
	Roles []string `json:"roles"`
}

func (a *API) handleUsers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !a.ensureAdmin(w, r) {
		return
	}
	if a.storage == nil {
		http.Error(w, "storage not available", http.StatusServiceUnavailable)
		return
	}

	users, err := a.storage.ListUsers(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	items := make([]adminUserItem, 0, len(users))
	for _, u := range users {
		roles, roleErr := a.storage.ListUserRoles(r.Context(), u.UserID)
		if roleErr != nil {
			http.Error(w, roleErr.Error(), http.StatusInternalServerError)
			return
		}
		roleCodes := extractRoleCodes(roles)
		items = append(items, adminUserItem{
			UserID:      u.UserID,
			Username:    u.Username,
			DisplayName: u.DisplayName,
			Status:      u.Status,
			Roles:       roleCodes,
			PrimaryRole: pickPrimaryRole(roleCodes),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) handleUserByID(w http.ResponseWriter, r *http.Request) {
	if !a.ensureAdmin(w, r) {
		return
	}
	if a.storage == nil {
		http.Error(w, "storage not available", http.StatusServiceUnavailable)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/v1/admin/users/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}
	userID := strings.TrimSpace(parts[0])

	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		a.handleGetUser(w, r, userID)
		return
	}

	action := strings.TrimSpace(parts[1])
	switch action {
	case "status":
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		a.handleUpdateUserStatus(w, r, userID)
	case "roles":
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		a.handleUpdateUserRoles(w, r, userID)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (a *API) handleGetUser(w http.ResponseWriter, r *http.Request, userID string) {
	user, err := a.storage.GetUserByUserID(r.Context(), userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	roles, err := a.storage.ListUserRoles(r.Context(), userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	roleCodes := extractRoleCodes(roles)
	writeJSON(w, http.StatusOK, adminUserItem{
		UserID:      user.UserID,
		Username:    user.Username,
		DisplayName: user.DisplayName,
		Status:      user.Status,
		Roles:       roleCodes,
		PrimaryRole: pickPrimaryRole(roleCodes),
	})
}

func (a *API) handleUpdateUserStatus(w http.ResponseWriter, r *http.Request, userID string) {
	var req updateUserStatusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	status := int8(1)
	if req.Status != nil {
		status = *req.Status
	} else if req.Enabled != nil {
		if *req.Enabled {
			status = 1
		} else {
			status = 0
		}
	} else {
		http.Error(w, "status or enabled is required", http.StatusBadRequest)
		return
	}

	if err := a.storage.UpdateUserStatus(r.Context(), userID, status); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "userId": userID, "status": status})
}

func (a *API) handleUpdateUserRoles(w http.ResponseWriter, r *http.Request, userID string) {
	var req updateUserRolesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.Roles) == 0 {
		http.Error(w, "roles is required", http.StatusBadRequest)
		return
	}

	if err := a.storage.ReplaceUserRoles(r.Context(), userID, req.Roles); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	roles, err := a.storage.ListUserRoles(r.Context(), userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	roleCodes := extractRoleCodes(roles)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"userId":      userID,
		"roles":       roleCodes,
		"primaryRole": pickPrimaryRole(roleCodes),
	})
}

func (a *API) ensureAdmin(w http.ResponseWriter, r *http.Request) bool {
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user == nil || strings.TrimSpace(user.UserID) == "" {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return false
	}
	authzSvc := authz.NewService(a.storage)
	allowed, err := authzSvc.HasAnyRole(r.Context(), user.UserID, "admin")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return false
	}
	if !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	return true
}

func extractRoleCodes(roles []storage.Role) []string {
	out := make([]string, 0, len(roles))
	for _, role := range roles {
		code := strings.TrimSpace(role.RoleCode)
		if code != "" {
			out = append(out, code)
		}
	}
	return out
}

func pickPrimaryRole(roleCodes []string) string {
	if len(roleCodes) == 0 {
		return ""
	}
	priority := map[string]int{"admin": 1, "operator": 2, "user": 3, "viewer": 4}
	best := roleCodes[0]
	bestScore := 99
	for _, role := range roleCodes {
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

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
