package orchestrator

import (
	"ai/pkg/auth"
	"ai/pkg/authz"
	"ai/pkg/storage"
	"context"
	"net/http"
	"strings"
)

func authenticatedUserID(r *http.Request) (string, bool) {
	if r == nil {
		return "", false
	}
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user == nil {
		return "", false
	}
	if user.UserID == "" {
		return "", false
	}
	return user.UserID, true
}

func authorizeResourceAccess(r *http.Request, resource string, requiredScope authz.Scope, ownerUserID string, systemOwned bool) (string, bool) {
	userID, ok := authenticatedUserID(r)
	if !ok {
		return "", false
	}
	if strings.TrimSpace(ownerUserID) == "" && requiredScope == authz.ScopeOwn {
		ownerUserID = userID
	}
	mysqlStorage, err := storage.GetMySQLStorage()
	if err != nil {
		if requiredScope == authz.ScopeOwn {
			if strings.TrimSpace(ownerUserID) == "" || strings.TrimSpace(ownerUserID) == userID {
				return userID, true
			}
		}
		if requiredScope == authz.ScopeSystem && systemOwned {
			return userID, true
		}
		return "", false
	}
	authzService := authz.NewService(mysqlStorage)
	allowed, checkErr := authzService.CanAccess(r.Context(), authz.CheckRequest{
		UserID:        userID,
		Resource:      resource,
		OwnerUserID:   ownerUserID,
		SystemOwned:   systemOwned,
		RequiredScope: requiredScope,
	})
	if checkErr != nil || !allowed {
		return "", false
	}
	return userID, true
}

func authzServiceFromRequest(_ context.Context) (*authz.Service, error) {
	mysqlStorage, err := storage.GetMySQLStorage()
	if err != nil {
		return nil, err
	}
	return authz.NewService(mysqlStorage), nil
}

func hasAllScopeAccess(r *http.Request, resource string) bool {
	if r == nil {
		return false
	}
	userID, ok := authenticatedUserID(r)
	if !ok {
		return false
	}
	svc, err := authzServiceFromRequest(r.Context())
	if err != nil {
		return false
	}
	allowed, checkErr := svc.CanAccess(r.Context(), authz.CheckRequest{
		UserID:        userID,
		Resource:      resource,
		RequiredScope: authz.ScopeAll,
	})
	return checkErr == nil && allowed
}

func hasAnyRole(r *http.Request, roles ...string) bool {
	if r == nil {
		return false
	}
	userID, ok := authenticatedUserID(r)
	if !ok {
		return false
	}
	svc, err := authzServiceFromRequest(r.Context())
	if err != nil {
		return false
	}
	hit, checkErr := svc.HasAnyRole(r.Context(), userID, roles...)
	return checkErr == nil && hit
}
