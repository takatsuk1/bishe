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
	ErrInvalidCredentials = errors.New("invalid username or password") // 无效的用户名或密码
	ErrInvalidToken       = errors.New("invalid token")                // 无效的令牌
	ErrUnauthorized       = errors.New("unauthorized")                 // 未授权
)

// Service 认证服务
type Service struct {
	storage    *storage.MySQLStorage // MySQL存储
	secret     []byte                // JWT密钥
	accessTTL  time.Duration         // 访问令牌有效期
	refreshTTL time.Duration         // 刷新令牌有效期
}

// TokenPair 令牌对
type TokenPair struct {
	AccessToken  string `json:"accessToken"`  // 访问令牌
	RefreshToken string `json:"refreshToken"` // 刷新令牌
	ExpiresIn    int64  `json:"expiresIn"`    // 过期时间（秒）
}

// AccessClaims 访问令牌声明
type AccessClaims struct {
	UserID               string `json:"uid"`      // 用户ID
	Username             string `json:"username"` // 用户名
	jwt.RegisteredClaims        // JWT标准声明
}

// NewService 创建新的认证服务
// 参数:
//
//	mysqlStorage - MySQL存储实例
//	jwtSecret - JWT密钥
//	accessTTL - 访问令牌有效期
//	refreshTTL - 刷新令牌有效期
//
// 返回值:
//
//	认证服务实例和错误
func NewService(mysqlStorage *storage.MySQLStorage, jwtSecret string, accessTTL time.Duration, refreshTTL time.Duration) (*Service, error) {
	// 检查存储是否为空
	if mysqlStorage == nil {
		return nil, fmt.Errorf("auth storage is required")
	}
	// 设置默认JWT密钥
	secret := strings.TrimSpace(jwtSecret)
	if secret == "" {
		secret = "mmmanus-dev-secret-change-me"
	}
	// 设置默认有效期
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
	// 确保默认角色存在
	if err := svc.storage.EnsureDefaultRoles(context.Background()); err != nil {
		return nil, fmt.Errorf("ensure default roles: %w", err)
	}
	return svc, nil
}

// Register 用户注册
// 参数:
//
//	ctx - 上下文
//	username - 用户名
//	password - 密码
//	displayName - 显示名称
//
// 返回值:
//
//	用户账户信息、令牌对和错误
func (s *Service) Register(ctx context.Context, username string, password string, displayName string) (*storage.UserAccount, *TokenPair, error) {
	// 规范化输入
	username = strings.TrimSpace(strings.ToLower(username))
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		displayName = username
	}
	// 验证凭据
	if err := validateCredentials(username, password); err != nil {
		return nil, nil, err
	}

	// 检查用户名是否已存在
	if _, err := s.storage.GetUserByUsername(ctx, username); err == nil {
		return nil, nil, fmt.Errorf("username already exists")
	}

	// 生成密码哈希
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, nil, fmt.Errorf("hash password: %w", err)
	}

	// 创建用户账户
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
	// 绑定用户角色
	if err := s.storage.BindUserRole(ctx, user.UserID, "user"); err != nil {
		return nil, nil, err
	}

	// 获取持久化的用户信息
	persisted, err := s.storage.GetUserByUsername(ctx, username)
	if err != nil {
		return nil, nil, err
	}

	// 颁发令牌对
	tokens, err := s.issueTokenPair(ctx, persisted)
	if err != nil {
		return nil, nil, err
	}
	return sanitizeUser(persisted), tokens, nil
}

// Login 用户登录
// 参数:
//
//	ctx - 上下文
//	username - 用户名
//	password - 密码
//
// 返回值:
//
//	用户账户信息、令牌对和错误
func (s *Service) Login(ctx context.Context, username string, password string) (*storage.UserAccount, *TokenPair, error) {
	username = strings.TrimSpace(strings.ToLower(username))
	// 获取用户信息
	user, err := s.storage.GetUserByUsername(ctx, username)
	if err != nil {
		return nil, nil, ErrInvalidCredentials
	}
	// 检查用户状态
	if user.Status != 1 {
		return nil, nil, ErrUnauthorized
	}
	// 验证密码
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		return nil, nil, ErrInvalidCredentials
	}

	// 颁发令牌对
	tokens, err := s.issueTokenPair(ctx, user)
	if err != nil {
		return nil, nil, err
	}
	return sanitizeUser(user), tokens, nil
}

// Refresh 刷新访问令牌
// 参数:
//
//	ctx - 上下文
//	refreshToken - 刷新令牌
//
// 返回值:
//
//	用户账户信息、令牌对和错误
func (s *Service) Refresh(ctx context.Context, refreshToken string) (*storage.UserAccount, *TokenPair, error) {
	// 计算令牌哈希
	hash := hashToken(refreshToken)
	// 获取存储的刷新令牌
	stored, err := s.storage.GetRefreshToken(ctx, hash)
	if err != nil {
		return nil, nil, ErrInvalidToken
	}
	// 检查令牌是否有效
	if !s.storage.IsRefreshTokenActive(stored, time.Now().UTC()) {
		return nil, nil, ErrInvalidToken
	}
	// 撤销旧的刷新令牌
	if err := s.storage.RevokeRefreshTokenByHash(ctx, hash); err != nil {
		return nil, nil, err
	}

	// 获取用户信息
	user, err := s.storage.GetUserByUserID(ctx, stored.UserID)
	if err != nil {
		return nil, nil, ErrUnauthorized
	}
	if user.Status != 1 {
		return nil, nil, ErrUnauthorized
	}

	// 颁发新的令牌对
	tokens, err := s.issueTokenPair(ctx, user)
	if err != nil {
		return nil, nil, err
	}
	return sanitizeUser(user), tokens, nil
}

// AuthenticateAccessToken 验证访问令牌
// 参数:
//
//	ctx - 上下文
//	accessToken - 访问令牌
//
// 返回值:
//
//	用户账户信息和错误
func (s *Service) AuthenticateAccessToken(ctx context.Context, accessToken string) (*storage.UserAccount, error) {
	claims := &AccessClaims{}
	// 解析JWT令牌
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

	// 获取用户信息
	user, err := s.storage.GetUserByUserID(ctx, claims.UserID)
	if err != nil {
		return nil, ErrUnauthorized
	}
	if user.Status != 1 {
		return nil, ErrUnauthorized
	}
	return sanitizeUser(user), nil
}

// Logout 用户登出
// 参数:
//
//	ctx - 上下文
//	userID - 用户ID
//
// 返回值:
//
//	错误
func (s *Service) Logout(ctx context.Context, userID string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ErrUnauthorized
	}
	// 撤销用户的所有刷新令牌
	return s.storage.RevokeRefreshTokensByUserID(ctx, userID)
}

// UpdateProfile 更新用户资料
// 参数:
//
//	ctx - 上下文
//	userID - 用户ID
//	displayName - 显示名称
//
// 返回值:
//
//	更新后的用户账户信息和错误
func (s *Service) UpdateProfile(ctx context.Context, userID string, displayName string) (*storage.UserAccount, error) {
	userID = strings.TrimSpace(userID)
	displayName = strings.TrimSpace(displayName)
	if userID == "" {
		return nil, ErrUnauthorized
	}
	if displayName == "" {
		return nil, fmt.Errorf("display name is required")
	}
	// 更新用户显示名称
	if err := s.storage.UpdateUserDisplayName(ctx, userID, displayName); err != nil {
		return nil, err
	}
	// 获取更新后的用户信息
	user, err := s.storage.GetUserByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	return sanitizeUser(user), nil
}

// ChangePassword 修改密码
// 参数:
//
//	ctx - 上下文
//	userID - 用户ID
//	currentPassword - 当前密码
//	newPassword - 新密码
//
// 返回值:
//
//	错误
func (s *Service) ChangePassword(ctx context.Context, userID string, currentPassword string, newPassword string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ErrUnauthorized
	}
	if len(newPassword) < 6 {
		return fmt.Errorf("new password must be at least 6 characters")
	}
	// 获取用户信息
	user, err := s.storage.GetUserByUserID(ctx, userID)
	if err != nil {
		return ErrUnauthorized
	}
	// 验证当前密码
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(currentPassword)) != nil {
		return ErrInvalidCredentials
	}
	// 生成新密码哈希
	newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	// 更新密码
	if err := s.storage.UpdateUserPasswordHash(ctx, userID, string(newHash)); err != nil {
		return err
	}
	// 撤销所有刷新令牌
	return s.storage.RevokeRefreshTokensByUserID(ctx, userID)
}

// GetUserRoleCodes 获取用户的角色代码列表
// 参数:
//
//	ctx - 上下文
//	userID - 用户ID
//
// 返回值:
//
//	角色代码列表和错误
func (s *Service) GetUserRoleCodes(ctx context.Context, userID string) ([]string, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, ErrUnauthorized
	}
	// 获取用户角色列表
	roles, err := s.storage.ListUserRoles(ctx, userID)
	if err != nil {
		return nil, err
	}
	// 提取角色代码
	roleCodes := make([]string, 0, len(roles))
	for _, role := range roles {
		if role.RoleCode == "" {
			continue
		}
		roleCodes = append(roleCodes, role.RoleCode)
	}
	return roleCodes, nil
}

// issueTokenPair 颁发令牌对
// 参数:
//
//	ctx - 上下文
//	user - 用户账户信息
//
// 返回值:
//
//	令牌对和错误
func (s *Service) issueTokenPair(ctx context.Context, user *storage.UserAccount) (*TokenPair, error) {
	now := time.Now().UTC()
	// 创建访问令牌声明
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

	// 签名访问令牌
	jwtToken := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims)
	accessToken, err := jwtToken.SignedString(s.secret)
	if err != nil {
		return nil, fmt.Errorf("sign access token: %w", err)
	}

	// 生成刷新令牌
	rawRefreshToken := uuid.NewString() + uuid.NewString()
	refreshRecord := &storage.UserRefreshToken{
		TokenID:   uuid.NewString(),
		UserID:    user.UserID,
		TokenHash: hashToken(rawRefreshToken),
		ExpiresAt: now.Add(s.refreshTTL),
	}
	// 保存刷新令牌
	if err := s.storage.SaveRefreshToken(ctx, refreshRecord); err != nil {
		return nil, err
	}

	return &TokenPair{
		AccessToken:  accessToken,
		RefreshToken: rawRefreshToken,
		ExpiresIn:    int64(s.accessTTL.Seconds()),
	}, nil
}

// validateCredentials 验证用户凭据
// 参数:
//
//	username - 用户名
//	password - 密码
//
// 返回值:
//
//	错误
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

// hashToken 计算令牌的SHA256哈希值
// 参数:
//
//	v - 令牌字符串
//
// 返回值:
//
//	哈希值
func hashToken(v string) string {
	h := sha256.Sum256([]byte(strings.TrimSpace(v)))
	return hex.EncodeToString(h[:])
}

// sanitizeUser 清理用户信息，移除敏感字段
// 参数:
//
//	user - 用户账户信息
//
// 返回值:
//
//	清理后的用户账户信息
func sanitizeUser(user *storage.UserAccount) *storage.UserAccount {
	if user == nil {
		return nil
	}
	out := *user
	out.PasswordHash = ""
	return &out
}
