<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute } from 'vue-router'
import { changePassword, updateProfile } from '../lib/authApi'
import { currentUser } from '../lib/authStore'
import { canManageUsers, currentRoles } from '../lib/permission'
import PageContainer from '../components/PageContainer.vue'
import PageHeader from '../components/PageHeader.vue'
import ModuleSectionCard from '../components/ModuleSectionCard.vue'

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
    profileMessage.value = '资料已更新'
  } catch (e) {
    profileMessage.value = e instanceof Error ? e.message : '更新失败'
  } finally {
    loadingProfile.value = false
  }
}

async function savePassword(): Promise<void> {
  passwordMessage.value = ''
  if (!currentPassword.value || !newPassword.value) {
    passwordMessage.value = '请填写完整的密码信息'
    return
  }
  if (newPassword.value !== confirmPassword.value) {
    passwordMessage.value = '两次输入的新密码不一致'
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
  <PageContainer mode="wide">
  <main class="module-page module-page--account account-center">
    <PageHeader
      eyebrow="ACCOUNT CENTER"
      title="账户设置"
      description="维护基础资料与登录安全信息。"
    />

    <section class="account-center__layout">
      <div class="account-center__main">
        <ModuleSectionCard class="account-center__section">
          <section class="account-form-card">
            <div class="account-form-card__head">
              <p class="account-center__eyebrow">Profile</p>
              <h2>资料编辑</h2>
              <p>修改当前账户的昵称信息，作为平台内的主要展示名称。</p>
            </div>

            <div class="account-form-grid">
              <label>
                账号
                <input :value="username" type="text" disabled />
              </label>

              <label>
                昵称
                <input v-model="displayName" type="text" placeholder="输入昵称" />
              </label>
            </div>

            <div class="account-form-card__actions">
              <button type="button" class="btn-primary" :disabled="loadingProfile" @click="saveProfile">
                {{ loadingProfile ? '保存中...' : '保存资料' }}
              </button>
              <p v-if="profileMessage" class="account-center__message">{{ profileMessage }}</p>
            </div>
          </section>
        </ModuleSectionCard>

        <ModuleSectionCard class="account-center__section">
          <section class="account-form-card">
            <div class="account-form-card__head">
              <p class="account-center__eyebrow">Security</p>
              <h2>密码修改</h2>
              <p>保持账户安全，只保留当前密码、新密码和确认新密码三个已有字段。</p>
            </div>

            <div class="account-form-grid">
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
            </div>

            <div class="account-form-card__actions">
              <button type="button" class="btn-primary" :disabled="loadingPassword" @click="savePassword">
                {{ loadingPassword ? '提交中...' : '修改密码' }}
              </button>
              <p v-if="passwordMessage" class="account-center__message">{{ passwordMessage }}</p>
            </div>
          </section>
        </ModuleSectionCard>
      </div>

      <aside class="account-center__side">
        <ModuleSectionCard class="account-center__section">
          <section class="account-summary-card">
            <div class="account-summary-card__head">
              <p class="account-center__eyebrow">Account Summary</p>
              <h2>账户信息</h2>
              <p>右侧仅展示当前已有信息的重排，不增加新的账户业务功能。</p>
            </div>

            <div class="account-summary-list">
              <article class="account-summary-item">
                <span>账号</span>
                <strong>{{ username || '--' }}</strong>
              </article>

              <article class="account-summary-item">
                <span>角色</span>
                <strong>{{ rolesText }}</strong>
              </article>

              <article class="account-summary-item">
                <span>昵称</span>
                <strong>{{ displayName || '--' }}</strong>
              </article>
            </div>

            <div class="account-summary-tags">
              <span>{{ canManageUsers() ? '管理员权限' : '标准账户' }}</span>
              <span v-if="deniedTip">已从无权限页面跳转</span>
            </div>
          </section>
        </ModuleSectionCard>
      </aside>
    </section>
  </main>
  </PageContainer>
</template>

<style scoped>
.account-center {
  gap: 14px;
}

.account-center__layout {
  display: grid;
  grid-template-columns: minmax(0, 1.2fr) 360px;
  gap: 16px;
  align-items: start;
}

.account-center__main,
.account-center__side {
  display: grid;
  gap: 14px;
}

.account-center__section {
  padding: 0;
  border: none;
  box-shadow: none;
  background: transparent;
}

.account-form-card,
.account-summary-card {
  border: 1px solid var(--line);
  border-radius: 20px;
  background:
    linear-gradient(180deg, rgba(255, 255, 255, 0.94), rgba(255, 255, 255, 0.84)),
    var(--bg-panel);
  box-shadow: var(--shadow-soft);
  padding: 20px;
  display: grid;
  gap: 16px;
}

.account-form-card__head,
.account-summary-card__head {
  display: grid;
  gap: 8px;
}

.account-center__eyebrow {
  margin: 0;
  font-size: 11px;
  letter-spacing: 0.12em;
  text-transform: uppercase;
  color: #5f8a78;
  font-weight: 700;
}

.account-form-card__head h2,
.account-summary-card__head h2 {
  margin: 0;
  font-family: var(--font-display);
  font-size: 28px;
  line-height: 1.08;
  letter-spacing: -0.02em;
}

.account-form-card__head p:last-child,
.account-summary-card__head p:last-child,
.account-center__message {
  margin: 0;
  color: var(--text-muted);
  line-height: 1.7;
}

.account-form-grid {
  display: grid;
  gap: 12px;
}

.account-form-grid label {
  display: grid;
  gap: 8px;
  font-size: 13px;
  font-weight: 700;
}

.account-form-grid input {
  min-height: 48px;
  border: 1px solid #d7dde3;
  border-radius: 14px;
  padding: 0 14px;
  background: rgba(251, 252, 253, 0.92);
  transition: border-color 0.2s ease, box-shadow 0.2s ease, background 0.2s ease;
}

.account-form-grid input:focus {
  outline: none;
  border-color: #b8c0f0;
  box-shadow: 0 0 0 4px rgba(184, 192, 240, 0.16);
  background: #fff;
}

.account-form-grid input:disabled {
  color: var(--text-muted);
  background: #f5f7f9;
  cursor: not-allowed;
}

.account-form-card__actions {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}

.account-summary-list {
  display: grid;
  gap: 12px;
}

.account-summary-item {
  border: 1px solid var(--line);
  border-radius: 16px;
  background: rgba(255, 255, 255, 0.76);
  padding: 14px 16px;
  display: grid;
  gap: 8px;
}

.account-summary-item span {
  color: var(--text-muted);
  font-size: 12px;
}

.account-summary-item strong {
  font-size: 16px;
  line-height: 1.4;
  word-break: break-word;
}

.account-summary-tags {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}

.account-summary-tags span {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-height: 30px;
  padding: 0 12px;
  border-radius: 999px;
  border: 1px solid rgba(221, 225, 230, 0.94);
  background: linear-gradient(120deg, rgba(204, 232, 220, 0.38), rgba(226, 216, 246, 0.32));
  color: #4a5b6a;
  font-size: 12px;
  font-weight: 700;
}

@media (max-width: 1080px) {
  .account-center__layout {
    grid-template-columns: 1fr;
  }
}
</style>
