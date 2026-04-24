package authz

import (
	"ai/pkg/storage"
	"context"
	"fmt"
	"strings"
)

// Scope 权限范围类型
type Scope string

const (
	ScopeOwn    Scope = "own"    // 自己的资源
	ScopeSystem Scope = "system" // 系统资源
	ScopeAll    Scope = "all"    // 所有资源
)

// Rule 权限规则
type Rule struct {
	Resource string // 资源标识
	Scope    Scope  // 权限范围
}

// CheckRequest 权限检查请求
type CheckRequest struct {
	UserID        string // 用户ID
	Resource      string // 资源标识
	OwnerUserID   string // 资源所有者ID
	SystemOwned   bool   // 是否为系统资源
	RequiredScope Scope  // 所需权限范围
}

// Service 授权服务
type Service struct {
	storage *storage.MySQLStorage // MySQL存储
	rules   map[string][]Rule     // 权限规则映射
}

// NewService 创建新的授权服务
// 参数:
//
//	mysqlStorage - MySQL存储实例
//
// 返回值:
//
//	授权服务实例
func NewService(mysqlStorage *storage.MySQLStorage) *Service {
	return &Service{
		storage: mysqlStorage,
		rules:   defaultRules(),
	}
}

// GetUserRoles 获取用户的角色列表
// 参数:
//
//	ctx - 上下文
//	userID - 用户ID
//
// 返回值:
//
//	角色代码列表和错误
func (s *Service) GetUserRoles(ctx context.Context, userID string) ([]string, error) {
	if s == nil || s.storage == nil {
		return nil, fmt.Errorf("authz storage is unavailable")
	}
	// 获取用户角色
	roles, err := s.storage.ListUserRoles(ctx, strings.TrimSpace(userID))
	if err != nil {
		return nil, err
	}
	// 提取角色代码
	out := make([]string, 0, len(roles))
	for _, role := range roles {
		if role.RoleCode != "" {
			out = append(out, role.RoleCode)
		}
	}
	return out, nil
}

// HasAnyRole 检查用户是否拥有任意指定角色
// 参数:
//
//	ctx - 上下文
//	userID - 用户ID
//	roles - 要检查的角色列表
//
// 返回值:
//
//	是否拥有任意角色和错误
func (s *Service) HasAnyRole(ctx context.Context, userID string, roles ...string) (bool, error) {
	if len(roles) == 0 {
		return false, nil
	}
	// 获取用户当前角色
	currentRoles, err := s.GetUserRoles(ctx, userID)
	if err != nil {
		return false, err
	}
	if len(currentRoles) == 0 {
		return false, nil
	}
	// 构建角色集合
	set := make(map[string]struct{}, len(currentRoles))
	for _, role := range currentRoles {
		set[strings.ToLower(strings.TrimSpace(role))] = struct{}{}
	}
	// 检查是否拥有任意指定角色
	for _, role := range roles {
		if _, ok := set[strings.ToLower(strings.TrimSpace(role))]; ok {
			return true, nil
		}
	}
	return false, nil
}

// CanAccess 检查用户是否有权限访问指定资源
// 参数:
//
//	ctx - 上下文
//	req - 权限检查请求
//
// 返回值:
//
//	是否有权限和错误
func (s *Service) CanAccess(ctx context.Context, req CheckRequest) (bool, error) {
	if s == nil || s.storage == nil {
		return false, fmt.Errorf("authz storage is unavailable")
	}
	// 规范化输入
	userID := strings.TrimSpace(req.UserID)
	resource := strings.TrimSpace(req.Resource)
	if userID == "" || resource == "" {
		return false, nil
	}
	// 获取用户角色
	roles, err := s.GetUserRoles(ctx, userID)
	if err != nil {
		return false, err
	}
	if len(roles) == 0 {
		return false, nil
	}

	// 检查每个角色的权限规则
	ownerUserID := strings.TrimSpace(req.OwnerUserID)
	for _, roleCode := range roles {
		roleRules := s.rules[strings.ToLower(strings.TrimSpace(roleCode))]
		for _, rule := range roleRules {
			// 检查资源是否匹配
			if rule.Resource != "*" && rule.Resource != resource {
				continue
			}
			// 检查权限范围是否满足
			if !scopeSatisfied(rule.Scope, req.RequiredScope, userID, ownerUserID, req.SystemOwned) {
				continue
			}
			return true, nil
		}
	}
	return false, nil
}

// scopeSatisfied 检查权限范围是否满足要求
// 参数:
//
//	granted - 已授予的权限范围
//	required - 所需的权限范围
//	userID - 用户ID
//	ownerUserID - 资源所有者ID
//	systemOwned - 是否为系统资源
//
// 返回值:
//
//	是否满足权限要求
func scopeSatisfied(granted Scope, required Scope, userID string, ownerUserID string, systemOwned bool) bool {
	// 如果授予的权限范围是所有资源，直接返回true
	if granted == ScopeAll {
		return true
	}
	// 默认所需权限范围为自己的资源
	if required == "" {
		required = ScopeOwn
	}
	// 根据所需权限范围进行检查
	switch required {
	case ScopeOwn:
		// 如果授予的是系统权限，检查是否为系统资源
		if granted == ScopeSystem {
			return systemOwned
		}
		// 检查是否授予了自有资源权限
		if granted != ScopeOwn {
			return false
		}
		// 如果没有指定所有者，则允许访问
		if ownerUserID == "" {
			return true
		}
		// 检查是否为资源所有者
		return ownerUserID == userID
	case ScopeSystem:
		// 系统资源需要系统权限
		return granted == ScopeSystem && systemOwned
	case ScopeAll:
		// 所有资源需要所有权限
		return granted == ScopeAll
	default:
		return false
	}
}

// defaultRules 返回默认的权限规则
// 返回值:
//
//	权限规则映射
func defaultRules() map[string][]Rule {
	return map[string][]Rule{
		"viewer": {
			{Resource: "orchestrator.workflow.read", Scope: ScopeOwn},
			{Resource: "orchestrator.tool.read", Scope: ScopeOwn},
			{Resource: "orchestrator.tool.read", Scope: ScopeSystem},
			{Resource: "orchestrator.agent.read", Scope: ScopeOwn},
			{Resource: "orchestrator.agent.read", Scope: ScopeSystem},
			{Resource: "monitor.read", Scope: ScopeOwn},
			{Resource: "orchestrator.workflow", Scope: ScopeOwn},
			{Resource: "orchestrator.tool", Scope: ScopeOwn},
			{Resource: "orchestrator.agent", Scope: ScopeOwn},
			{Resource: "monitor.run", Scope: ScopeOwn},
		},
		"user": {
			{Resource: "orchestrator.workflow.read", Scope: ScopeOwn},
			{Resource: "orchestrator.workflow.manage", Scope: ScopeOwn},
			{Resource: "orchestrator.tool.read", Scope: ScopeOwn},
			{Resource: "orchestrator.tool.read", Scope: ScopeSystem},
			{Resource: "orchestrator.tool.manage", Scope: ScopeOwn},
			{Resource: "orchestrator.agent.read", Scope: ScopeOwn},
			{Resource: "orchestrator.agent.read", Scope: ScopeSystem},
			{Resource: "orchestrator.agent.manage", Scope: ScopeOwn},
			{Resource: "monitor.read", Scope: ScopeOwn},
			{Resource: "orchestrator.workflow", Scope: ScopeOwn},
			{Resource: "orchestrator.tool", Scope: ScopeOwn},
			{Resource: "orchestrator.agent", Scope: ScopeOwn},
			{Resource: "monitor.run", Scope: ScopeOwn},
		},
		"operator": {
			{Resource: "orchestrator.workflow.read", Scope: ScopeAll},
			{Resource: "orchestrator.workflow.manage", Scope: ScopeOwn},
			{Resource: "orchestrator.tool.read", Scope: ScopeAll},
			{Resource: "orchestrator.tool.manage", Scope: ScopeOwn},
			{Resource: "orchestrator.agent.read", Scope: ScopeAll},
			{Resource: "orchestrator.agent.manage", Scope: ScopeOwn},
			{Resource: "orchestrator.agent.ops", Scope: ScopeAll},
			{Resource: "monitor.read", Scope: ScopeAll},
			{Resource: "monitor.alert.manage", Scope: ScopeAll},
			{Resource: "orchestrator.workflow", Scope: ScopeOwn},
			{Resource: "orchestrator.tool", Scope: ScopeSystem},
			{Resource: "orchestrator.agent", Scope: ScopeSystem},
			{Resource: "monitor.run", Scope: ScopeAll},
		},
		"admin": {
			{Resource: "orchestrator.tool.system.manage", Scope: ScopeAll},
			{Resource: "*", Scope: ScopeAll},
		},
	}
}
