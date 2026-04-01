<script setup lang="ts">
import { ref } from 'vue'
import {
  testWorkflow,
  testUserAgent,
  publishUserAgent,
  stopUserAgent,
  restartUserAgent,
  type ExecutionResult,
  type UserAgent,
} from '../lib/userAgentApi'
import type { WorkflowDefinition } from '../types/workflow'

const props = defineProps<{
  agent?: UserAgent
  workflowDef?: WorkflowDefinition
}>()

const emit = defineEmits<{
  (e: 'published', agent: UserAgent): void
  (e: 'stopped', agentId: string): void
}>()

const testInput = ref('{\n  "query": "测试输入"\n}')
const testResult = ref<ExecutionResult | null>(null)
const testing = ref(false)
const publishing = ref(false)
const error = ref('')

async function handleTest() {
  error.value = ''
  testResult.value = null
  testing.value = true

  try {
    let input: Record<string, unknown> = {}
    if (testInput.value.trim()) {
      input = JSON.parse(testInput.value)
    }

    if (props.agent) {
      testResult.value = await testUserAgent(props.agent.agentId, input)
    } else if (props.workflowDef) {
      testResult.value = await testWorkflow({
        workflowDef: props.workflowDef,
        input,
      })
    } else {
      throw new Error('没有可测试的Agent或工作流')
    }
  } catch (e) {
    error.value = e instanceof Error ? e.message : '测试失败'
  } finally {
    testing.value = false
  }
}

async function handlePublish() {
  if (!props.agent) return

  error.value = ''
  publishing.value = true

  try {
    const result = await publishUserAgent(props.agent.agentId)
    emit('published', result)
  } catch (e) {
    error.value = e instanceof Error ? e.message : '发布失败'
  } finally {
    publishing.value = false
  }
}

async function handleStop() {
  if (!props.agent) return

  error.value = ''

  try {
    await stopUserAgent(props.agent.agentId)
    emit('stopped', props.agent.agentId)
  } catch (e) {
    error.value = e instanceof Error ? e.message : '停止失败'
  }
}

async function handleRestart() {
  if (!props.agent) return

  error.value = ''

  try {
    await restartUserAgent(props.agent.agentId)
  } catch (e) {
    error.value = e instanceof Error ? e.message : '重启失败'
  }
}

function getStateClass(state: string): string {
  switch (state) {
    case 'succeeded':
      return 'state-success'
    case 'failed':
      return 'state-error'
    case 'running':
      return 'state-running'
    default:
      return ''
  }
}
</script>

<template>
  <div class="agent-tester">
    <div class="test-section">
      <h3>测试Agent</h3>
      
      <div class="input-group">
        <label>输入参数 (JSON)</label>
        <textarea
          v-model="testInput"
          rows="6"
          placeholder='{"query": "测试输入"}'
        ></textarea>
      </div>

      <div class="button-group">
        <button class="btn-test" @click="handleTest" :disabled="testing">
          {{ testing ? '测试中...' : '运行测试' }}
        </button>

        <template v-if="agent">
          <button
            v-if="agent.status === 'draft'"
            class="btn-publish"
            @click="handlePublish"
            :disabled="publishing"
          >
            {{ publishing ? '发布中...' : '发布Agent' }}
          </button>

          <button
            v-if="agent.status === 'published'"
            class="btn-stop"
            @click="handleStop"
          >
            停止运行
          </button>

          <button
            v-if="agent.status === 'published'"
            class="btn-restart"
            @click="handleRestart"
          >
            重启
          </button>
        </template>
      </div>
    </div>

    <div v-if="error" class="error-message">
      {{ error }}
    </div>

    <div v-if="testResult" class="result-section">
      <h3>执行结果</h3>
      
      <div class="result-header">
        <span class="run-id">Run ID: {{ testResult.runId }}</span>
        <span class="state-badge" :class="getStateClass(testResult.state)">
          {{ testResult.state }}
        </span>
      </div>

      <div v-if="testResult.error" class="result-error">
        {{ testResult.error }}
      </div>

      <div v-if="testResult.output" class="result-output">
        <h4>输出</h4>
        <pre>{{ JSON.stringify(testResult.output, null, 2) }}</pre>
      </div>

      <div v-if="testResult.nodeResults?.length" class="node-results">
        <h4>节点执行结果</h4>
        <div
          v-for="node in testResult.nodeResults"
          :key="node.nodeId"
          class="node-result"
        >
          <div class="node-header">
            <span class="node-id">{{ node.nodeId }}</span>
            <span class="node-type">({{ node.nodeType }})</span>
            <span class="state-badge small" :class="getStateClass(node.state)">
              {{ node.state }}
            </span>
            <span class="duration">{{ node.duration }}ms</span>
          </div>
          <div v-if="node.error" class="node-error">{{ node.error }}</div>
          <div v-if="node.output" class="node-output">
            <pre>{{ JSON.stringify(node.output, null, 2) }}</pre>
          </div>
        </div>
      </div>
    </div>

    <div v-if="agent" class="agent-info">
      <h3>Agent信息</h3>
      <div class="info-grid">
        <div class="info-item">
          <span class="label">状态</span>
          <span class="value status-badge" :class="agent.status">{{ agent.status }}</span>
        </div>
        <div class="info-item">
          <span class="label">进程状态</span>
          <span class="value">{{ agent.processStatus || '-' }}</span>
        </div>
        <div class="info-item">
          <span class="label">端口</span>
          <span class="value">{{ agent.port || '-' }}</span>
        </div>
        <div class="info-item">
          <span class="label">PID</span>
          <span class="value">{{ agent.processPid || '-' }}</span>
        </div>
        <div class="info-item">
          <span class="label">发布时间</span>
          <span class="value">{{ agent.publishedAt || '-' }}</span>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.agent-tester {
  padding: 20px;
  background: #f9f9f9;
  border-radius: 8px;
}

.test-section,
.result-section,
.agent-info {
  background: white;
  border-radius: 8px;
  padding: 16px;
  margin-bottom: 16px;
}

h3 {
  margin: 0 0 16px 0;
  font-size: 18px;
  color: #333;
}

h4 {
  margin: 16px 0 8px 0;
  font-size: 14px;
  color: #666;
}

.input-group {
  margin-bottom: 16px;
}

.input-group label {
  display: block;
  margin-bottom: 6px;
  font-weight: 500;
  color: #555;
}

.input-group textarea {
  width: 100%;
  padding: 10px;
  border: 1px solid #ddd;
  border-radius: 4px;
  font-family: monospace;
  font-size: 13px;
  resize: vertical;
}

.button-group {
  display: flex;
  gap: 10px;
}

.btn-test {
  background: #4a90d9;
  color: white;
  border: none;
  padding: 10px 20px;
  border-radius: 6px;
  cursor: pointer;
  font-size: 14px;
}

.btn-test:hover:not(:disabled) {
  background: #3a7bc8;
}

.btn-test:disabled {
  background: #ccc;
  cursor: not-allowed;
}

.btn-publish {
  background: #27ae60;
  color: white;
  border: none;
  padding: 10px 20px;
  border-radius: 6px;
  cursor: pointer;
  font-size: 14px;
}

.btn-publish:hover:not(:disabled) {
  background: #219a52;
}

.btn-stop {
  background: #e74c3c;
  color: white;
  border: none;
  padding: 10px 20px;
  border-radius: 6px;
  cursor: pointer;
  font-size: 14px;
}

.btn-stop:hover {
  background: #c0392b;
}

.btn-restart {
  background: #f39c12;
  color: white;
  border: none;
  padding: 10px 20px;
  border-radius: 6px;
  cursor: pointer;
  font-size: 14px;
}

.btn-restart:hover {
  background: #d68910;
}

.error-message {
  background: #fee;
  color: #c00;
  padding: 12px;
  border-radius: 4px;
  margin-bottom: 16px;
}

.result-header {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 12px;
}

.run-id {
  font-family: monospace;
  font-size: 12px;
  color: #888;
}

.state-badge {
  padding: 4px 12px;
  border-radius: 12px;
  font-size: 12px;
  font-weight: 500;
}

.state-badge.state-success {
  background: #d4edda;
  color: #155724;
}

.state-badge.state-error {
  background: #f8d7da;
  color: #721c24;
}

.state-badge.state-running {
  background: #cce5ff;
  color: #004085;
}

.state-badge.small {
  padding: 2px 8px;
  font-size: 11px;
}

.result-error {
  background: #f8d7da;
  color: #721c24;
  padding: 10px;
  border-radius: 4px;
  margin-bottom: 12px;
}

.result-output pre,
.node-output pre {
  background: #f5f5f5;
  padding: 10px;
  border-radius: 4px;
  overflow-x: auto;
  font-size: 12px;
  margin: 0;
}

.node-results {
  margin-top: 16px;
}

.node-result {
  border: 1px solid #e0e0e0;
  border-radius: 4px;
  padding: 10px;
  margin-bottom: 8px;
}

.node-header {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 6px;
}

.node-id {
  font-weight: 500;
  color: #333;
}

.node-type {
  color: #888;
  font-size: 12px;
}

.duration {
  color: #888;
  font-size: 12px;
  margin-left: auto;
}

.node-error {
  color: #c00;
  font-size: 13px;
  padding: 6px;
  background: #fee;
  border-radius: 4px;
}

.agent-info .info-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
  gap: 12px;
}

.info-item {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.info-item .label {
  font-size: 12px;
  color: #888;
}

.info-item .value {
  font-size: 14px;
  color: #333;
}

.status-badge {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 12px;
}

.status-badge.draft {
  background: #f0f0f0;
  color: #666;
}

.status-badge.published {
  background: #d4edda;
  color: #155724;
}

.status-badge.stopped {
  background: #f8d7da;
  color: #721c24;
}
</style>
