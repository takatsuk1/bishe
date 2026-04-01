<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute } from 'vue-router'
import { changePassword, updateProfile } from '../lib/authApi'
import { currentUser } from '../lib/authStore'
import { canManageUsers, currentRoles } from '../lib/permission'

const displayName = ref(currentUser.value?.displayName || currentUser.value?.username || '')
const currentPassword = ref('')
const newPassword = ref('')
const confirmPassword = ref('')
const profileMessage = ref('')
const passwordMessage = ref('')
const loadingProfile = ref(false)
const loadingPassword = ref(false)
const route = useRoute()

const username = computed(() => currentUser.value?.username || '')
const deniedTip = computed(() => String(route.query.denied || '') === '1')
const rolesText = computed(() => (currentRoles.value.length > 0 ? currentRoles.value.join(' / ') : 'viewer'))

async function saveProfile(): Promise<void> {
  profileMessage.value = ''
  if (!displayName.value.trim()) {
    profileMessage.value = '昵称不能为空'
    return
  }
  loadingProfile.value = true
  try {
    await updateProfile(displayName.value.trim())
    profileMessage.value = '昵称已更新'
  } catch (e) {
    profileMessage.value = e instanceof Error ? e.message : '更新失败'
  } finally {
    loadingProfile.value = false
  }
}

async function savePassword(): Promise<void> {
  passwordMessage.value = ''
  if (!currentPassword.value || !newPassword.value) {
    passwordMessage.value = '请填写完整密码信息'
    return
  }
  if (newPassword.value !== confirmPassword.value) {
    passwordMessage.value = '两次新密码不一致'
    return
  }
  loadingPassword.value = true
  try {
    await changePassword(currentPassword.value, newPassword.value)
    currentPassword.value = ''
    newPassword.value = ''
    confirmPassword.value = ''
    passwordMessage.value = '密码已更新，旧会话刷新令牌已失效'
  } catch (e) {
    passwordMessage.value = e instanceof Error ? e.message : '修改失败'
  } finally {
    loadingPassword.value = false
  }
}
</script>

<template>
  <main class="auth-page">
    <section class="auth-card">
      <h1>个人资料</h1>
      <p class="auth-hint">账号：{{ username }}</p>
      <p class="auth-hint">角色：{{ rolesText }}</p>
      <p class="auth-hint" v-if="canManageUsers()">你拥有管理员用户管理权限。</p>
      <p class="auth-hint" v-if="deniedTip">你当前角色无权访问目标页面，已自动跳转到账号页。</p>

      <div class="auth-form">
        <label>
          昵称
          <input v-model="displayName" type="text" placeholder="输入显示名称" />
        </label>
        <button type="button" class="btn-primary" :disabled="loadingProfile" @click="saveProfile">
          {{ loadingProfile ? '保存中...' : '保存资料' }}
        </button>
        <p v-if="profileMessage" class="auth-hint">{{ profileMessage }}</p>
      </div>

      <hr style="margin: 14px 0; border: none; border-top: 1px solid var(--line);" />

      <div class="auth-form">
        <label>
          当前密码
          <input v-model="currentPassword" type="password" autocomplete="current-password" />
        </label>
        <label>
          新密码
          <input v-model="newPassword" type="password" autocomplete="new-password" />
        </label>
        <label>
          确认新密码
          <input v-model="confirmPassword" type="password" autocomplete="new-password" />
        </label>
        <button type="button" class="btn-primary" :disabled="loadingPassword" @click="savePassword">
          {{ loadingPassword ? '提交中...' : '修改密码' }}
        </button>
        <p v-if="passwordMessage" class="auth-hint">{{ passwordMessage }}</p>
      </div>
    </section>
  </main>
</template>
