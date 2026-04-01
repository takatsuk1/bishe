package tools

import (
	"context"
	"fmt"
)

type ToolType string

const (
	ToolTypeHTTP     ToolType = "http"
	ToolTypeMCP      ToolType = "mcp"
	ToolTypeFunction ToolType = "function"
)

type ParameterType string

const (
	ParamTypeString  ParameterType = "string"
	ParamTypeNumber  ParameterType = "number"
	ParamTypeBoolean ParameterType = "boolean"
	ParamTypeObject  ParameterType = "object"
	ParamTypeArray   ParameterType = "array"
)

type ToolParameter struct {
	Name        string        `json:"name"`
	Type        ParameterType `json:"type"`
	Required    bool          `json:"required"`
	Description string        `json:"description"`
	Default     any           `json:"default,omitempty"`
	Enum        []any         `json:"enum,omitempty"`
}

type ToolInfo struct {
	Name        string          `json:"name"`
	Type        ToolType        `json:"type"`
	Description string          `json:"description"`
	Parameters  []ToolParameter `json:"parameters"`
}

type Tool interface {
	Info() ToolInfo
	Execute(ctx context.Context, params map[string]any) (map[string]any, error)
}

type BaseTool struct {
	info ToolInfo
}

func NewBaseTool(name string, toolType ToolType, description string, parameters []ToolParameter) *BaseTool {
	return &BaseTool{
		info: ToolInfo{
			Name:        name,
			Type:        toolType,
			Description: description,
			Parameters:  parameters,
		},
	}
}

func (t *BaseTool) Info() ToolInfo {
	return t.info
}

func ValidateParameters(params map[string]any, paramDefs []ToolParameter) error {
	for _, def := range paramDefs {
		if !def.Required {
			continue
		}
		val, exists := params[def.Name]
		if !exists || val == nil {
			if def.Default != nil {
				continue
			}
			return fmt.Errorf("required parameter '%s' is missing", def.Name)
		}

		if def.Type == ParamTypeString {
			if normalized, ok := normalizeStringParameterValue(def.Name, val); ok {
				params[def.Name] = normalized
				val = normalized
			}
		}

		if err := validateParameterType(def.Name, val, def.Type); err != nil {
			return err
		}
	}
	return nil
}

func normalizeStringParameterValue(name string, value any) (string, bool) {
	if s, ok := value.(string); ok {
		return s, true
	}

	if b, ok := value.([]byte); ok {
		return string(b), true
	}

	if m, ok := value.(map[string]any); ok {
		if nested, exists := m[name]; exists {
			if s, ok := nested.(string); ok {
				return s, true
			}
		}
		if nested, exists := m["value"]; exists {
			if s, ok := nested.(string); ok {
				return s, true
			}
		}
	}

	return "", false
}

func validateParameterType(name string, value any, paramType ParameterType) error {
	switch paramType {
	case ParamTypeString:
		if _, ok := value.(string); !ok {
			return fmt.Errorf("parameter '%s' must be string (got %T)", name, value)
		}
	case ParamTypeNumber:
		switch value.(type) {
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		default:
			return fmt.Errorf("parameter '%s' must be number (got %T)", name, value)
		}
	case ParamTypeBoolean:
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("parameter '%s' must be boolean (got %T)", name, value)
		}
	case ParamTypeObject:
		if _, ok := value.(map[string]any); !ok {
			return fmt.Errorf("parameter '%s' must be object (got %T)", name, value)
		}
	case ParamTypeArray:
		if _, ok := value.([]any); !ok {
			return fmt.Errorf("parameter '%s' must be array (got %T)", name, value)
		}
	}
	return nil
}

func ApplyDefaults(params map[string]any, paramDefs []ToolParameter) map[string]any {
	result := make(map[string]any)
	for k, v := range params {
		result[k] = v
	}
	for _, def := range paramDefs {
		if _, exists := result[def.Name]; !exists && def.Default != nil {
			result[def.Name] = def.Default
		}
	}
	return result
}

type ExecutionResult struct {
	Success bool           `json:"success"`
	Data    map[string]any `json:"data,omitempty"`
	Error   string         `json:"error,omitempty"`
}

func SuccessResult(data map[string]any) *ExecutionResult {
	return &ExecutionResult{
		Success: true,
		Data:    data,
	}
}

func ErrorResult(err error) *ExecutionResult {
	return &ExecutionResult{
		Success: false,
		Error:   err.Error(),
	}
}
