# Agent Workflow API 文档

## 概述

Agent Workflow API 提供获取项目中指定 Agent 完整编排结构数据的接口。该接口返回 host、deepresearch、urlreader 和 lbshelper 四个 Agent 的详细编排信息，便于前端为每个 Agent 单独生成对应的编排图。

## 基本信息

- **API 版本**: v1
- **基础路径**: `/v1/agent-workflows`
- **响应格式**: JSON
- **字符编码**: UTF-8

---

## 接口列表

### 1. 获取所有 Agent 编排结构

获取项目中所有 Agent 的完整编排结构数据。

**请求**

```
GET /v1/agent-workflows
```

**请求头**

| 参数名 | 类型 | 必填 | 说明 |
|--------|------|------|------|
| Authorization | string | 否 | Bearer Token，用于身份验证 |
| X-Forwarded-For | string | 否 | 客户端真实 IP，用于频率限制 |

**请求参数**

| 参数名 | 类型 | 必填 | 说明 |
|--------|------|------|------|
| token | string | 否 | 认证 Token（可替代 Authorization 头） |

**响应示例**

```json
{
  "apiVersion": "v1",
  "timestamp": "2024-01-15T10:30:00Z",
  "agents": [
    {
      "id": "host",
      "name": "Host Agent",
      "type": "orchestrator",
      "description": "主控 Agent，负责路由决策和调用下游 Agent",
      "version": "1.0.0",
      "configuration": {
        "timeout": 600,
        "retryPolicy": {
          "maxAttempts": 3,
          "initialDelayMs": 200,
          "maxDelayMs": 5000,
          "backoffMultiplier": 2.0
        },
        "inputSchema": {
          "type": "object",
          "properties": {
            "text": { "type": "string", "description": "用户输入文本" },
            "user_id": { "type": "string", "description": "用户 ID" }
          },
          "required": ["text"]
        },
        "outputSchema": {
          "type": "object",
          "properties": {
            "response": { "type": "string", "description": "最终响应" },
            "agent_name": { "type": "string", "description": "调用的下游 Agent 名称" }
          }
        },
        "environmentVars": [
          { "name": "LLM_URL", "required": true, "description": "大模型 API URL" },
          { "name": "LLM_API_KEY", "required": true, "description": "大模型 API Key" }
        ]
      },
      "dependencies": [
        { "agentId": "deepresearch", "type": "downstream", "required": false, "description": "深度研究 Agent" },
        { "agentId": "urlreader", "type": "downstream", "required": false, "description": "URL 读取 Agent" },
        { "agentId": "lbshelper", "type": "downstream", "required": false, "description": "位置服务 Agent" }
      ],
      "executionOrder": {
        "startNodeId": "start",
        "sequence": ["start", "init_agents", "chat_model", "condition", "call_agent", "end"],
        "parallel": null
      },
      "nodes": [...],
      "edges": [...],
      "metadata": {
        "createdAt": "2024-01-01T00:00:00Z",
        "updatedAt": "2024-01-15T10:30:00Z",
        "author": "system",
        "tags": ["orchestrator", "router", "main"],
        "labels": { "tier": "primary", "visibility": "public" }
      }
    }
  ]
}
```

---

### 2. 获取单个 Agent 编排结构

根据 Agent ID 获取指定 Agent 的编排结构数据。

**请求**

```
GET /v1/agent-workflows/{agentId}
```

**路径参数**

| 参数名 | 类型 | 必填 | 说明 |
|--------|------|------|------|
| agentId | string | 是 | Agent 唯一标识符，可选值：host, deepresearch, urlreader, lbshelper |

**响应示例**

```json
{
  "apiVersion": "v1",
  "timestamp": "2024-01-15T10:30:00Z",
  "agent": {
    "id": "deepresearch",
    "name": "Deep Research Agent",
    "type": "worker",
    "description": "深度研究 Agent，使用 Tavily 进行深度检索并整理答案",
    "version": "1.0.0",
    ...
  }
}
```

---

## 数据结构

### AgentWorkflowDetail

| 字段 | 类型 | 说明 |
|------|------|------|
| id | string | Agent 唯一标识符 |
| name | string | Agent 显示名称 |
| type | string | Agent 类型：orchestrator（编排器）或 worker（工作者） |
| description | string | Agent 功能描述 |
| version | string | Agent 版本号 |
| configuration | AgentConfiguration | Agent 配置信息 |
| dependencies | AgentDependency[] | Agent 依赖列表 |
| executionOrder | ExecutionOrder | 执行顺序信息 |
| nodes | NodeDefinition[] | 节点定义列表 |
| edges | EdgeDefinition[] | 边定义列表 |
| metadata | AgentMetadata | 元数据信息 |

### AgentConfiguration

| 字段 | 类型 | 说明 |
|------|------|------|
| timeout | int | 超时时间（秒） |
| retryPolicy | RetryPolicy | 重试策略 |
| inputSchema | object | 输入参数 JSON Schema |
| outputSchema | object | 输出参数 JSON Schema |
| environmentVars | EnvironmentVar[] | 环境变量列表 |

### RetryPolicy

| 字段 | 类型 | 说明 |
|------|------|------|
| maxAttempts | int | 最大重试次数 |
| initialDelayMs | int | 初始延迟（毫秒） |
| maxDelayMs | int | 最大延迟（毫秒） |
| backoffMultiplier | float | 退避乘数 |

### AgentDependency

| 字段 | 类型 | 说明 |
|------|------|------|
| agentId | string | 依赖的 Agent ID |
| type | string | 依赖类型：downstream（下游）、external_service（外部服务） |
| required | bool | 是否必需 |
| description | string | 依赖描述 |
| inputMapping | map | 输入映射（可选） |

### ExecutionOrder

| 字段 | 类型 | 说明 |
|------|------|------|
| startNodeId | string | 起始节点 ID |
| sequence | string[] | 执行序列 |
| parallel | string[][] | 并行执行节点组（可选） |

### NodeDefinition

| 字段 | 类型 | 说明 |
|------|------|------|
| id | string | 节点唯一标识符 |
| type | string | 节点类型：start, end, task, condition, loop, chat_model, tool, agent_ref |
| agentId | string | 所属 Agent ID（可选） |
| taskType | string | 任务类型（可选） |
| condition | string | 条件表达式（condition 类型节点） |
| loopConfig | LoopConfig | 循环配置（loop 类型节点） |
| metadata | map | 元数据，包含 UI 信息 |

### EdgeDefinition

| 字段 | 类型 | 说明 |
|------|------|------|
| from | string | 源节点 ID |
| to | string | 目标节点 ID |
| label | string | 边标签（如 true/false） |
| mapping | map | 数据映射（可选） |

### AgentMetadata

| 字段 | 类型 | 说明 |
|------|------|------|
| createdAt | string | 创建时间（ISO 8601） |
| updatedAt | string | 更新时间（ISO 8601） |
| author | string | 作者 |
| tags | string[] | 标签列表 |
| labels | map | 标签键值对 |

---

## 错误响应

所有错误响应遵循统一格式：

```json
{
  "apiVersion": "v1",
  "error": {
    "code": "ERROR_CODE",
    "message": "错误描述",
    "details": "详细信息（可选）"
  }
}
```

### 错误码

| 错误码 | HTTP 状态码 | 说明 |
|--------|-------------|------|
| UNAUTHORIZED | 401 | 认证失败，Token 无效或已过期 |
| RATE_LIMIT_EXCEEDED | 429 | 请求频率超限，请稍后重试 |
| AGENT_NOT_FOUND | 404 | 指定的 Agent 不存在 |
| INTERNAL_ERROR | 500 | 服务器内部错误 |
| INVALID_REQUEST | 400/405 | 请求无效或方法不允许 |

---

## 权限控制

接口支持可选的 Token 认证：

1. **Authorization 头**: `Authorization: Bearer <token>`
2. **Query 参数**: `?token=<token>`

如果提供了 Token，系统会验证其有效性。无效 Token 将返回 401 错误。

---

## 频率限制

- **限制**: 每个客户端每分钟最多 100 次请求
- **识别方式**: 优先使用 X-Forwarded-For 头，否则使用 RemoteAddr
- **超限响应**: HTTP 429，错误码 RATE_LIMIT_EXCEEDED

---

## 前端解析指南

### 1. 获取 Agent 列表

```typescript
const response = await fetch('/v1/agent-workflows');
const data = await response.json();

// 遍历所有 Agent
data.agents.forEach(agent => {
  console.log(`Agent: ${agent.name} (${agent.id})`);
});
```

### 2. 为每个 Agent 生成编排图

```typescript
function renderAgentWorkflow(agent: AgentWorkflowDetail) {
  // 1. 创建节点映射
  const nodeMap = new Map(agent.nodes.map(n => [n.id, n]));
  
  // 2. 创建边映射
  const edgeMap = new Map<string, EdgeDefinition[]>();
  agent.edges.forEach(edge => {
    if (!edgeMap.has(edge.from)) {
      edgeMap.set(edge.from, []);
    }
    edgeMap.get(edge.from)!.push(edge);
  });
  
  // 3. 从起始节点开始渲染
  let currentId = agent.executionOrder.startNodeId;
  while (currentId) {
    const node = nodeMap.get(currentId);
    renderNode(node);
    
    const outEdges = edgeMap.get(currentId) || [];
    if (outEdges.length === 0) break;
    
    currentId = outEdges[0].to;
  }
}
```

### 3. 解析节点 UI 信息

```typescript
function getNodeUI(node: NodeDefinition) {
  return {
    x: parseInt(node.metadata?.['ui.x'] || '0'),
    y: parseInt(node.metadata?.['ui.y'] || '0'),
    label: node.metadata?.['ui.label'] || node.id,
    agent: node.metadata?.['ui.agent'],
  };
}
```

### 4. 处理条件分支

```typescript
function handleConditionNode(node: NodeDefinition, edges: EdgeDefinition[]) {
  const trueEdge = edges.find(e => e.label === 'true');
  const falseEdge = edges.find(e => e.label === 'false');
  
  return {
    trueTarget: trueEdge?.to,
    falseTarget: falseEdge?.to,
  };
}
```

### 5. 渲染依赖关系图

```typescript
function renderDependencyGraph(agent: AgentWorkflowDetail) {
  agent.dependencies.forEach(dep => {
    console.log(`${agent.id} -> ${dep.agentId} (${dep.type})`);
  });
}
```

---

## 性能指标

- **响应时间**: < 200ms（缓存命中时 < 10ms）
- **缓存策略**: 响应数据缓存 5 分钟
- **并发支持**: 支持高并发请求

---

## 版本控制

API 支持版本控制，当前版本为 v1。响应头中包含版本信息：

```
X-API-Version: v1
```

未来版本扩展时，将保持向后兼容性，通过版本号区分不同版本的 API。
