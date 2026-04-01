package monitor

import (
	"ai/pkg/auth"
	"ai/pkg/authz"
	"ai/pkg/logger"
	"ai/pkg/monitor"
	"ai/pkg/storage"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

type API struct {
	service *monitor.Service
}

func NewAPI(service *monitor.Service) *API {
	return &API{service: service}
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/monitor/", a.handleRoot)
	mux.HandleFunc("/v1/monitor/overview", a.handleOverview)
	mux.HandleFunc("/v1/monitor/runs", a.handleRuns)
	mux.HandleFunc("/v1/monitor/runs/", a.handleRunChildren)
	mux.HandleFunc("/v1/monitor/alerts", a.handleAlerts)
	mux.HandleFunc("/v1/monitor/alerts/", a.handleAlertAction)
	return mux
}

func (a *API) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/monitor/" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("monitor API"))
}

func (a *API) handleOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	userID, ok := authorizedMonitorUserID(r)
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if a.service == nil {
		http.Error(w, "monitor service unavailable", http.StatusServiceUnavailable)
		return
	}
	overview, err := a.service.BuildOverview(r.Context(), userID, 10)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, overview)
}

func (a *API) handleRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	userID, ok := authorizedMonitorUserID(r)
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if a.service == nil {
		http.Error(w, "monitor service unavailable", http.StatusServiceUnavailable)
		return
	}
	page, pageSize := parsePage(r)
	runs, total, err := a.service.ListRuns(r.Context(), monitor.ListRunsInput{
		UserID:     userID,
		WorkflowID: strings.TrimSpace(r.URL.Query().Get("workflowId")),
		TaskID:     strings.TrimSpace(r.URL.Query().Get("taskId")),
		Status:     strings.TrimSpace(r.URL.Query().Get("status")),
		Page:       page,
		PageSize:   pageSize,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"page":     page,
		"pageSize": pageSize,
		"total":    total,
		"items":    runs,
	})
}

func (a *API) handleRunChildren(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	userID, ok := authorizedMonitorUserID(r)
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if a.service == nil {
		http.Error(w, "monitor service unavailable", http.StatusServiceUnavailable)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/v1/monitor/runs/")
	parts := strings.Split(path, "/")
	runID := strings.TrimSpace(parts[0])
	if runID == "" {
		http.Error(w, "run id is required", http.StatusBadRequest)
		return
	}

	if len(parts) == 1 {
		detail, err := a.service.GetRunDetail(r.Context(), runID, userID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, detail)
		return
	}

	if parts[1] == "family" {
		limit := 20
		if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
			if parsed, err := strconv.Atoi(v); err == nil {
				limit = parsed
			}
		}
		family, err := a.service.GetRunFamily(r.Context(), runID, userID, limit)
		if err != nil {
			logger.Errorf("[TRACE] monitor.family failed run=%s user=%s err=%v", runID, userID, err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, family)
		return
	}

	if parts[1] != "events" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if _, err := a.service.GetRunDetail(r.Context(), runID, userID); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	page, pageSize := parsePage(r)
	events, total, err := a.service.ListRunEvents(r.Context(), monitor.ListRunEventsInput{
		RunID:    runID,
		Page:     page,
		PageSize: pageSize,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"page":     page,
		"pageSize": pageSize,
		"total":    total,
		"items":    events,
	})
}

func (a *API) handleAlerts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	userID, ok := authorizedMonitorUserID(r)
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if a.service == nil {
		http.Error(w, "monitor service unavailable", http.StatusServiceUnavailable)
		return
	}
	page, pageSize := parsePage(r)
	alerts, total, err := a.service.ListAlerts(r.Context(), monitor.ListAlertsInput{
		UserID:     userID,
		RunID:      strings.TrimSpace(r.URL.Query().Get("runId")),
		WorkflowID: strings.TrimSpace(r.URL.Query().Get("workflowId")),
		Status:     strings.TrimSpace(r.URL.Query().Get("status")),
		Page:       page,
		PageSize:   pageSize,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"page":     page,
		"pageSize": pageSize,
		"total":    total,
		"items":    alerts,
	})
}

func (a *API) handleAlertAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if a.service == nil {
		http.Error(w, "monitor service unavailable", http.StatusServiceUnavailable)
		return
	}
	if !canManageAlerts(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1/monitor/alerts/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 {
		http.Error(w, "invalid alert action path", http.StatusBadRequest)
		return
	}
	alertID := strings.TrimSpace(parts[0])
	action := strings.TrimSpace(parts[1])
	if alertID == "" {
		http.Error(w, "alert id is required", http.StatusBadRequest)
		return
	}
	var err error
	switch action {
	case "ack":
		err = a.service.AcknowledgeAlert(r.Context(), alertID)
	case "resolve":
		err = a.service.ResolveAlert(r.Context(), alertID)
	default:
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "alertId": alertID, "action": action})
}

func parsePage(r *http.Request) (int, int) {
	if r == nil {
		return 1, 20
	}
	page := 1
	pageSize := 20
	if v := strings.TrimSpace(r.URL.Query().Get("page")); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			page = parsed
		}
	}
	if v := strings.TrimSpace(r.URL.Query().Get("pageSize")); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			pageSize = parsed
		}
	}
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return page, pageSize
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func authenticatedUserID(r *http.Request) (string, bool) {
	if r == nil {
		return "", false
	}
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user == nil || strings.TrimSpace(user.UserID) == "" {
		return "", false
	}
	return user.UserID, true
}

func authorizedMonitorUserID(r *http.Request) (string, bool) {
	userID, ok := authenticatedUserID(r)
	if !ok {
		return "", false
	}
	mysqlStorage, err := storage.GetMySQLStorage()
	if err != nil {
		return userID, true
	}
	authzService := authz.NewService(mysqlStorage)
	allowedOwn, ownErr := authzService.CanAccess(r.Context(), authz.CheckRequest{
		UserID:        userID,
		Resource:      "monitor.read",
		OwnerUserID:   userID,
		RequiredScope: authz.ScopeOwn,
	})
	if ownErr != nil || !allowedOwn {
		return "", false
	}
	allowedAll, checkErr := authzService.CanAccess(r.Context(), authz.CheckRequest{
		UserID:        userID,
		Resource:      "monitor.read",
		RequiredScope: authz.ScopeAll,
	})
	if checkErr != nil {
		return userID, true
	}
	if allowedAll {
		return "", true
	}
	return userID, true
}

func canManageAlerts(r *http.Request) bool {
	userID, ok := authenticatedUserID(r)
	if !ok {
		return false
	}
	mysqlStorage, err := storage.GetMySQLStorage()
	if err != nil {
		return false
	}
	authzService := authz.NewService(mysqlStorage)
	allowed, checkErr := authzService.CanAccess(r.Context(), authz.CheckRequest{
		UserID:        userID,
		Resource:      "monitor.alert.manage",
		RequiredScope: authz.ScopeAll,
	})
	return checkErr == nil && allowed
}
