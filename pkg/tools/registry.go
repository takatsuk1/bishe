package tools

import (
	"context"
	"fmt"
	"sync"
)

type ToolRegistry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools: make(map[string]Tool),
	}
}

func (r *ToolRegistry) Register(tool Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	info := tool.Info()
	name := info.Name
	if name == "" {
		return fmt.Errorf("tool name is empty")
	}

	if _, exists := r.tools[name]; exists {
		return fmt.Errorf("tool '%s' already registered", name)
	}

	r.tools[name] = tool
	return nil
}

func (r *ToolRegistry) Unregister(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.tools[name]; !exists {
		return fmt.Errorf("tool '%s' not found", name)
	}

	delete(r.tools, name)
	return nil
}

func (r *ToolRegistry) Get(name string) (Tool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tool, exists := r.tools[name]
	if !exists {
		return nil, fmt.Errorf("tool '%s' not found", name)
	}

	return tool, nil
}

func (r *ToolRegistry) List() []ToolInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]ToolInfo, 0, len(r.tools))
	for _, tool := range r.tools {
		result = append(result, tool.Info())
	}

	return result
}

func (r *ToolRegistry) ListByType(toolType ToolType) []ToolInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]ToolInfo, 0)
	for _, tool := range r.tools {
		info := tool.Info()
		if info.Type == toolType {
			result = append(result, info)
		}
	}

	return result
}

func (r *ToolRegistry) Exists(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	_, exists := r.tools[name]
	return exists
}

func (r *ToolRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return len(r.tools)
}

func (r *ToolRegistry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.tools = make(map[string]Tool)
}

func (r *ToolRegistry) Execute(ctx context.Context, name string, params map[string]any) (map[string]any, error) {
	tool, err := r.Get(name)
	if err != nil {
		return nil, err
	}

	return tool.Execute(ctx, params)
}

type GlobalRegistry struct {
	registry *ToolRegistry
}

var globalRegistry = &GlobalRegistry{
	registry: NewToolRegistry(),
}

func GetGlobalRegistry() *ToolRegistry {
	return globalRegistry.registry
}

func RegisterTool(tool Tool) error {
	return globalRegistry.registry.Register(tool)
}

func UnregisterTool(name string) error {
	return globalRegistry.registry.Unregister(name)
}

func GetTool(name string) (Tool, error) {
	return globalRegistry.registry.Get(name)
}

func ListTools() []ToolInfo {
	return globalRegistry.registry.List()
}

func ExecuteTool(ctx context.Context, name string, params map[string]any) (map[string]any, error) {
	return globalRegistry.registry.Execute(ctx, name, params)
}

type ToolBuilder struct {
	name        string
	toolType    ToolType
	description string
	parameters  []ToolParameter
	config      any
}

func NewToolBuilder(name string) *ToolBuilder {
	return &ToolBuilder{
		name:       name,
		parameters: make([]ToolParameter, 0),
	}
}

func (b *ToolBuilder) Type(toolType ToolType) *ToolBuilder {
	b.toolType = toolType
	return b
}

func (b *ToolBuilder) Description(desc string) *ToolBuilder {
	b.description = desc
	return b
}

func (b *ToolBuilder) AddParameter(name string, paramType ParameterType, required bool, description string) *ToolBuilder {
	b.parameters = append(b.parameters, ToolParameter{
		Name:        name,
		Type:        paramType,
		Required:    required,
		Description: description,
	})
	return b
}

func (b *ToolBuilder) AddParameterWithDefault(name string, paramType ParameterType, description string, defaultValue any) *ToolBuilder {
	b.parameters = append(b.parameters, ToolParameter{
		Name:        name,
		Type:        paramType,
		Required:    false,
		Description: description,
		Default:     defaultValue,
	})
	return b
}

func (b *ToolBuilder) HTTPConfig(config HTTPToolConfig) *ToolBuilder {
	b.toolType = ToolTypeHTTP
	b.config = config
	return b
}

func (b *ToolBuilder) MCPConfig(config MCPToolConfig) *ToolBuilder {
	b.toolType = ToolTypeMCP
	b.config = config
	return b
}

func (b *ToolBuilder) Build() (Tool, error) {
	switch b.toolType {
	case ToolTypeHTTP:
		config, ok := b.config.(HTTPToolConfig)
		if !ok {
			config = HTTPToolConfig{}
		}
		return NewHTTPTool(b.name, b.description, b.parameters, config), nil
	case ToolTypeMCP:
		config, ok := b.config.(MCPToolConfig)
		if !ok {
			config = MCPToolConfig{}
		}
		return NewMCPTool(b.name, b.description, b.parameters, config), nil
	default:
		return nil, fmt.Errorf("unsupported tool type: %s", b.toolType)
	}
}
