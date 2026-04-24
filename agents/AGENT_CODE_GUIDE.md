# Agents 代码速读指南

这份文档的目标不是逐行解释每个 agent，而是帮你快速建立一套统一的阅读框架。`agents/` 目录下的大多数 agent 都遵循同一种模板，只是“接了什么工具”“工作流怎么编排”“最后怎么组织回复”不同。

如果你后面要给别人讲代码，建议把讲解顺序固定成：

1. `cmd/*.go` 负责启动
2. `agents/*/server_internal.go` 负责把 agent 暴露成 HTTP 服务
3. `agents/*/agent.go` 负责真正的业务逻辑
4. `buildXXXWorkflow()` 负责定义工作流拓扑

---

## 1. 先记住整体调用链

以 `deepresearch` 为例，完整链路是：

`cmd/deepresearch.go`
-> `deepresearch.NewAgent()`
-> `deepresearch.NewHTTPServer()`
-> HTTP 请求进入 `internalProcessor.ProcessMessage()`
-> `agent.ProcessInternal()`
-> `orchestratorEngine.StartWorkflow()`
-> `workflowNodeWorker.Execute()`
-> 根据节点类型调用 `callChatModel()` 或 `callTool()`
-> `orchestratorEngine.WaitRun()`
-> 回写任务状态，返回最终结果

你可以把它理解成三层：

- 启动层：把一个 agent 跑成 HTTP 服务
- 编排层：把一次用户请求包装成 workflow run
- 执行层：每个节点真正调用大模型或工具

---

## 2. 每个 agent 基本都长这样

在 `agents/*/agent.go` 里，通常都会看到下面几个固定部件：

### 2.1 常量区

一般会有 2 到 3 个常量，例如：

- `XXXWorkflowID`
- `XXXWorkflowWorkerID`
- `XXXDefaultTaskType`

作用：

- `WorkflowID`：整个工作流的唯一标识
- `WorkflowWorkerID`：执行工作流节点的 worker 标识
- `DefaultTaskType`：有些 agent 会用它给节点任务分类

这几个名字通常能直接反映 agent 的定位。

### 2.2 Agent 结构体

`Agent` 是整个 agent 的核心对象，一般会持有：

- `orchestratorEngine orchestrator.Engine`
- `llmClient *llm.Client`
- `chatModel string`
- 若干 `tools.Tool`
- 少量业务相关状态

例如：

- `deepresearch` 持有 `TavilyTool`
- `urlreader` 持有 `FetchTool`
- `financehelper` 持有 `MySQLTool`、`AkshareTool`
- `host` 持有 `AgentInfoTool`、`CallAgentTool`

理解要点：

- `Agent` 本身不是“会自动工作”的
- 它更像一个装好了引擎、模型、工具和配置的运行容器

### 2.3 workflowNodeWorker

常见定义是：

```go
type workflowNodeWorker struct {
    agent *Agent
}
```

它的职责很明确：

- orchestrator 在执行 workflow 节点时，会回调这个 worker
- worker 再把执行请求转发给 `agent`

所以它本质上是“工作流执行器”和“业务 agent”之间的一层适配器。

### 2.4 progress / step reporter

很多 agent 都会定义：

- `ctxKeyTaskManager`
- `stepReporter`
- `xxxNodeProgressText`

它们是用来做任务流式进度反馈的，不是主业务核心，但对前端体验很重要。

你可以把它理解成：

- workflow 在跑
- agent 按节点状态生成“当前执行到哪一步了”
- task manager 把这些状态推给前端

---

## 3. 最重要的入口：`NewAgent()`

如果你只能先看一个函数，就先看 `NewAgent()`。

它通常做 5 件事。

### 3.1 读取配置

几乎都从：

```go
cfg := config.GetMainConfig()
```

开始。

然后取出：

- LLM 地址、Key、模型名
- orchestrator 超时与重试配置
- MySQL / Tavily / AMap / MCP 相关配置

### 3.2 初始化模型客户端

典型写法：

```go
agent.llmClient = llm.NewClient(cfg.LLM.URL, cfg.LLM.APIKey)
agent.chatModel = strings.TrimSpace(cfg.LLM.ChatModel)
```

然后通常有一层兜底：

- 先用 `ChatModel`
- 没有就退到 `ReasoningModel`
- 再没有就写死默认模型名

这说明项目里的 agent 都是“配置优先，默认值兜底”的风格。

### 3.3 初始化工具

这是不同 agent 之间差异最大的地方。

常见几种工具来源：

- `tools.NewHTTPTool(...)`
- `tools.NewRawHTTPTool(...)`
- `tools.NewMCPTool(...)`
- `tools.NewCallAgentTool(...)`

含义分别大致是：

- HTTP Tool：调用外部 HTTP API
- Raw HTTP Tool：直接封装一个 Go 回调为工具
- MCP Tool：通过 MCP 协议调用工具
- CallAgentTool：把另一个 agent 当工具调用

### 3.4 初始化 orchestrator 引擎

典型流程：

```go
engineCfg := orchestrator.Config{...}
agent.orchestratorEngine = orchestrator.NewEngine(
    engineCfg,
    orchestrator.NewInMemoryAgentRegistry(),
)
```

这里要记住一件事：

- agent 真正的执行控制，不是手写 if-else 串起来的
- 而是交给 `orchestrator.Engine` 按 workflow 图来跑

### 3.5 注册 worker 和 workflow

这是 `NewAgent()` 的收尾，也是最关键的装配动作：

```go
agent.orchestratorEngine.RegisterWorker(...)
wf, err := buildXXXWorkflow()
agent.orchestratorEngine.RegisterWorkflow(wf)
```

一句话理解：

- 注册 worker：告诉引擎“节点来了谁来执行”
- 注册 workflow：告诉引擎“整张图长什么样”

所以 `NewAgent()` 干的其实是“把一个可运行的工作流 agent 装配完成”。

---

## 4. 第二关键入口：`ProcessInternal()`

这个函数几乎是所有 agent 的统一主入口。

它通常负责 6 件事。

### 4.1 解析用户输入

会从 `protocol.Message` 里抽取文本部分：

- 遍历 `initialMsg.Parts`
- 只保留 `PartTypeText`
- 拼成一个 `query`

这说明本项目的 agent 输入层是消息协议统一的，不直接依赖 HTTP body 细节。

### 4.2 读取 metadata

很多 agent 会从 `initialMsg.Metadata` 中取：

- `user_id`
- `api_key`
- 其他业务字段

这里是“请求上下文”和“用户文本”分开的体现。

### 4.3 启动工作流

核心调用通常是：

```go
runID, err := a.orchestratorEngine.StartWorkflow(ctx, XXXWorkflowID, map[string]any{...})
```

这里传进去的 payload 很重要，常见字段有：

- `task_id`
- `query`
- `text`
- `input`
- `user_id`

可以把它理解成：把一轮用户请求包装成工作流初始上下文。

### 4.4 启动进度上报

很多 agent 会做：

```go
stopProgress := a.startProgressReporter(ctx, taskID, runID, manager)
defer stopProgress()
```

用途是：

- 监听 workflow 节点状态
- 把“执行到哪一步了”的信息实时推到任务系统

### 4.5 等待工作流执行完成

核心调用：

```go
runResult, err := a.orchestratorEngine.WaitRun(ctx, runID)
```

然后检查：

- `runResult.State`
- `runResult.ErrorMessage`
- `runResult.FinalOutput`

### 4.6 组织最终输出并更新任务状态

常见收尾动作：

- 从 `FinalOutput["response"]` 取最终文本
- 或者调用专门的结构化输出函数整理结果
- `manager.UpdateTaskState(..., TaskStateCompleted, ...)`

这一步就是把 workflow 的运行结果变成对用户可见的最终回复。

---

## 5. 真正执行节点的是：`workflowNodeWorker.Execute()`

这个函数是理解 agent 模板的核心。

它做的事情非常固定：

1. 拿到 orchestrator 传入的 `ExecutionRequest`
2. 读取当前节点的 `NodeType`
3. 如果是大模型节点，走 `callChatModel()`
4. 如果是工具节点，走 `callTool()`
5. 返回 `ExecutionResult`

典型结构是：

```go
switch req.NodeType {
case orchestrator.NodeTypeChatModel:
    output, err = w.agent.callChatModel(...)
case orchestrator.NodeTypeTool:
    output, err = w.agent.callTool(...)
default:
    output = map[string]any{"response": query}
}
```

你讲给别人时可以直接说：

- workflow 决定“下一步做什么”
- worker 决定“这一步具体怎么执行”

---

## 6. `callChatModel()` 和 `callTool()` 分别负责什么

### 6.1 `callChatModel()`

这个函数通常负责：

- 根据 `nodeCfg` 决定 prompt / intent / model / url / api key
- 调用 `llmClient.ChatCompletion(...)` 或流式接口
- 把模型返回结果包装成统一的 `map[string]any`

常见返回字段：

- `response`
- `action`
- `keywords`
- 其他中间结构化字段

重点理解：

- 这里不是“单纯问一下大模型”
- 而是“根据当前 workflow 节点的语义来调用模型”

也就是说，**节点配置决定了模型这一步扮演什么角色**。

### 6.2 `callTool()`

这个函数通常负责：

- 从 `nodeCfg` 中拿到 `tool_name`
- 组装工具参数
- 调用 `tool.Execute(...)`
- 解析工具输出
- 继续写回 workflow payload

重点理解：

- 大模型节点负责思考、判断、总结
- 工具节点负责拿外部真实数据

这是整个项目 agent 模板里最稳定的一条边界。

---

## 7. `buildXXXWorkflow()` 是“业务流程图”

每个 agent 最后都会有一个很长的 `buildXXXWorkflow()`。

如果 `ProcessInternal()` 是主入口，
那 `buildXXXWorkflow()` 就是“业务脑图”。

这里通常会做：

1. `orchestrator.NewWorkflow(...)`
2. `wf.AddNode(...)`
3. 设置开始节点 / 结束节点
4. 配置节点之间的连线关系
5. 配置条件分支、输入映射、节点元数据

阅读时重点看这几类信息：

- 节点 ID
- 节点类型 `NodeTypeChatModel` / `NodeTypeTool`
- `Config`
- `input_mapping`
- `input_source`
- `tool_name`
- `intent`

你可以把它理解成：

- `agent.go` 前半部分是在造执行器
- `buildXXXWorkflow()` 是在定义执行图

### 7.1 看 workflow 时不要先看细节，先看节点职责

建议先把节点按三类归类：

- 起始/收尾节点：`N_start`、`N_end`
- 判断节点：路由、分类、条件分支
- 执行节点：模型调用或工具调用

### 7.2 `xxxNodeProgressText` 和 workflow 节点 ID 是对应关系

像：

- `deepResearchNodeProgressText`
- `financeHelperNodeProgressText`
- `hostNodeProgressText`

本质上是把技术节点 ID 翻译成用户可见步骤文案。

所以如果你要讲“这个 agent 的流程”，可以直接把：

- `buildXXXWorkflow()`
- `xxxNodeProgressText`

结合起来看，理解会快很多。

---

## 8. `server_internal.go` 的角色：把内部 Agent 包成 HTTP 服务

这个文件也几乎是统一模板。

它主要包含两个东西。

### 8.1 `internalProcessor`

它实现了消息处理接口，核心动作是：

- `manager.BuildTask(...)`
- `manager.SubscribeTask(...)`
- 开 goroutine 调用 `agent.ProcessInternal(...)`
- 失败时把任务状态改成 `TaskStateFailed`

所以它的角色是：

- 把“同步 HTTP 请求”
- 转成“异步任务流 + 流式事件订阅”

### 8.2 `NewHTTPServer()`

这个函数会做两件事：

1. 定义 `protocol.AgentCard`
2. 用 `httpagent.NewServer(...)` 创建 HTTP 服务

`AgentCard` 里通常会写：

- agent 名称
- 描述
- provider
- 输入输出模式
- skills

这相当于 agent 的“对外名片”。

所以你可以把 `server_internal.go` 理解成：

- 对内调用 `ProcessInternal`
- 对外暴露标准化 agent 服务协议

---

## 9. `cmd/*.go` 的角色：把 agent 跑起来

例如 [cmd/deepresearch.go](/c:/Users/takatsuki/Desktop/毕业设计/mmmanus-main/cmd/deepresearch.go:1)：

- `config.Init()`
- `deepresearch.NewAgent()`
- `deepresearch.NewHTTPServer()`
- `http.ListenAndServe(addr, handler)`

它几乎不包含业务逻辑，只负责：

- 初始化配置
- 选择监听地址
- 启动 HTTP 服务

所以讲解时可以明确说：

- `cmd` 只是启动器
- 真正逻辑都在 `agents/*`

---

## 10. 推荐的源码阅读顺序

如果你要快速讲明白一个 agent，建议按这个顺序读：

1. 看 `cmd/xxx.go`
2. 看 `agents/xxx/server_internal.go`
3. 看 `agents/xxx/agent.go` 里的 `Agent` 结构体
4. 看 `NewAgent()`
5. 看 `ProcessInternal()`
6. 看 `workflowNodeWorker.Execute()`
7. 看 `callChatModel()` / `callTool()`
8. 最后看 `buildXXXWorkflow()`

原因很简单：

- 先知道入口
- 再知道执行主线
- 最后再下沉到业务细节

如果你一上来就读 `buildXXXWorkflow()`，通常会被大量节点配置淹没。

---

## 11. 各个 agent 的差异，主要差在“工具”和“workflow”

虽然模板类似，但业务重点不同。

### 11.1 `deepresearch`

关键词：

- Tavily 搜索
- 循环检索
- 判断信息是否足够
- 结构化总结

讲解重点：

- 它是“搜索型 agent”
- workflow 里有明显的循环与停止判断

### 11.2 `urlreader`

关键词：

- 提取 URL
- 调 fetch MCP
- 生成网页摘要

讲解重点：

- 它是“单资源读取 + 总结”型 agent
- 结构比 `deepresearch` 更直

### 11.3 `financehelper`

关键词：

- MySQL
- AkShare
- 账单写入 / 报告查询 / 财经资讯 / 理财建议

讲解重点：

- 它是“多分支、多工具、多结构化数据”型 agent
- `callTool()` 和 schema 处理部分比别的 agent 复杂得多

### 11.4 `host`

关键词：

- `agent_info`
- `call_agent`
- 调度其他 agent

讲解重点：

- 它更像“总控 agent”或“路由 agent”
- 自己不一定做具体业务，而是决定是否调用别的 agent

### 11.5 `lbshelper`

关键词：

- 地图 / 地址 / POI
- AMap MCP

讲解重点：

- 核心差异在地图工具调用与地点类 prompt

### 11.6 `bazihelper`

关键词：

- 八字参数提取
- MCP 工具调用
- 结果总结

讲解重点：

- 典型的“模型抽参数 -> 工具执行 -> 模型总结”

### 11.7 `resumecustomizer`

关键词：

- 简历优化
- 大模型生成

讲解重点：

- 工具参与较少
- 更偏模型驱动型 workflow

### 11.8 `interviewsimulator`

关键词：

- 面试流程
- 多轮问答模拟

讲解重点：

- 核心在 prompt 设计和 workflow 对多轮过程的组织

### 11.9 `careerradar`

关键词：

- 岗位分析
- 调其他 agent
- 大模型总结

讲解重点：

- 介于 `host` 和普通业务 agent 之间
- 既有编排，也有自己的分析逻辑

---

## 12. 给别人讲时，最值得反复强调的 4 个概念

### 12.1 Agent 不是单函数，而是“工作流 + 工具 + 模型 + HTTP 包装”

这个项目里的 agent 不是简单的：

- 输入字符串
- 输出字符串

而是一个完整的运行单元，包含：

- 对外协议
- 对内工作流
- 节点执行器
- 状态管理

### 12.2 `ProcessInternal()` 是请求级主线

每来一条消息，几乎都会走这条主线：

- 解析输入
- 启动 workflow
- 等待结果
- 返回结果

所以不管 agent 多复杂，都能先从这里落脚。

### 12.3 `buildXXXWorkflow()` 决定“流程”，`callChatModel()` / `callTool()` 决定“动作”

这是理解整个模板最关键的一句话：

- workflow 定义步骤顺序和分支
- 节点执行函数定义每一步具体怎么做

### 12.4 大部分 agent 的可复用模板非常强

如果后面你要自己新增一个 agent，通常可以复用现有结构：

1. 定义 `Agent` 结构体
2. 写 `NewAgent()`
3. 写 `ProcessInternal()`
4. 写 `workflowNodeWorker.Execute()`
5. 实现 `callChatModel()` / `callTool()`
6. 写 `buildXXXWorkflow()`
7. 写 `server_internal.go`
8. 在 `cmd/` 下加启动命令

也就是说，这个项目已经形成了一套相对稳定的 agent 脚手架。

---

## 13. 你可以直接拿去讲的一段总结

这套 agent 架构的核心思想是：

- 用 `cmd` 启动服务
- 用 `server_internal.go` 暴露标准 HTTP agent 接口
- 用 `ProcessInternal()` 把一次用户请求包装成工作流执行
- 用 `orchestrator` 驱动 workflow
- 用 `workflowNodeWorker.Execute()` 执行每个节点
- 用 `callChatModel()` 和 `callTool()` 分别处理模型节点和工具节点
- 最后把结果通过 task manager 流式返回给前端

因此，`agents` 目录里的各个 agent 看起来文件很长，但真正的骨架非常统一。你只要先抓住“装配、启动、编排、执行、返回”这五步，后面看任何一个 agent 都会快很多。

---

## 14. 对应参考文件

推荐你先看这些文件：

- [cmd/deepresearch.go](/c:/Users/takatsuki/Desktop/毕业设计/mmmanus-main/cmd/deepresearch.go:1)
- [agents/deepresearch/server_internal.go](/c:/Users/takatsuki/Desktop/毕业设计/mmmanus-main/agents/deepresearch/server_internal.go:1)
- [agents/deepresearch/agent.go](/c:/Users/takatsuki/Desktop/毕业设计/mmmanus-main/agents/deepresearch/agent.go:1)
- [agents/urlreader/agent.go](/c:/Users/takatsuki/Desktop/毕业设计/mmmanus-main/agents/urlreader/agent.go:1)
- [agents/financehelper/agent.go](/c:/Users/takatsuki/Desktop/毕业设计/mmmanus-main/agents/financehelper/agent.go:1)
- [agents/host/agent.go](/c:/Users/takatsuki/Desktop/毕业设计/mmmanus-main/agents/host/agent.go:1)

如果你时间有限，优先顺序建议是：

1. `deepresearch`
2. `urlreader`
3. `host`
4. `financehelper`

这个顺序从“模板最清晰”逐步过渡到“业务最复杂”。
