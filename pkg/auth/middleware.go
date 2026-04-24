package auth

import (
	"ai/pkg/storage"
	"context"
	"fmt"
	"net/http"
	"strings"
)

// contextKey 上下文键类型
type contextKey string

// authUserContextKey 认证用户上下文键
const authUserContextKey contextKey = "auth_user"

// Middleware 创建认证中间件
// 参数:
//   svc - 认证服务实例
// 返回值:
//   HTTP中间件函数
func Middleware(svc *Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 检查认证服务是否可用
			if svc == nil {
				http.Error(w, "auth service unavailable", http.StatusServiceUnavailable)
				return
			}
			// 处理OPTIONS预检请求
			if r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}

			// 从请求头中提取Bearer令牌
			token, err := bearerTokenFromHeader(r.Header.Get("Authorization"))
			if err != nil {
				http.Error(w, err.Error(), http.StatusUnauthorized)
				return
			}
			// 验证访问令牌
			user, err := svc.AuthenticateAccessToken(r.Context(), token)
			if err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			// 将用户信息添加到上下文中
			ctx := context.WithValue(r.Context(), authUserContextKey, user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// UserFromContext 从上下文中获取用户信息
// 参数:
//   ctx - 上下文
// 返回值:
//   用户账户信息和是否成功获取
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

// bearerTokenFromHeader 从Authorization头中提取Bearer令牌
// 参数:
//   v - Authorization头的值
// 返回值:
//   Bearer令牌和错误信息
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
