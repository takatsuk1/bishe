# MMManus 智能体平台

这是一个基于 Go 语言构建的智能体（Agent）管理平台，提供多种专业领域的智能助手服务，支持工作流编排、工具调用和聊天交互功能。

## 项目架构

```
mmmanus-main/
├── agents/          # 智能体模块
├── api/             # API 接口层
├── cmd/             # 命令行入口
├── config/          # 配置管理
├── pkg/             # 核心包
├── scripts/         # 脚本和数据库迁移
├── tools/           # 外部工具 MCP
├── uml图/           # UML 设计文档
└── web/             # 前端应用
```

## 目录结构详解

### agents/ - 智能体模块

包含多个专业领域的智能体实现：

| 智能体 | 描述 |
|--------|------|
| `bazihelper/` | 八字命理助手，提供命理分析服务 |
| `careerradar/` | 职业雷达，提供职业规划建议 |
| `deepresearch/` | 深度研究助手，支持深入调研分析 |
| `financehelper/` | 金融助手，提供财务分析服务 |
| `host/` | 主机管理助手，管理主机资源 |
| `interviewsimulator/` | 面试模拟器，模拟面试场景 |
| `lbshelper/` | LBS 位置服务助手 |
| `resumecustomizer/` | 简历定制助手，优化简历内容 |
| `urlreader/` | URL 读取助手，解析网页内容 |
| `user_agents/` | 用户自定义智能体测试示例 |

每个智能体包含：
- `agent.go` - 智能体核心逻辑
- `*_helper.go` - 业务辅助函数
- `server_internal.go` - 内部服务实现
- `agent_test.go` - 单元测试

### api/ - API 接口层

提供对外 RESTful API 服务：

| 模块 | 描述 |
|------|------|
| `admin/` | 管理员接口，管理用户和权限 |
| `auth/` | 认证接口，处理登录注册 |
| `chat/` | 聊天接口，支持 OpenAI 兼容协议 |
| `monitor/` | 监控接口，查看系统状态 |
| `orchestrator/` | 编排接口，管理工作流和工具 |

### cmd/ - 命令行入口

包含项目的启动命令：

- `allinone.go` - 一站式启动所有服务
- `bazihelper.go`, `deepresearch.go`, `financehelper.go` 等 - 单个智能体启动命令
- `openai_connector.go` - OpenAI 连接器
- `public_services.go` - 公共服务启动
- `root.go` - 根命令定义

### config/ - 配置管理

- `config.go` - 配置加载和解析逻辑

### pkg/ - 核心包

平台核心功能模块：

| 包 | 描述 |
|------|------|
| `agentfmt/` | 智能体格式化工具 |
| `agentmanager/` | 智能体管理器，管理生命周期 |
| `auth/` | 认证中间件和服务 |
| `authz/` | 授权服务 |
| `card/` | 卡片组件 |
| `codegen/` | 代码生成器 |
| `executor/` | 工作流执行器 |
| `llm/` | LLM 客户端封装 |
| `logger/` | 日志记录器 |
| `memory/` | 内存存储管理 |
| `monitor/` | 监控服务 |
| `noncore_service/` | 非核心服务（用户管理、工具运行时等） |
| `orchestrator/` | 编排引擎，管理工作流和任务 |
| `protocol/` | 协议定义 |
| `storage/` | 存储层（MySQL、Redis） |
| `taskmanager/` | 任务管理器 |
| `tools/` | 工具注册和调用（地图、HTTP、MCP等） |
| `transport/` | 传输层（HTTP 客户端/服务端） |

### scripts/ - 脚本和数据库迁移

- `perf/` - 性能测试脚本
- `migrate_*.sql` - 数据库迁移脚本
- `monitor_schema.sql` - 监控表结构
- `schema.sql` - 主数据库表结构

### tools/ - 外部工具 MCP

外部工具的 MCP（Model Context Protocol）实现：

- `jsonfilemcp/` - JSON 文件操作工具
- `mysqlmcp/` - MySQL 数据库操作工具
- `scriptmcp/` - 脚本执行工具

### uml图/ - UML 设计文档

包含系统的完整 UML 设计图：

- `E-R图/` - 实体关系图
- `时序图/` - 时序图
- `活动图/` - 活动图
- `用例图/` - 用例图
- `类图/` - 类图
- `系统包图/` - 系统整体包图

### web/ - 前端应用

基于 **Vue 3 + TypeScript + Vite** 的现代化前端界面：

#### 项目结构

| 目录 | 描述 |
|------|------|
| `public/` | 静态资源（favicon.svg, icons.svg） |
| `src/assets/` | 图片资源（hero.png, vite.svg, vue.svg） |
| `src/components/` | 可复用组件 |
| `src/layouts/` | 布局组件 |
| `src/lib/` | API 客户端和工具函数 |
| `src/pages/` | 页面组件 |

#### 核心组件

| 组件 | 描述 |
|------|------|
| `AgentTester.vue` | 智能体测试组件 |
| `ModulePageHeader.vue` | 模块页面头部 |
| `ModuleSectionCard.vue` | 模块区域卡片 |
| `PageContainer.vue` | 页面容器 |
| `PageHeader.vue` | 页面头部 |
| `StatCard.vue` | 统计卡片 |
| `WorkspaceTopNav.vue` | 工作区顶部导航 |

#### 页面组件

| 页面 | 描述 |
|------|------|
| `LoginPage.vue` | 登录页面 |
| `RegisterPage.vue` | 注册页面 |
| `HomePage.vue` | 首页 |
| `ChatPage.vue` | 聊天页面 |
| `AssistantsCenterPage.vue` | 助手中心 |
| `AdminUsersPage.vue` | 用户管理页面 |
| `MonitorPage.vue` | 监控页面 |
| `PlatformCapabilitiesPage.vue` | 平台能力展示 |
| `ProfilePage.vue` | 用户个人资料 |

#### 布局组件

- `PublicLayout.vue` - 公共布局（登录、注册等）
- `WorkspaceLayout.vue` - 工作区布局（登录后）

#### API 客户端

| 文件 | 描述 |
|------|------|
| `authApi.ts` | 认证相关接口 |
| `adminApi.ts` | 管理员接口 |
| `orchestratorApi.ts` | 编排服务接口 |
| `userAgentApi.ts` | 用户智能体接口 |
| `monitorApi.ts` | 监控接口 |
| `agents.ts` | 智能体列表管理 |
| `authStore.ts` | 认证状态管理 |
| `stream.ts` | 流式响应处理 |
| `permission.ts` | 权限控制 |

## 技术栈

- **后端**: Go 1.20+
- **前端**: Vue 3 + TypeScript + Vite
- **数据库**: MySQL + Redis
- **API**: RESTful + OpenAI 兼容接口
- **日志**: 结构化日志

## 快速开始

```bash
# 安装依赖
go mod download

# 启动所有服务
go run main.go allinone

# 启动单个智能体
go run main.go bazihelper
```

## 配置

配置文件位于 `config.yaml`，包含数据库连接、LLM 配置、服务端口等。

