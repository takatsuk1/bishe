package cmd

import (
	adminapi "ai/api/admin"
	authapi "ai/api/auth"
	monitorapi "ai/api/monitor"
	"ai/api/orchestrator"
	"ai/config"
	"ai/pkg/auth"
	"ai/pkg/logger"
	"ai/pkg/monitor"
	"ai/pkg/storage"
	"net/http"
	"time"
)

// buildPublicServicesHandler wires non-agent shared APIs (orchestrator, auth).
func buildPublicServicesHandler(mysqlStorage *storage.MySQLStorage) http.Handler {
	mux := http.NewServeMux()

	orchestratorAPI := orchestrator.NewOrchestratorAPI()
	var monitorAPI *monitorapi.API
	var authMiddleware func(http.Handler) http.Handler
	if mysqlStorage != nil {
		orchestratorAPI = orchestrator.NewOrchestratorAPIWithStorage(mysqlStorage)
		monitorAPI = monitorapi.NewAPI(monitor.NewService(mysqlStorage, nil))
		cfg := config.GetMainConfig()
		authService, err := auth.NewService(
			mysqlStorage,
			cfg.Auth.JWTSecret,
			time.Duration(cfg.Auth.AccessTokenTTLMinutes)*time.Minute,
			time.Duration(cfg.Auth.RefreshTokenTTLHours)*time.Hour,
		)
		if err != nil {
			logger.Warnf("init auth service failed: %v", err)
		} else {
			authMiddleware = auth.Middleware(authService)
			authMux := http.NewServeMux()
			authapi.NewAPI(authService).RegisterRoutes(authMux)
			mux.Handle("/v1/auth/", withCORS(authMux))
		}
	}

	orchHandler := orchestratorAPI.Handler()
	mux.Handle("/v1/orchestrator/", withCORS(orchHandler))
	if monitorAPI != nil {
		monitorHandler := monitorAPI.Handler()
		mux.Handle("/v1/monitor/", withCORS(monitorHandler))
	}
	if authMiddleware != nil {
		protected := withCORS(authMiddleware(orchHandler))
		adminMux := http.NewServeMux()
		adminapi.NewAPI(mysqlStorage).RegisterRoutes(adminMux)
		adminProtected := withCORS(authMiddleware(adminMux))
		mux.Handle("/v1/admin/users", adminProtected)
		mux.Handle("/v1/admin/users/", adminProtected)
		mux.Handle("/v1/orchestrator/agents", protected)
		mux.Handle("/v1/orchestrator/agent-workflows", protected)
		mux.Handle("/v1/orchestrator/agent-workflows/", protected)
		mux.Handle("/v1/orchestrator/tools", protected)
		mux.Handle("/v1/orchestrator/workflows", protected)
		mux.Handle("/v1/orchestrator/workflows/", protected)
		mux.Handle("/v1/orchestrator/runs/", protected)
		mux.Handle("/v1/orchestrator/user-workflows", protected)
		mux.Handle("/v1/orchestrator/user-workflows/", protected)
		mux.Handle("/v1/orchestrator/user-tools", protected)
		mux.Handle("/v1/orchestrator/user-tools/", protected)
		mux.Handle("/v1/orchestrator/user-agents", protected)
		mux.Handle("/v1/orchestrator/user-agents/", protected)
		if monitorAPI != nil {
			monitorProtected := withCORS(authMiddleware(monitorAPI.Handler()))
			mux.Handle("/v1/monitor/overview", monitorProtected)
			mux.Handle("/v1/monitor/runs", monitorProtected)
			mux.Handle("/v1/monitor/runs/", monitorProtected)
			mux.Handle("/v1/monitor/alerts", monitorProtected)
			mux.Handle("/v1/monitor/alerts/", monitorProtected)
		}
	}

	return mux
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Expose-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
