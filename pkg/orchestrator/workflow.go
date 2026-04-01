package orchestrator

import (
	"errors"
	"fmt"
)

// 错误定义
var (
	// ErrWorkflowIDEmpty 表示工作流ID为空
	ErrWorkflowIDEmpty = errors.New("workflow id is empty")
	// ErrNodeIDEmpty 表示节点ID为空
	ErrNodeIDEmpty = errors.New("node id is empty")
	// ErrNodeAlreadyExists 表示节点已存在
	ErrNodeAlreadyExists = errors.New("node already exists")
	// ErrNodeNotFound 表示节点未找到
	ErrNodeNotFound = errors.New("node not found")
	// ErrDuplicateEdge 表示边重复
	ErrDuplicateEdge = errors.New("duplicate edge")
	// ErrStartNodeNotDefined 表示未定义开始节点
	ErrStartNodeNotDefined = errors.New("start node not defined")
	// ErrWorkflowHasCycle 表示工作流存在循环
	ErrWorkflowHasCycle = errors.New("workflow has cycle")
	// ErrLoopNodeMissingRoute 表示循环节点缺少路由定义
	ErrLoopNodeMissingRoute = errors.New("loop node must define continue and exit nodes")
	// ErrWorkflowHasUncontrolledCycle 表示存在未经过 loop 节点的环
	ErrWorkflowHasUncontrolledCycle = errors.New("workflow has uncontrolled cycle")
)

// NodeType 表示节点类型
type NodeType string

// 节点类型常量定义
const (
	// NodeTypeStart 表示开始节点
	NodeTypeStart NodeType = "start"
	// NodeTypeEnd 表示结束节点
	NodeTypeEnd NodeType = "end"
	// NodeTypeCondition 表示条件节点
	NodeTypeCondition NodeType = "condition"
	// NodeTypeLoop 表示循环节点
	NodeTypeLoop NodeType = "loop"
	// NodeTypeChatModel 表示聊天模型节点
	NodeTypeChatModel NodeType = "chat_model"
	// NodeTypeTool 表示工具节点
	NodeTypeTool NodeType = "tool"
	// NodeTypePreInput 表示预处理输入节点
	NodeTypePreInput NodeType = "pre_input"
)

// Workflow 表示一个工作流
type Workflow struct {
	// ID 工作流唯一标识符
	ID string
	// Name 工作流名称
	Name string
	// Description 工作流描述
	Description string
	// SchemaVersion 模式版本
	SchemaVersion int
	// StartNodeID 开始节点ID
	StartNodeID string
	// Nodes 节点映射
	Nodes map[string]Node
	// Edges 边列表
	Edges []Edge
}

// Port 表示节点的端口
type Port struct {
	// Name 端口名称
	Name string
	// Type 端口类型
	Type string
	// Description 端口描述
	Description string
}

// Node 表示工作流中的一个节点
type Node struct {
	// ID 节点唯一标识符
	ID string
	// Type 节点类型
	Type NodeType
	// Config 节点配置
	Config map[string]any
	// AgentID 代理ID
	AgentID string
	// TaskType 任务类型
	TaskType string
	// InputPorts 输入端口
	InputPorts []Port
	// OutputPorts 输出端口
	OutputPorts []Port
	// InputMapping 输入映射
	InputMapping map[string]string
	// OutputMapping 输出映射
	OutputMapping map[string]string
	// SchemaVersion 模式版本
	SchemaVersion int
	// InputMap 输入映射
	InputMap map[string]string
	// OutputMap 输出映射
	OutputMap map[string]string
	// Condition 条件表达式
	Condition string
	// PreInput 节点预处理输入模板
	PreInput string
	// LoopConfig 循环配置
	LoopConfig *LoopConfig
	// Metadata 元数据
	Metadata map[string]string
}

// LoopConfig 表示循环节点的配置
type LoopConfig struct {
	// MaxIterations 最大迭代次数
	MaxIterations int
	// ContinueTo 继续执行的节点ID
	ContinueTo string
	// ExitTo 退出循环的节点ID
	ExitTo string
}

// Edge 表示工作流中的一条边
type Edge struct {
	// From 源节点ID
	From string
	// To 目标节点ID
	To string
	// Label 边标签
	Label string
	// Mapping 数据映射
	Mapping map[string]string
}

// NewWorkflow 创建一个新的工作流
// id: 工作流ID
// name: 工作流名称
// 返回创建的工作流和可能的错误
func NewWorkflow(id, name string) (*Workflow, error) {
	// 检查工作流ID是否为空
	if id == "" {
		return nil, ErrWorkflowIDEmpty
	}
	// 创建并返回工作流
	return &Workflow{
		ID:    id,
		Name:  name,
		Nodes: make(map[string]Node),
		Edges: make([]Edge, 0),
	}, nil
}

// AddNode 向工作流添加节点
// node: 要添加的节点
// 返回可能的错误
func (w *Workflow) AddNode(node Node) error {
	// 检查节点ID是否为空
	if node.ID == "" {
		return ErrNodeIDEmpty
	}
	// 检查节点是否已存在
	if _, exists := w.Nodes[node.ID]; exists {
		return fmt.Errorf("%w: %s", ErrNodeAlreadyExists, node.ID)
	}
	// 检查循环节点是否有完整的路由配置
	if node.Type == NodeTypeLoop {
		if node.LoopConfig == nil || node.LoopConfig.ContinueTo == "" || node.LoopConfig.ExitTo == "" {
			return ErrLoopNodeMissingRoute
		}
	}
	// 克隆节点的各种映射和配置
	node.InputMap = cloneStringMap(node.InputMap)
	node.OutputMap = cloneStringMap(node.OutputMap)
	node.InputMapping = cloneStringMap(node.InputMapping)
	node.OutputMapping = cloneStringMap(node.OutputMapping)
	node.Config = cloneAnyMap(node.Config)
	node.InputPorts = clonePorts(node.InputPorts)
	node.OutputPorts = clonePorts(node.OutputPorts)
	node.Metadata = cloneStringMap(node.Metadata)
	// 添加节点到工作流
	w.Nodes[node.ID] = node
	// 如果还没有设置开始节点，则将当前节点设置为开始节点
	if w.StartNodeID == "" {
		w.StartNodeID = node.ID
	}
	return nil
}

// AddEdge 向工作流添加边
// from: 源节点ID
// to: 目标节点ID
// 返回可能的错误
func (w *Workflow) AddEdge(from, to string) error {
	// 调用带标签的添加边方法，使用空标签和空映射
	return w.AddEdgeWithLabel(from, to, "", nil)
}

// AddEdgeWithLabel 向工作流添加带标签的边
// from: 源节点ID
// to: 目标节点ID
// label: 边标签
// mapping: 数据映射
// 返回可能的错误
func (w *Workflow) AddEdgeWithLabel(from, to, label string, mapping map[string]string) error {
	// 检查源节点和目标节点ID是否为空
	if from == "" || to == "" {
		return ErrNodeIDEmpty
	}
	// 检查源节点是否存在
	if _, ok := w.Nodes[from]; !ok {
		return fmt.Errorf("%w: %s", ErrNodeNotFound, from)
	}
	// 检查目标节点是否存在
	if _, ok := w.Nodes[to]; !ok {
		return fmt.Errorf("%w: %s", ErrNodeNotFound, to)
	}
	// 检查边是否已存在
	for _, e := range w.Edges {
		if e.From == from && e.To == to {
			return fmt.Errorf("%w: %s->%s", ErrDuplicateEdge, from, to)
		}
	}
	// 添加边到工作流
	w.Edges = append(w.Edges, Edge{From: from, To: to, Label: label, Mapping: cloneStringMap(mapping)})
	return nil
}

// Validate 执行工作流编译前的结构检查
// 返回可能的错误
func (w *Workflow) Validate() error {
	// 检查是否定义了开始节点
	if w.StartNodeID == "" {
		return ErrStartNodeNotDefined
	}
	// 检查开始节点是否存在
	if _, ok := w.Nodes[w.StartNodeID]; !ok {
		return fmt.Errorf("%w: %s", ErrNodeNotFound, w.StartNodeID)
	}
	// 检查所有循环节点的路由配置
	for _, node := range w.Nodes {
		if node.Type != NodeTypeLoop || node.LoopConfig == nil {
			continue
		}
		// 检查继续执行的节点是否存在
		if _, ok := w.Nodes[node.LoopConfig.ContinueTo]; !ok {
			return fmt.Errorf("%w: %s", ErrNodeNotFound, node.LoopConfig.ContinueTo)
		}
		// 检查退出循环的节点是否存在
		if _, ok := w.Nodes[node.LoopConfig.ExitTo]; !ok {
			return fmt.Errorf("%w: %s", ErrNodeNotFound, node.LoopConfig.ExitTo)
		}
	}
	// 检查工作流是否存在循环
	if hasCycle(w.Nodes, w.Edges) {
		if hasUncontrolledCycle(w.Nodes, w.Edges) {
			return ErrWorkflowHasUncontrolledCycle
		}
	}
	return nil
}

// hasCycle 检查工作流是否存在循环
// nodes: 节点映射
// edges: 边列表
// 返回是否存在循环
func hasCycle(nodes map[string]Node, edges []Edge) bool {
	// 构建邻接表
	adj := make(map[string][]string, len(nodes))
	for _, e := range edges {
		adj[e.From] = append(adj[e.From], e.To)
	}
	// 初始化访问标记和递归栈
	visited := make(map[string]bool, len(nodes))
	stack := make(map[string]bool, len(nodes))
	// 定义深度优先搜索函数
	var dfs func(id string) bool
	dfs = func(id string) bool {
		// 如果节点在递归栈中，说明存在循环
		if stack[id] {
			return true
		}
		// 如果节点已访问过，直接返回
		if visited[id] {
			return false
		}
		// 标记节点为已访问
		visited[id] = true
		// 将节点加入递归栈
		stack[id] = true
		// 遍历所有相邻节点
		for _, next := range adj[id] {
			if dfs(next) {
				return true
			}
		}
		// 将节点从递归栈中移除
		stack[id] = false
		return false
	}
	// 对每个节点执行深度优先搜索
	for id := range nodes {
		if dfs(id) {
			return true
		}
	}
	return false
}

// cloneStringMap 克隆一个map[string]string
// in: 输入map
// 返回克隆后的map
func cloneStringMap(in map[string]string) map[string]string {
	// 如果输入为nil，返回nil
	if in == nil {
		return nil
	}
	// 创建新的map并复制所有键值对
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// clonePorts 克隆一个Port切片
// in: 输入切片
// 返回克隆后的切片
func clonePorts(in []Port) []Port {
	// 如果切片为空，返回nil
	if len(in) == 0 {
		return nil
	}
	// 创建新的切片并复制所有元素
	out := make([]Port, len(in))
	copy(out, in)
	return out
}

// hasUncontrolledCycle 检查是否存在不包含 loop 节点的环。
func hasUncontrolledCycle(nodes map[string]Node, edges []Edge) bool {
	adj := make(map[string][]string, len(nodes))
	for _, e := range edges {
		adj[e.From] = append(adj[e.From], e.To)
	}

	visited := make(map[string]bool, len(nodes))
	inStack := make(map[string]bool, len(nodes))
	stack := make([]string, 0, len(nodes))

	var dfs func(id string) bool
	dfs = func(id string) bool {
		visited[id] = true
		inStack[id] = true
		stack = append(stack, id)

		for _, next := range adj[id] {
			if inStack[next] {
				start := -1
				for i := len(stack) - 1; i >= 0; i-- {
					if stack[i] == next {
						start = i
						break
					}
				}
				if start >= 0 {
					hasLoop := false
					for _, nid := range stack[start:] {
						if nodes[nid].Type == NodeTypeLoop {
							hasLoop = true
							break
						}
					}
					if !hasLoop {
						return true
					}
				}
				continue
			}
			if !visited[next] {
				if dfs(next) {
					return true
				}
			}
		}

		stack = stack[:len(stack)-1]
		inStack[id] = false
		return false
	}

	for id := range nodes {
		if !visited[id] && dfs(id) {
			return true
		}
	}
	return false
}
