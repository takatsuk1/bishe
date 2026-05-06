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

	"github.com/gin-gonic/gin"
)

// buildPublicServicesHandler wires non-agent shared APIs (orchestrator, auth).
func buildPublicServicesHandler(mysqlStorage *storage.MySQLStorage) http.Handler {
	router := gin.New()
	router.Use(ginCORS())

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
			authapi.NewAPI(authService).RegisterRoutes(router)
		}
	}

	orchHandler := orchestratorAPI.Handler()
	if authMiddleware != nil {
		orchHandler = authMiddleware(orchHandler)
	}
	router.Any("/v1/orchestrator", gin.WrapH(orchHandler))
	router.Any("/v1/orchestrator/*path", gin.WrapH(orchHandler))

	if monitorAPI != nil {
		monitorHandler := monitorAPI.Handler()
		if authMiddleware != nil {
			monitorHandler = authMiddleware(monitorHandler)
		}
		router.Any("/v1/monitor", gin.WrapH(monitorHandler))
		router.Any("/v1/monitor/*path", gin.WrapH(monitorHandler))
	}

	if authMiddleware != nil {
		adminRouter := gin.New()
		adminapi.NewAPI(mysqlStorage).RegisterRoutes(adminRouter)
		adminProtected := authMiddleware(adminRouter)
		router.Any("/v1/admin", gin.WrapH(adminProtected))
		router.Any("/v1/admin/*path", gin.WrapH(adminProtected))
	}

	return router
}

func ginCORS() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")
		c.Header("Access-Control-Expose-Headers", "Content-Type")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
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
