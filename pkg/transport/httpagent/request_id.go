package httpagent

import "context"

type requestIDKey struct{}
type authTokenKey struct{}

// WithRequestID attaches a request ID to a context so downstream HTTP calls/logs can correlate.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if requestID == "" {
		return ctx
	}
	return context.WithValue(ctx, requestIDKey{}, requestID)
}

// RequestIDFromContext returns a request ID previously attached via WithRequestID.
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v := ctx.Value(requestIDKey{}); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// WithAuthorizationToken attaches an auth token so httpagent.Client can send bearer auth.
func WithAuthorizationToken(ctx context.Context, token string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if token == "" {
		return ctx
	}
	return context.WithValue(ctx, authTokenKey{}, token)
}

// AuthorizationTokenFromContext returns an auth token attached via WithAuthorizationToken.
func AuthorizationTokenFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v := ctx.Value(authTokenKey{}); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
