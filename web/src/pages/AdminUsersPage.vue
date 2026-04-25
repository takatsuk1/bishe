<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { listAdminUsers, updateAdminUserRoles, updateAdminUserStatus, type AdminUserItem } from '../lib/adminApi'
import { currentPrimaryRole } from '../lib/permission'

const loading = ref(false)
const error = ref('')
const users = ref<AdminUserItem[]>([])
const roleDraft = ref<Record<string, string>>({})
const savingByUser = ref<Record<string, boolean>>({})

const roleOptions = ['viewer', 'user', 'operator', 'admin']
const roleLabel = computed(() => currentPrimaryRole.value)

async function loadUsers(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const items = await listAdminUsers()
    users.value = items
    const draft: Record<string, string> = {}
    items.forEach((item) => {
      draft[item.userId] = item.primaryRole || item.roles[0] || 'viewer'
    })
    roleDraft.value = draft
  } catch (err) {
    error.value = err instanceof Error ? err.message : '加载用户失败'
  } finally {
    loading.value = false
  }
}

function roleText(item: AdminUserItem): string {
  if (!Array.isArray(item.roles) || item.roles.length === 0) {
    return 'viewer'
  }
  return item.roles.join(' / ')
}

async function toggleStatus(item: AdminUserItem): Promise<void> {
  const nextEnabled = Number(item.status) !== 1
  savingByUser.value = { ...savingByUser.value, [item.userId]: true }
  error.value = ''
  try {
    await updateAdminUserStatus(item.userId, nextEnabled)
    item.status = nextEnabled ? 1 : 0
  } catch (err) {
    error.value = err instanceof Error ? err.message : '更新状态失败'
  } finally {
    const next = { ...savingByUser.value }
    delete next[item.userId]
    savingByUser.value = next
  }
}

async function saveRole(item: AdminUserItem): Promise<void> {
  const role = roleDraft.value[item.userId] || item.primaryRole || 'viewer'
  savingByUser.value = { ...savingByUser.value, [item.userId]: true }
  error.value = ''
  try {
    await updateAdminUserRoles(item.userId, [role])
    item.roles = [role]
    item.primaryRole = role
  } catch (err) {
    error.value = err instanceof Error ? err.message : '更新角色失败'
  } finally {
    const next = { ...savingByUser.value }
    delete next[item.userId]
    savingByUser.value = next
  }
}

function isSaving(userId: string): boolean {
  return !!savingByUser.value[userId]
}

onMounted(() => {
  loadUsers()
})
</script>

<template>
  <main class="auth-page">
    <section class="auth-card admin-card">
      <h1>用户管理</h1>
      <p class="auth-hint">当前角色：{{ roleLabel }}</p>

      <div class="admin-toolbar">
        <button type="button" class="btn-primary" :disabled="loading" @click="loadUsers">
          {{ loading ? '刷新中...' : '刷新列表' }}
        </button>
      </div>

      <p v-if="error" class="admin-error">{{ error }}</p>
      <p v-else-if="loading" class="auth-hint">加载用户中...</p>

      <div v-else class="admin-table-wrap">
        <table class="admin-table">
          <thead>
            <tr>
              <th>用户名</th>
              <th>显示名</th>
              <th>当前角色</th>
              <th>启用状态</th>
              <th>角色修改</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="item in users" :key="item.userId">
              <td>{{ item.username }}</td>
              <td>{{ item.displayName || '-' }}</td>
              <td>{{ roleText(item) }}</td>
              <td>
                <span class="state-badge" :class="Number(item.status) === 1 ? 'ok' : 'off'">
                  {{ Number(item.status) === 1 ? '启用' : '禁用' }}
                </span>
              </td>
              <td>
                <select v-model="roleDraft[item.userId]">
                  <option v-for="role in roleOptions" :key="role" :value="role">{{ role }}</option>
                </select>
              </td>
              <td class="action-cell">
                <button type="button" class="cancel" :disabled="isSaving(item.userId)" @click="toggleStatus(item)">
                  {{ Number(item.status) === 1 ? '禁用' : '启用' }}
                </button>
                <button type="button" class="send" :disabled="isSaving(item.userId)" @click="saveRole(item)">
                  保存角色
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>
  </main>
</template>

<style scoped>
.admin-card {
  width: min(1080px, 100% - 28px);
}

.admin-toolbar {
  margin-bottom: 12px;
}

.admin-error {
  padding: 10px 12px;
  border-radius: 10px;
  border: 1px solid #f2c6cb;
  background: #fff0f0;
  color: #b1262f;
}

.admin-table-wrap {
  overflow-x: auto;
}

.admin-table {
  width: 100%;
  border-collapse: collapse;
  border: 1px solid var(--line);
  border-radius: 12px;
  overflow: hidden;
}

.admin-table th,
.admin-table td {
  padding: 10px;
  border-bottom: 1px solid var(--line);
  text-align: left;
  vertical-align: middle;
}

.admin-table th {
  background: var(--bg-soft);
  font-weight: 700;
}

.state-badge {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 999px;
  font-size: 12px;
  font-weight: 700;
}

.state-badge.ok {
  background: #ebf7f2;
  border: 1px solid #b7e2d0;
  color: #245748;
}

.state-badge.off {
  background: #fff0f0;
  border: 1px solid #f2c6cb;
  color: #b1262f;
}

.action-cell {
  display: flex;
  gap: 8px;
}

.admin-table select {
  min-width: 120px;
  padding: 6px 8px;
  border: 1px solid #d8dee1;
  border-radius: 8px;
  background: #fff;
}
</style>
