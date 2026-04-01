package authz

import (
	"ai/pkg/storage"
	"context"
	"fmt"
	"strings"
)

type Scope string

const (
	ScopeOwn    Scope = "own"
	ScopeSystem Scope = "system"
	ScopeAll    Scope = "all"
)

type Rule struct {
	Resource string
	Scope    Scope
}

type CheckRequest struct {
	UserID        string
	Resource      string
	OwnerUserID   string
	SystemOwned   bool
	RequiredScope Scope
}

type Service struct {
	storage *storage.MySQLStorage
	rules   map[string][]Rule
}

func NewService(mysqlStorage *storage.MySQLStorage) *Service {
	return &Service{
		storage: mysqlStorage,
		rules:   defaultRules(),
	}
}

func (s *Service) GetUserRoles(ctx context.Context, userID string) ([]string, error) {
	if s == nil || s.storage == nil {
		return nil, fmt.Errorf("authz storage is unavailable")
	}
	roles, err := s.storage.ListUserRoles(ctx, strings.TrimSpace(userID))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(roles))
	for _, role := range roles {
		if role.RoleCode != "" {
			out = append(out, role.RoleCode)
		}
	}
	return out, nil
}

func (s *Service) HasAnyRole(ctx context.Context, userID string, roles ...string) (bool, error) {
	if len(roles) == 0 {
		return false, nil
	}
	currentRoles, err := s.GetUserRoles(ctx, userID)
	if err != nil {
		return false, err
	}
	if len(currentRoles) == 0 {
		return false, nil
	}
	set := make(map[string]struct{}, len(currentRoles))
	for _, role := range currentRoles {
		set[strings.ToLower(strings.TrimSpace(role))] = struct{}{}
	}
	for _, role := range roles {
		if _, ok := set[strings.ToLower(strings.TrimSpace(role))]; ok {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) CanAccess(ctx context.Context, req CheckRequest) (bool, error) {
	if s == nil || s.storage == nil {
		return false, fmt.Errorf("authz storage is unavailable")
	}
	userID := strings.TrimSpace(req.UserID)
	resource := strings.TrimSpace(req.Resource)
	if userID == "" || resource == "" {
		return false, nil
	}
	roles, err := s.GetUserRoles(ctx, userID)
	if err != nil {
		return false, err
	}
	if len(roles) == 0 {
		return false, nil
	}

	ownerUserID := strings.TrimSpace(req.OwnerUserID)
	for _, roleCode := range roles {
		roleRules := s.rules[strings.ToLower(strings.TrimSpace(roleCode))]
		for _, rule := range roleRules {
			if rule.Resource != "*" && rule.Resource != resource {
				continue
			}
			if !scopeSatisfied(rule.Scope, req.RequiredScope, userID, ownerUserID, req.SystemOwned) {
				continue
			}
			return true, nil
		}
	}
	return false, nil
}

func scopeSatisfied(granted Scope, required Scope, userID string, ownerUserID string, systemOwned bool) bool {
	if granted == ScopeAll {
		return true
	}
	if required == "" {
		required = ScopeOwn
	}
	switch required {
	case ScopeOwn:
		if granted == ScopeSystem {
			return systemOwned
		}
		if granted != ScopeOwn {
			return false
		}
		if ownerUserID == "" {
			return true
		}
		return ownerUserID == userID
	case ScopeSystem:
		return granted == ScopeSystem && systemOwned
	case ScopeAll:
		return granted == ScopeAll
	default:
		return false
	}
}

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
