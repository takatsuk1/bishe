package auth

import (
	"ai/pkg/storage"
	"context"
	"fmt"
	"net/http"
	"strings"
)

type contextKey string

const authUserContextKey contextKey = "auth_user"

func Middleware(svc *Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if svc == nil {
				http.Error(w, "auth service unavailable", http.StatusServiceUnavailable)
				return
			}
			if r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}

			token, err := bearerTokenFromHeader(r.Header.Get("Authorization"))
			if err != nil {
				http.Error(w, err.Error(), http.StatusUnauthorized)
				return
			}
			user, err := svc.AuthenticateAccessToken(r.Context(), token)
			if err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			ctx := context.WithValue(r.Context(), authUserContextKey, user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func UserFromContext(ctx context.Context) (*storage.UserAccount, bool) {
	v := ctx.Value(authUserContextKey)
	if v == nil {
		return nil, false
	}
	user, ok := v.(*storage.UserAccount)
	if !ok {
		return nil, false
	}
	return user, true
}

func bearerTokenFromHeader(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", fmt.Errorf("missing authorization header")
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(v, prefix) {
		return "", fmt.Errorf("invalid authorization header")
	}
	token := strings.TrimSpace(strings.TrimPrefix(v, prefix))
	if token == "" {
		return "", fmt.Errorf("missing bearer token")
	}
	return token, nil
}
