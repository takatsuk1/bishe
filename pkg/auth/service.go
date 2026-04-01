package auth

import (
	"ai/pkg/storage"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrInvalidToken       = errors.New("invalid token")
	ErrUnauthorized       = errors.New("unauthorized")
)

type Service struct {
	storage    *storage.MySQLStorage
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
}

type TokenPair struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    int64  `json:"expiresIn"`
}

type AccessClaims struct {
	UserID   string `json:"uid"`
	Username string `json:"username"`
	jwt.RegisteredClaims
}

func NewService(mysqlStorage *storage.MySQLStorage, jwtSecret string, accessTTL time.Duration, refreshTTL time.Duration) (*Service, error) {
	if mysqlStorage == nil {
		return nil, fmt.Errorf("auth storage is required")
	}
	secret := strings.TrimSpace(jwtSecret)
	if secret == "" {
		secret = "mmmanus-dev-secret-change-me"
	}
	if accessTTL <= 0 {
		accessTTL = 30 * time.Minute
	}
	if refreshTTL <= 0 {
		refreshTTL = 7 * 24 * time.Hour
	}
	svc := &Service{
		storage:    mysqlStorage,
		secret:     []byte(secret),
		accessTTL:  accessTTL,
		refreshTTL: refreshTTL,
	}
	if err := svc.storage.EnsureDefaultRoles(context.Background()); err != nil {
		return nil, fmt.Errorf("ensure default roles: %w", err)
	}
	return svc, nil
}

func (s *Service) Register(ctx context.Context, username string, password string, displayName string) (*storage.UserAccount, *TokenPair, error) {
	username = strings.TrimSpace(strings.ToLower(username))
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		displayName = username
	}
	if err := validateCredentials(username, password); err != nil {
		return nil, nil, err
	}

	if _, err := s.storage.GetUserByUsername(ctx, username); err == nil {
		return nil, nil, fmt.Errorf("username already exists")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, nil, fmt.Errorf("hash password: %w", err)
	}

	user := &storage.UserAccount{
		UserID:       "usr_" + uuid.NewString(),
		Username:     username,
		DisplayName:  displayName,
		PasswordHash: string(hash),
		Status:       1,
	}
	if err := s.storage.CreateUser(ctx, user); err != nil {
		return nil, nil, err
	}
	if err := s.storage.BindUserRole(ctx, user.UserID, "user"); err != nil {
		return nil, nil, err
	}

	persisted, err := s.storage.GetUserByUsername(ctx, username)
	if err != nil {
		return nil, nil, err
	}

	tokens, err := s.issueTokenPair(ctx, persisted)
	if err != nil {
		return nil, nil, err
	}
	return sanitizeUser(persisted), tokens, nil
}

func (s *Service) Login(ctx context.Context, username string, password string) (*storage.UserAccount, *TokenPair, error) {
	username = strings.TrimSpace(strings.ToLower(username))
	user, err := s.storage.GetUserByUsername(ctx, username)
	if err != nil {
		return nil, nil, ErrInvalidCredentials
	}
	if user.Status != 1 {
		return nil, nil, ErrUnauthorized
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		return nil, nil, ErrInvalidCredentials
	}

	tokens, err := s.issueTokenPair(ctx, user)
	if err != nil {
		return nil, nil, err
	}
	return sanitizeUser(user), tokens, nil
}

func (s *Service) Refresh(ctx context.Context, refreshToken string) (*storage.UserAccount, *TokenPair, error) {
	hash := hashToken(refreshToken)
	stored, err := s.storage.GetRefreshToken(ctx, hash)
	if err != nil {
		return nil, nil, ErrInvalidToken
	}
	if !s.storage.IsRefreshTokenActive(stored, time.Now().UTC()) {
		return nil, nil, ErrInvalidToken
	}
	if err := s.storage.RevokeRefreshTokenByHash(ctx, hash); err != nil {
		return nil, nil, err
	}

	user, err := s.storage.GetUserByUserID(ctx, stored.UserID)
	if err != nil {
		return nil, nil, ErrUnauthorized
	}
	if user.Status != 1 {
		return nil, nil, ErrUnauthorized
	}

	tokens, err := s.issueTokenPair(ctx, user)
	if err != nil {
		return nil, nil, err
	}
	return sanitizeUser(user), tokens, nil
}

func (s *Service) AuthenticateAccessToken(ctx context.Context, accessToken string) (*storage.UserAccount, error) {
	claims := &AccessClaims{}
	tkn, err := jwt.ParseWithClaims(accessToken, claims, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return s.secret, nil
	})
	if err != nil || !tkn.Valid {
		return nil, ErrInvalidToken
	}
	if claims.UserID == "" {
		return nil, ErrInvalidToken
	}

	user, err := s.storage.GetUserByUserID(ctx, claims.UserID)
	if err != nil {
		return nil, ErrUnauthorized
	}
	if user.Status != 1 {
		return nil, ErrUnauthorized
	}
	return sanitizeUser(user), nil
}

func (s *Service) Logout(ctx context.Context, userID string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ErrUnauthorized
	}
	return s.storage.RevokeRefreshTokensByUserID(ctx, userID)
}

func (s *Service) UpdateProfile(ctx context.Context, userID string, displayName string) (*storage.UserAccount, error) {
	userID = strings.TrimSpace(userID)
	displayName = strings.TrimSpace(displayName)
	if userID == "" {
		return nil, ErrUnauthorized
	}
	if displayName == "" {
		return nil, fmt.Errorf("display name is required")
	}
	if err := s.storage.UpdateUserDisplayName(ctx, userID, displayName); err != nil {
		return nil, err
	}
	user, err := s.storage.GetUserByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	return sanitizeUser(user), nil
}

func (s *Service) ChangePassword(ctx context.Context, userID string, currentPassword string, newPassword string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ErrUnauthorized
	}
	if len(newPassword) < 6 {
		return fmt.Errorf("new password must be at least 6 characters")
	}
	user, err := s.storage.GetUserByUserID(ctx, userID)
	if err != nil {
		return ErrUnauthorized
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(currentPassword)) != nil {
		return ErrInvalidCredentials
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	if err := s.storage.UpdateUserPasswordHash(ctx, userID, string(newHash)); err != nil {
		return err
	}
	return s.storage.RevokeRefreshTokensByUserID(ctx, userID)
}

func (s *Service) GetUserRoleCodes(ctx context.Context, userID string) ([]string, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, ErrUnauthorized
	}
	roles, err := s.storage.ListUserRoles(ctx, userID)
	if err != nil {
		return nil, err
	}
	roleCodes := make([]string, 0, len(roles))
	for _, role := range roles {
		if role.RoleCode == "" {
			continue
		}
		roleCodes = append(roleCodes, role.RoleCode)
	}
	return roleCodes, nil
}

func (s *Service) issueTokenPair(ctx context.Context, user *storage.UserAccount) (*TokenPair, error) {
	now := time.Now().UTC()
	accessClaims := AccessClaims{
		UserID:   user.UserID,
		Username: user.Username,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   user.UserID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.accessTTL)),
			ID:        uuid.NewString(),
		},
	}

	jwtToken := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims)
	accessToken, err := jwtToken.SignedString(s.secret)
	if err != nil {
		return nil, fmt.Errorf("sign access token: %w", err)
	}

	rawRefreshToken := uuid.NewString() + uuid.NewString()
	refreshRecord := &storage.UserRefreshToken{
		TokenID:   uuid.NewString(),
		UserID:    user.UserID,
		TokenHash: hashToken(rawRefreshToken),
		ExpiresAt: now.Add(s.refreshTTL),
	}
	if err := s.storage.SaveRefreshToken(ctx, refreshRecord); err != nil {
		return nil, err
	}

	return &TokenPair{
		AccessToken:  accessToken,
		RefreshToken: rawRefreshToken,
		ExpiresIn:    int64(s.accessTTL.Seconds()),
	}, nil
}

func validateCredentials(username string, password string) error {
	if username == "" {
		return fmt.Errorf("username is required")
	}
	if len(username) < 3 || len(username) > 64 {
		return fmt.Errorf("username must be 3-64 characters")
	}
	if len(password) < 6 {
		return fmt.Errorf("password must be at least 6 characters")
	}
	return nil
}

func hashToken(v string) string {
	h := sha256.Sum256([]byte(strings.TrimSpace(v)))
	return hex.EncodeToString(h[:])
}

func sanitizeUser(user *storage.UserAccount) *storage.UserAccount {
	if user == nil {
		return nil
	}
	out := *user
	out.PasswordHash = ""
	return &out
}
