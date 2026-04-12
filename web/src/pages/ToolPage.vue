<script setup lang="ts">
import { computed, ref, onMounted, nextTick } from 'vue'
import {
  listUserTools,
  listMCPTools,
  startMCPServer,
  stopMCPServer,
  createUserTool,
  updateUserTool,
  deleteUserTool,
  type UserTool,
  type MCPServerTool,
  type ToolParameter,
} from '../lib/userAgentApi'
import { canManageOwnTools, canManageSystemTools, currentPrimaryRole } from '../lib/permission'
import PageContainer from '../components/PageContainer.vue'
import PageHeader from '../components/PageHeader.vue'
import ModuleSectionCard from '../components/ModuleSectionCard.vue'
import StatCard from '../components/StatCard.vue'

const tools = ref<UserTool[]>([])
const loading = ref(false)
const error = ref('')
const fieldErrors = ref<Record<string, string>>({})
const showModal = ref(false)
const editingTool = ref<UserTool | null>(null)
const mcpToolsByToolId = ref<Record<string, MCPServerTool[]>>({})
const mcpToolsLoadingByToolId = ref<Record<string, boolean>>({})
const mcpToolsErrorByToolId = ref<Record<string, string>>({})
const mcpServerRunningByToolId = ref<Record<string, boolean>>({})
const inlineEditorRef = ref<HTMLElement | null>(null)
const readOnlyMode = computed(() => !canManageOwnTools() && !canManageSystemTools())
const roleLabel = computed(() => currentPrimaryRole.value)
const toolStats = computed(() => [
  { label: '工具总数', value: String(tools.value.length) },
  { label: 'HTTP 工具', value: String(tools.value.filter((item) => item.toolType === 'http').length) },
  { label: 'MCP 工具', value: String(tools.value.filter((item) => item.toolType === 'mcp').length) },
])

function baseInfoLines(tool: UserTool): string[] {
  const lines: string[] = []
  if (tool.toolType === 'http' && tool.config) {
    lines.push(`${String((tool.config as any).method || 'GET').toUpperCase()} · ${String((tool.config as any).url || 'URL 未配置')}`)
  }
  if (tool.toolType === 'mcp' && tool.config) {
    const mode = String((tool.config as any).mcp_mode || 'url')
    lines.push(mode === 'stdio' ? `STDIO · ${mcpServerURL(tool) || '未配置'}` : `URL · ${String((tool.config as any).server_url || '未配置')}`)
  }
  lines.push(`参数数量 · ${tool.parameters?.length || 0}`)
  return lines
}

function isSystemTool(tool: UserTool): boolean {
  return String(tool.userId || '').trim().toLowerCase() === 'system'
}

function canManageTool(tool: UserTool): boolean {
  if (isSystemTool(tool)) {
    return canManageSystemTools()
  }
  return canManageOwnTools()
}

async function revealEditor() {
  await nextTick()
  inlineEditorRef.value?.scrollIntoView({ behavior: 'smooth', block: 'start' })
}

function defaultHTTPConfig() {
  return {
    method: 'GET',
    url: '',
    headers: {} as Record<string, string>,
    timeout: 30,
  }
}

function defaultMCPConfig() {
  return {
    mcp_mode: 'url',
    server_url: '',
    tool_name: '',
    server_name: '',
    mcp_servers_json: '',
  }
}

const form = ref({
  toolId: '',
  name: '',
  description: '',
  toolType: 'http',
  config: defaultHTTPConfig(),
  parameters: [] as ToolParameter[],
  outputParameters: [] as ToolParameter[],
})

const newParam = ref<ToolParameter>({
  name: '',
  type: 'string',
  required: false,
  description: '',
})
const newOutputParam = ref<ToolParameter>({
  name: '',
  type: 'string',
  required: false,
  description: '',
})

async function loadTools() {
  console.log('[ToolPage] loadTools called')
  loading.value = true
  error.value = ''
  try {
    console.log('[ToolPage] calling listUserTools API')
    const result = await listUserTools()
    tools.value = Array.isArray(result) ? result : []
    await loadMCPToolsForCards()
    console.log('[ToolPage] listUserTools result:', tools.value)
  } catch (e) {
    console.error('[ToolPage] loadTools error:', e)
    error.value = e instanceof Error ? e.message : '加载工具列表失败'
  } finally {
    loading.value = false
  }
}

function mcpServerURL(tool: UserTool): string {
  if (tool.toolType !== 'mcp') {
    return ''
  }
  const cfg = (tool.config || {}) as Record<string, unknown>
  if (String(cfg.mcp_mode || 'url').trim() === 'stdio') {
    return getStdioServerLabel(cfg)
  }
  return String(cfg.server_url || '').trim()
}

function getStdioServerLabel(cfg: Record<string, unknown>): string {
  const explicit = String(cfg.server_name || '').trim()
  if (explicit) return explicit
  const servers = (cfg.mcp_servers || cfg.mcpServers) as Record<string, unknown> | undefined
  if (!servers || typeof servers !== 'object') return ''
  const keys = Object.keys(servers)
  return keys.length > 0 ? keys[0] : ''
}

function shouldAutoLoadMcpTools(tool: UserTool): boolean {
  if (tool.toolType !== 'mcp') return false
  const cfg = (tool.config || {}) as Record<string, unknown>
  const mode = String(cfg.mcp_mode || 'url').trim()
  if (mode === 'stdio') return true
  return !!String(cfg.server_url || '').trim()
}

async function loadMCPToolsForCards() {
  const mcpTools = tools.value.filter((tool) => shouldAutoLoadMcpTools(tool))
  await Promise.all(mcpTools.map(async (tool) => {
    mcpToolsLoadingByToolId.value[tool.toolId] = true
    mcpToolsErrorByToolId.value[tool.toolId] = ''
    try {
      const cfg = (tool.config || {}) as Record<string, unknown>
      const mode = String(cfg.mcp_mode || 'url').trim()
      if (mode === 'stdio') {
        const started = await startMCPServer(tool.toolId)
        mcpToolsByToolId.value[tool.toolId] = Array.isArray(started.tools) ? started.tools : []
        mcpServerRunningByToolId.value[tool.toolId] = !!started.started
      } else {
        const listed = await listMCPTools(tool.toolId)
        mcpToolsByToolId.value[tool.toolId] = listed
        mcpServerRunningByToolId.value[tool.toolId] = listed.length > 0
      }
    } catch (e) {
      try {
        const listed = await listMCPTools(tool.toolId)
        mcpToolsByToolId.value[tool.toolId] = listed
        mcpServerRunningByToolId.value[tool.toolId] = listed.length > 0
        mcpToolsErrorByToolId.value[tool.toolId] = listed.length === 0
          ? (e instanceof Error ? e.message : '获取 MCP tools 失败')
          : ''
      } catch {
        mcpToolsByToolId.value[tool.toolId] = []
        mcpServerRunningByToolId.value[tool.toolId] = false
        mcpToolsErrorByToolId.value[tool.toolId] = e instanceof Error ? e.message : '获取 MCP tools 失败'
      }
    } finally {
      mcpToolsLoadingByToolId.value[tool.toolId] = false
    }
  }))
}

function openCreateModal() {
  if (!canManageOwnTools()) {
    error.value = '当前角色无权创建工具'
    return
  }
  console.log('[ToolPage] openCreateModal called')
  error.value = ''
  fieldErrors.value = {}
  editingTool.value = null
  form.value = {
    toolId: '',
    name: '',
    description: '',
    toolType: 'http',
    config: defaultHTTPConfig(),
    parameters: [],
    outputParameters: [],
  }
  showModal.value = true
  revealEditor()
  console.log('[ToolPage] showModal set to true:', showModal.value)
}

function openEditModal(tool: UserTool) {
  if (!canManageTool(tool)) {
    error.value = '当前角色无权编辑该工具'
    return
  }
  console.log('[ToolPage] openEditModal called with tool:', tool)
  const cfg = (tool.config || {}) as Record<string, unknown>
  const normalizedConfig: Record<string, unknown> = tool.toolType === 'mcp'
    ? { ...defaultMCPConfig(), ...cfg }
    : { ...defaultHTTPConfig(), ...cfg }

  if (tool.toolType === 'mcp' && String(normalizedConfig.mcp_mode || 'url') === 'stdio') {
    const servers = (normalizedConfig.mcp_servers || (normalizedConfig as any).mcpServers) as Record<string, unknown> | undefined
    if (servers && typeof servers === 'object') {
      ;(normalizedConfig as any).mcp_servers_json = JSON.stringify({ mcpServers: servers }, null, 2)
    }
  }

  editingTool.value = tool
  fieldErrors.value = {}
  form.value = {
    toolId: tool.toolId,
    name: tool.name,
    description: tool.description,
    toolType: tool.toolType,
    config: normalizedConfig as any,
    parameters: tool.parameters || [],
    outputParameters: (tool as any).outputParameters || [],
  }
  showModal.value = true
  revealEditor()
}

function handleToolTypeChange(nextType: string) {
  if (nextType === 'mcp') {
    form.value.config = {
      ...defaultMCPConfig(),
      ...(form.value.config as any),
    }
    return
  }
  form.value.config = {
    ...defaultHTTPConfig(),
    ...(form.value.config as any),
  }
}

function setFieldError(key: string, message: string) {
  fieldErrors.value[key] = message
}

function clearFieldErrors() {
  fieldErrors.value = {}
}

async function handleSubmit() {
  if (editingTool.value && !canManageTool(editingTool.value)) {
    error.value = '当前角色无权保存该工具'
    return
  }
  if (!editingTool.value && !canManageOwnTools()) {
    error.value = '当前角色无权创建工具'
    return
  }
  console.log('[ToolPage] handleSubmit called')
  console.log('[ToolPage] form value:', form.value)
  error.value = ''
  clearFieldErrors()
  if (!form.value.toolId.trim()) {
    console.warn('[ToolPage] validation failed: toolId is empty')
    setFieldError('toolId', '工具ID未填写')
    return
  }
  if (!form.value.name.trim()) {
    console.warn('[ToolPage] validation failed: name is empty')
    setFieldError('name', '工具名称未填写')
    return
  }
  if (form.value.toolType === 'http' && !String((form.value.config as any).url || '').trim()) {
    console.warn('[ToolPage] validation failed: url is empty')
    setFieldError('http_url', 'HTTP 请求URL未填写')
    return
  }
  if (form.value.toolType === 'mcp') {
    const mode = String((form.value.config as any).mcp_mode || 'url').trim()
    if (mode === 'url' && !String((form.value.config as any).server_url || '').trim()) {
      console.warn('[ToolPage] validation failed: mcp server_url is empty')
      setFieldError('mcp_server_url', '云端 MCP URL未填写')
      return
    }
    if (mode === 'stdio') {
      const raw = String((form.value.config as any).mcp_servers_json || '').trim()
      if (!raw) {
        setFieldError('mcp_servers_json', '本地 MCP mcpServers JSON未填写')
        return
      }
      try {
        const parsed = JSON.parse(raw)
        const servers = parsed?.mcpServers ?? parsed?.mcp_servers ?? parsed
        if (!servers || typeof servers !== 'object') {
          throw new Error('mcpServers 结构错误')
        }
        const nextConfig = {
          ...(form.value.config as any),
          mcp_servers: servers,
        }
        const keys = Object.keys(servers as Record<string, unknown>)
        if (!String(nextConfig.server_name || '').trim() && keys.length === 1) {
          nextConfig.server_name = keys[0]
        }
        delete nextConfig.mcp_servers_json
        form.value.config = nextConfig
      } catch (err) {
        setFieldError('mcp_servers_json', err instanceof Error ? err.message : 'mcpServers JSON 解析失败')
        return
      }
    }
  }
  try {
    if (editingTool.value) {
      console.log('[ToolPage] updating existing tool:', form.value.toolId)
      await updateUserTool(form.value.toolId, {
        name: form.value.name,
        description: form.value.description,
        config: form.value.config,
        parameters: form.value.parameters,
      })
      console.log('[ToolPage] tool updated successfully')
    } else {
      console.log('[ToolPage] creating new tool:', form.value.toolId)
      await createUserTool({
        toolId: form.value.toolId,
        name: form.value.name,
        description: form.value.description,
        toolType: form.value.toolType,
        config: form.value.config,
        parameters: form.value.parameters,
      })
      console.log('[ToolPage] tool created successfully')
    }
    showModal.value = false
    await loadTools()
  } catch (e) {
    console.error('[ToolPage] handleSubmit error:', e)
    error.value = e instanceof Error ? e.message : '保存失败'
  }
}

async function handleStartMCP(tool: UserTool) {
  if (!canManageTool(tool)) {
    error.value = '当前角色无权操作该工具'
    return
  }
  mcpToolsLoadingByToolId.value[tool.toolId] = true
  mcpToolsErrorByToolId.value[tool.toolId] = ''
  try {
    const result = await startMCPServer(tool.toolId)
    mcpToolsByToolId.value[tool.toolId] = Array.isArray(result.tools) ? result.tools : []
    mcpServerRunningByToolId.value[tool.toolId] = !!result.started
  } catch (e) {
    mcpToolsByToolId.value[tool.toolId] = []
    mcpServerRunningByToolId.value[tool.toolId] = false
    mcpToolsErrorByToolId.value[tool.toolId] = e instanceof Error ? e.message : '启动 MCP 失败'
  } finally {
    mcpToolsLoadingByToolId.value[tool.toolId] = false
  }
}

async function handleStopMCP(tool: UserTool) {
  if (!canManageTool(tool)) {
    error.value = '当前角色无权操作该工具'
    return
  }
  mcpToolsLoadingByToolId.value[tool.toolId] = true
  mcpToolsErrorByToolId.value[tool.toolId] = ''
  try {
    await stopMCPServer(tool.toolId)
    mcpServerRunningByToolId.value[tool.toolId] = false
    mcpToolsByToolId.value[tool.toolId] = []
  } catch (e) {
    mcpToolsErrorByToolId.value[tool.toolId] = e instanceof Error ? e.message : '停止 MCP 失败'
  } finally {
    mcpToolsLoadingByToolId.value[tool.toolId] = false
  }
}

async function handleDelete(toolId: string) {
  const target = tools.value.find((item) => item.toolId === toolId)
  if (target && !canManageTool(target)) {
    error.value = '当前角色无权删除该工具'
    return
  }
  if (!confirm('确定要删除这个工具吗？')) return
  try {
    await deleteUserTool(toolId)
    await loadTools()
  } catch (e) {
    error.value = e instanceof Error ? e.message : '删除失败'
  }
}

function addParameter() {
  if (!newParam.value.name.trim()) {
    setFieldError('new_param_name', '参数名未填写')
    return
  }
  form.value.parameters.push({ ...newParam.value, name: newParam.value.name.trim() })
  newParam.value = { name: '', type: 'string', required: false, description: '' }
  delete fieldErrors.value.new_param_name
}

function removeParameter(index: number) {
  form.value.parameters.splice(index, 1)
}

function addOutputParameter() {
  if (!newOutputParam.value.name.trim()) {
    setFieldError('new_output_param_name', '输出参数名未填写')
    return
  }
  form.value.outputParameters.push({ ...newOutputParam.value, name: newOutputParam.value.name.trim() })
  newOutputParam.value = { name: '', type: 'string', required: false, description: '' }
  delete fieldErrors.value.new_output_param_name
}

function removeOutputParameter(index: number) {
  form.value.outputParameters.splice(index, 1)
}

onMounted(() => {
  loadTools()
})
</script>

<template>
  <PageContainer mode="wide">
  <div class="module-page module-page--tool tool-page tool-hub">
    <PageHeader eyebrow="TOOL HUB" title="工具管理" description="统一查看工具定义、参数与 MCP 运行状态。">
      <template #actions>
        <button v-if="!readOnlyMode" type="button" class="btn-primary" @click="openCreateModal">+ 创建工具</button>
      </template>
    </PageHeader>
    <p v-if="readOnlyMode" class="hint">当前角色 {{ roleLabel }}：只读模式</p>

    <section class="tool-overview-grid tool-hub__overview-grid">
      <StatCard v-for="item in toolStats" :key="item.label" :label="item.label" :value="item.value" />
    </section>

    <div v-if="error" class="error">{{ error }}</div>

    <div v-if="loading" class="loading">加载中...</div>

    <ModuleSectionCard v-else title="工具列表" description="当前账号可见的工具资源中心。">
      <div class="tool-list tool-hub__grid">
      <div v-if="tools.length === 0" class="empty">
        暂无工具，点击"创建工具"添加新工具
      </div>

      <div v-for="tool in tools" :key="tool.toolId" class="tool-card tool-hub__card">
        <div class="tool-header tool-hub__card-header">
          <div class="tool-hub__title-wrap">
            <div class="tool-hub__badge-row">
              <span class="tool-type-badge" :class="tool.toolType">{{ tool.toolType.toUpperCase() }}</span>
              <span v-if="isSystemTool(tool)" class="tool-owner-badge">SYSTEM</span>
            </div>
            <h3>{{ tool.name }}</h3>
          </div>
          <div class="tool-actions tool-hub__actions">
            <button
              v-if="canManageTool(tool) && tool.toolType === 'mcp' && ((tool.config as any).mcp_mode || 'url') === 'stdio'"
              v-show="!mcpServerRunningByToolId[tool.toolId]"
              type="button"
              class="btn-secondary"
              @click="handleStartMCP(tool)"
            >
              启动
            </button>
            <button
              v-if="canManageTool(tool) && tool.toolType === 'mcp' && ((tool.config as any).mcp_mode || 'url') === 'stdio'"
              v-show="mcpServerRunningByToolId[tool.toolId]"
              type="button"
              class="btn-secondary"
              @click="handleStopMCP(tool)"
            >
              停止
            </button>
            <button v-if="canManageTool(tool)" type="button" class="btn-secondary" @click="openEditModal(tool)">编辑</button>
            <button v-if="canManageTool(tool)" type="button" class="btn-danger" @click="handleDelete(tool.toolId)">删除</button>
          </div>
        </div>

        <div class="tool-hub__section tool-hub__section--description">
          <p class="tool-description">{{ tool.description || '暂无描述' }}</p>
        </div>

        <div class="tool-hub__section tool-hub__section--info">
          <div class="tool-info tool-hub__info-list">
            <span v-for="line in baseInfoLines(tool)" :key="line">{{ line }}</span>
          </div>
        </div>

        <div class="tool-hub__section tool-hub__section--tags">
          <div v-if="tool.toolType === 'mcp'" class="mcp-tool-list">
          <div class="mcp-tool-list-title">MCP 支持工具</div>
          <div v-if="mcpToolsLoadingByToolId[tool.toolId]" class="mcp-tool-list-hint">正在加载...</div>
          <div v-else-if="mcpToolsErrorByToolId[tool.toolId]" class="mcp-tool-list-error">
            {{ mcpToolsErrorByToolId[tool.toolId] }}
          </div>
          <div v-else-if="(mcpToolsByToolId[tool.toolId] || []).length === 0" class="mcp-tool-list-hint">
            {{ ((tool.config as any).mcp_mode || 'url') === 'stdio' ? '暂无 tools，可尝试重新启动' : '未发现可用 tools' }}
          </div>
          <div v-else class="mcp-tool-tags">
            <span
              v-for="mcpTool in mcpToolsByToolId[tool.toolId]"
              :key="`${tool.toolId}-${mcpTool.name}`"
              class="mcp-tool-tag"
              :title="mcpTool.description || mcpTool.name"
            >
              {{ mcpTool.name }}
            </span>
          </div>
          </div>
          <div v-else class="mcp-tool-tags">
            <span class="mcp-tool-tag">HTTP 接口</span>
            <span class="mcp-tool-tag">请求工具</span>
          </div>
        </div>
      </div>
      </div>
    </ModuleSectionCard>

    <div v-if="showModal" ref="inlineEditorRef" class="inline-editor">
      <div class="modal">
        <h2>{{ editingTool ? '编辑工具' : '创建工具' }}</h2>

        <div class="form-group">
          <label>工具ID</label>
          <input v-model="form.toolId" :disabled="!!editingTool" placeholder="例如: weather_api" />
          <p v-if="fieldErrors.toolId" class="field-error">{{ fieldErrors.toolId }}</p>
        </div>

        <div class="form-group">
          <label>工具名称</label>
          <input v-model="form.name" placeholder="例如: 天气查询" />
          <p v-if="fieldErrors.name" class="field-error">{{ fieldErrors.name }}</p>
        </div>

        <div class="form-group">
          <label>描述</label>
          <textarea v-model="form.description" placeholder="工具功能描述"></textarea>
        </div>

        <div class="form-group">
          <label>工具类型</label>
          <select v-model="form.toolType" @change="handleToolTypeChange(form.toolType)">
            <option value="http">HTTP请求</option>
            <option value="mcp">MCP工具</option>
          </select>
        </div>

        <template v-if="form.toolType === 'http'">
          <div class="form-group">
            <label>请求方法</label>
            <select v-model="form.config.method">
              <option value="GET">GET</option>
              <option value="POST">POST</option>
              <option value="PUT">PUT</option>
              <option value="DELETE">DELETE</option>
            </select>
          </div>

          <div class="form-group">
            <label>请求URL</label>
            <input v-model="form.config.url" placeholder="https://api.example.com/data?key={{param}}" />
            <small>支持变量替换: {"param"}</small>
            <p v-if="fieldErrors.http_url" class="field-error">{{ fieldErrors.http_url }}</p>
          </div>
        </template>

        <template v-else-if="form.toolType === 'mcp'">
          <div class="form-group">
            <label>MCP 模式</label>
            <select v-model="(form.config as any).mcp_mode">
              <option value="url">云端 MCP (URL)</option>
              <option value="stdio">本地 MCP (STDIO)</option>
            </select>
          </div>

          <div v-if="(form.config as any).mcp_mode !== 'stdio'" class="form-group">
            <label>云端 MCP URL</label>
            <input v-model="(form.config as any).server_url" placeholder="https://mcp.example.com/sse" />
            <p v-if="fieldErrors.mcp_server_url" class="field-error">{{ fieldErrors.mcp_server_url }}</p>
          </div>

          <div v-else class="form-group">
            <label>本地 MCP Server 配置 (JSON)</label>
            <textarea
              v-model="(form.config as any).mcp_servers_json"
              placeholder='{
  "mcpServers": {
    "fetch": {
      "command": "uvx",
      "args": ["mcp-server-fetch"]
    }
  }
}'
            ></textarea>
            <small>仅需提供 mcpServers JSON 配置</small>
            <p v-if="fieldErrors.mcp_servers_json" class="field-error">{{ fieldErrors.mcp_servers_json }}</p>
          </div>

          <div v-if="(form.config as any).mcp_mode === 'stdio'" class="form-group">
            <label>Server Key (可选)</label>
            <input v-model="(form.config as any).server_name" placeholder="例如: fetch" />
          </div>

          <div class="form-group">
            <label>MCP 工具名</label>
            <input v-model="(form.config as any).tool_name" placeholder="例如: maps_text_search" />
            <small v-if="editingTool && mcpToolsByToolId[editingTool.toolId]?.length">
              当前服务可用: {{ mcpToolsByToolId[editingTool.toolId].map(t => t.name).join(', ') }}
            </small>
          </div>
        </template>

        <div class="form-group">
          <label>参数定义</label>
          <div class="param-input">
            <input class="param-name-input" v-model="newParam.name" placeholder="参数名（例如 query）" />
            <select v-model="newParam.type">
              <option value="string">字符串</option>
            </select>
            <label class="param-required-toggle">
              <input type="checkbox" v-model="newParam.required" />
              <span>必填</span>
            </label>
            <button type="button" @click="addParameter">添加</button>
          </div>
          <p v-if="fieldErrors.new_param_name" class="field-error">{{ fieldErrors.new_param_name }}</p>
          <input v-model="newParam.description" placeholder="参数描述" />
          <div class="param-list">
            <div v-for="(param, index) in form.parameters" :key="index" class="param-item">
              <span>{{ param.name }} ({{ param.type }}{{ param.required ? ', 必填' : '' }})</span>
              <span class="param-desc">{{ param.description }}</span>
              <button type="button" @click="removeParameter(index)">×</button>
            </div>
          </div>
        </div>

        <div class="form-group">
          <label>输出参数定义</label>
          <div class="param-input">
            <input class="param-name-input" v-model="newOutputParam.name" placeholder="输出参数名（例如 result）" />
            <select v-model="newOutputParam.type">
              <option value="string">字符串</option>
            </select>
            <label class="param-required-toggle">
              <input type="checkbox" v-model="newOutputParam.required" />
              <span>必填</span>
            </label>
            <button type="button" @click="addOutputParameter">添加</button>
          </div>
          <p v-if="fieldErrors.new_output_param_name" class="field-error">{{ fieldErrors.new_output_param_name }}</p>
          <input v-model="newOutputParam.description" placeholder="输出参数描述" />
          <div class="param-list">
            <div v-for="(param, index) in form.outputParameters" :key="index" class="param-item">
              <span>{{ param.name }} ({{ param.type }}{{ param.required ? ', 必填' : '' }})</span>
              <span class="param-desc">{{ param.description }}</span>
              <button type="button" @click="removeOutputParameter(index)">×</button>
            </div>
          </div>
        </div>

        <div class="modal-actions">
          <button type="button" class="btn-secondary" @click="showModal = false">取消</button>
          <button type="button" class="btn-primary" @click="handleSubmit">保存</button>
        </div>
      </div>
    </div>
  </div>
  </PageContainer>
</template>

<style scoped>
.tool-page {
  width: 100%;
  margin: 0;
  display: grid;
  gap: 14px;
}

.tool-overview-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 12px;
}

.tool-hub__overview-grid :deep(.stat-card) {
  min-height: 116px;
  border-radius: 16px;
  background: linear-gradient(180deg, rgba(255, 255, 255, 0.96), rgba(248, 249, 251, 0.92));
}

.header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 16px;
  gap: 12px;
}

.header h1 {
  margin: 0;
  font-family: var(--font-display);
  font-size: 24px;
}

.btn-primary {
  border: 1px solid transparent;
  border-radius: 12px;
  padding: 10px 16px;
  background: var(--accent-green);
  color: #193229;
  font-weight: 700;
  cursor: pointer;
  transition: transform 0.2s ease, box-shadow 0.2s ease, filter 0.2s ease;
}

.btn-primary:hover {
  transform: translateY(-1px);
  box-shadow: var(--shadow-soft);
  filter: brightness(0.97);
}

.btn-secondary {
  border: 1px solid var(--line);
  border-radius: 10px;
  padding: 8px 14px;
  background: #ffffff;
  color: var(--text-main);
  cursor: pointer;
  transition: background 0.2s ease, transform 0.2s ease;
}

.btn-secondary:hover {
  background: var(--bg-soft);
  transform: translateY(-1px);
}

.btn-danger {
  border: 1px solid #efc8cb;
  border-radius: 10px;
  padding: 8px 14px;
  background: #fff4f4;
  color: #9f2a30;
  cursor: pointer;
  transition: background 0.2s ease, transform 0.2s ease;
}

.btn-danger:hover {
  background: #ffe9ea;
  transform: translateY(-1px);
}

.error {
  background: #fff0f0;
  color: #b1262f;
  padding: 10px 12px;
  border: 1px solid #f2c6cb;
  border-radius: 10px;
  margin-bottom: 12px;
}

.loading {
  text-align: center;
  padding: 36px;
  color: var(--text-muted);
}

.hint {
  margin: 0 0 12px;
  color: var(--text-muted);
  font-size: 13px;
}

.empty {
  text-align: center;
  padding: 36px;
  color: var(--text-muted);
  border: 1px dashed var(--line);
  border-radius: 14px;
  background: #fff;
}

.tool-list {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 16px;
}

.tool-card {
  background:
    linear-gradient(180deg, rgba(255, 255, 255, 0.94), rgba(255, 255, 255, 0.84)),
    var(--bg-panel);
  border: 1px solid var(--line);
  border-radius: 20px;
  padding: 18px;
  box-shadow: var(--shadow-soft);
  transition: transform 0.2s ease, box-shadow 0.2s ease;
  display: grid;
  grid-template-rows: auto 72px 88px auto;
  gap: 14px;
  min-height: 420px;
}

.tool-card:hover {
  transform: translateY(-2px);
  box-shadow: var(--shadow-medium);
}

.tool-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 14px;
}

.tool-hub__title-wrap {
  display: grid;
  gap: 10px;
  min-width: 0;
}

.tool-hub__badge-row {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.tool-type-badge {
  background: var(--bg-soft);
  border: 1px solid var(--line);
  padding: 3px 8px;
  border-radius: 999px;
  font-size: 12px;
  font-weight: 700;
}

.tool-type-badge.http {
  background: #ebf7f2;
  border-color: #b7e2d0;
  color: #245748;
}

.tool-type-badge.mcp {
  background: #f1edfb;
  border-color: #cfc2ef;
  color: #51407a;
}

.tool-header h3 {
  margin: 0;
  font-size: 18px;
  line-height: 1.25;
}

.tool-owner-badge {
  background: #fff6e8;
  border: 1px solid #f1d2a7;
  color: #8a5b19;
  border-radius: 999px;
  padding: 2px 8px;
  font-size: 11px;
  font-weight: 700;
}

.tool-description {
  color: var(--text-muted);
  font-size: 14px;
  line-height: 1.7;
  margin: 0;
}

.tool-info {
  font-size: 12px;
  color: var(--text-muted);
}

.tool-hub__info-list {
  display: grid;
  gap: 8px;
}

.tool-hub__info-list span {
  display: flex;
  align-items: center;
  min-height: 34px;
  padding: 0 12px;
  border-radius: 12px;
  border: 1px solid rgba(221, 225, 230, 0.94);
  background: rgba(255, 255, 255, 0.72);
  margin: 0;
}

.tool-actions {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
  justify-content: flex-end;
}

.tool-hub__section {
  min-height: 0;
}

.tool-hub__section--tags {
  display: flex;
  align-items: flex-end;
}

.mcp-tool-list {
  display: grid;
  gap: 8px;
  align-content: start;
}

.mcp-tool-list-title {
  font-size: 12px;
  color: var(--text-muted);
  margin-bottom: 6px;
}

.mcp-tool-list-hint {
  font-size: 12px;
  color: var(--text-muted);
}

.mcp-tool-list-error {
  font-size: 12px;
  color: #b1262f;
}

.mcp-tool-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.mcp-tool-tag {
  font-size: 11px;
  line-height: 1;
  padding: 6px 8px;
  border-radius: 999px;
  background: linear-gradient(120deg, rgba(204, 232, 220, 0.38), rgba(226, 216, 246, 0.32));
  border: 1px solid #d6e0fb;
  color: #2f4b86;
}

.inline-editor {
  margin-top: 14px;
}

.modal {
  background: #fff;
  border: 1px solid var(--line);
  border-radius: 16px;
  padding: 24px;
  width: 100%;
  max-width: 800px;
  max-height: 70vh;
  overflow-y: auto;
  box-shadow: var(--shadow-medium);
}

.modal h2 {
  margin: 0 0 20px 0;
  font-family: var(--font-display);
}

.form-group {
  margin-bottom: 16px;
}

.form-group label {
  display: block;
  margin-bottom: 6px;
  font-weight: 500;
}

.form-group input,
.form-group select,
.form-group textarea {
  width: 100%;
  padding: 10px 12px;
  border: 1px solid #d8dee1;
  border-radius: 10px;
  font-size: 14px;
  background: #fbfcfd;
  transition: border-color 0.2s ease, box-shadow 0.2s ease;
}

.form-group input:focus,
.form-group select:focus,
.form-group textarea:focus {
  outline: none;
  border-color: #b8c0f0;
  box-shadow: 0 0 0 3px rgba(184, 192, 240, 0.22);
}

.form-group textarea {
  min-height: 60px;
}

.form-group small {
  color: var(--text-muted);
  font-size: 12px;
}

.field-error {
  margin: 6px 0 0;
  color: #b1262f;
  font-size: 12px;
}

.header-input,
.param-input {
  display: flex;
  gap: 8px;
  margin-bottom: 8px;
}

.header-input input,
.param-input input {
  flex: 1;
}

.param-name-input {
  min-width: 190px;
}

.param-required-toggle {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  flex: none;
  padding: 0 4px;
  font-size: 13px;
  color: var(--text-main);
  white-space: nowrap;
}

.param-required-toggle input[type='checkbox'] {
  width: auto;
  flex: none;
  margin: 0;
  padding: 0;
}

.header-input button,
.param-input button {
  padding: 8px 16px;
}

.header-list,
.param-list {
  margin-top: 8px;
}

.header-item,
.param-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 7px 9px;
  background: #f9fafb;
  border: 1px solid #edf1f3;
  border-radius: 8px;
  margin-bottom: 4px;
}

.header-item button,
.param-item button {
  background: none;
  border: none;
  color: #87919b;
  cursor: pointer;
  font-size: 16px;
}

.param-desc {
  color: #87919b;
  font-size: 12px;
}

.modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 12px;
  margin-top: 20px;
}

@media (max-width: 960px) {
  .header {
    flex-direction: column;
    align-items: flex-start;
  }

  .tool-list {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .tool-overview-grid {
    grid-template-columns: 1fr;
  }
}

@media (max-width: 720px) {
  .tool-list {
    grid-template-columns: 1fr;
  }

  .tool-card {
    grid-template-rows: auto auto auto auto;
    min-height: auto;
  }

  .tool-header {
    flex-direction: column;
  }

  .tool-actions {
    justify-content: flex-start;
  }
}
</style>
