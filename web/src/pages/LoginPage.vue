<script setup lang="ts">
import { computed, ref } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { login } from '../lib/authApi'

const router = useRouter()
const route = useRoute()

const username = ref('')
const password = ref('')
const loading = ref(false)
const error = ref('')

const submitLabel = computed(() => (loading.value ? '登录中...' : '登录'))

async function handleSubmit(): Promise<void> {
  error.value = ''

  if (!username.value.trim()) {
    error.value = '请输入账号'
    return
  }

  if (!password.value) {
    error.value = '请输入密码'
    return
  }

  loading.value = true
  try {
    await login({
      username: username.value.trim(),
      password: password.value,
    })

    const redirect =
      typeof route.query.redirect === 'string' && route.query.redirect ? route.query.redirect : '/workspace'
    await router.replace(redirect)
  } catch (e) {
    error.value = e instanceof Error ? e.message : '登录失败，请稍后重试'
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <main class="login-page">
    <section class="login-shell">
      <div class="login-brand glass-card">
        <div class="login-brand__badge">AI Platform</div>
        <div class="login-brand__content">
          <p class="login-brand__eyebrow">Welcome Back</p>
          <h1>多智能体助手平台</h1>
          <h2>连接对话、工作流、监控与工具能力的一体化智能平台。</h2>
          <p class="login-brand__desc">
            登录后即可进入你的平台控制台，继续使用对话控制台、助手中心、工作流编排、运行监控与工具中心等现有模块。
          </p>
        </div>

        <div class="login-brand__panel">
          <div class="brand-panel__window">
            <div class="brand-panel__chrome">
              <span></span>
              <span></span>
              <span></span>
            </div>
            <div class="brand-panel__layout">
              <aside class="brand-panel__sidebar">
                <span></span>
                <span></span>
                <span></span>
              </aside>
              <div class="brand-panel__content">
                <div class="brand-panel__hero"></div>
                <div class="brand-panel__grid">
                  <span></span>
                  <span></span>
                  <span></span>
                  <span></span>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>

      <section class="login-card glass-card">
        <div class="login-card__head">
          <p class="login-card__eyebrow">Account Login</p>
          <h3>登录平台后台</h3>
          <p>使用你的账号密码进入控制台，继续已有工作流与智能助手协作。</p>
        </div>

        <form class="login-form" @submit.prevent="handleSubmit">
          <label>
            账号
            <input
              v-model="username"
              type="text"
              placeholder="请输入账号"
              autocomplete="username"
            />
          </label>

          <label>
            密码
            <input
              v-model="password"
              type="password"
              placeholder="请输入密码"
              autocomplete="current-password"
            />
          </label>

          <p v-if="error" class="login-error">{{ error }}</p>

          <button type="submit" class="btn-primary login-submit" :disabled="loading">
            {{ submitLabel }}
          </button>
        </form>

        <RouterLink class="login-back" to="/">返回官网</RouterLink>
      </section>
    </section>
  </main>
</template>

<style scoped>
.login-page {
  min-height: 100vh;
  padding: 24px;
}

.login-shell {
  width: min(1220px, 100%);
  margin: 0 auto;
  min-height: calc(100vh - 48px);
  display: grid;
  grid-template-columns: minmax(0, 1.08fr) minmax(380px, 0.92fr);
  gap: 22px;
  align-items: stretch;
}

.glass-card {
  border: 1px solid rgba(221, 225, 230, 0.92);
  border-radius: 28px;
  background:
    linear-gradient(180deg, rgba(255, 255, 255, 0.92), rgba(255, 255, 255, 0.82)),
    rgba(255, 255, 255, 0.78);
  box-shadow: 0 16px 38px rgba(48, 56, 68, 0.08);
  backdrop-filter: blur(16px);
}

.login-brand,
.login-card {
  min-height: 760px;
}

.login-brand {
  padding: 32px;
  display: grid;
  grid-template-rows: auto auto 1fr;
  gap: 22px;
  background:
    radial-gradient(circle at top left, rgba(204, 232, 220, 0.4), transparent 34%),
    radial-gradient(circle at top right, rgba(226, 216, 246, 0.38), transparent 32%),
    linear-gradient(180deg, rgba(255, 255, 255, 0.9), rgba(255, 255, 255, 0.8));
}

.login-brand__badge {
  width: fit-content;
  min-height: 36px;
  padding: 0 14px;
  border-radius: 999px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  background: linear-gradient(120deg, rgba(204, 232, 220, 0.86), rgba(226, 216, 246, 0.82));
  color: #335248;
  font-size: 12px;
  font-weight: 700;
  letter-spacing: 0.12em;
  text-transform: uppercase;
}

.login-brand__content {
  display: grid;
  gap: 14px;
  max-width: 620px;
}

.login-brand__eyebrow,
.login-card__eyebrow {
  margin: 0;
  color: #5f8a78;
  font-size: 12px;
  text-transform: uppercase;
  letter-spacing: 0.14em;
  font-weight: 700;
}

.login-brand h1,
.login-card h3 {
  margin: 0;
  font-family: var(--font-display);
  letter-spacing: -0.03em;
}

.login-brand h1 {
  font-size: clamp(44px, 5vw, 72px);
  line-height: 1;
}

.login-brand h2 {
  margin: 0;
  font-family: var(--font-display);
  font-size: clamp(26px, 3vw, 38px);
  line-height: 1.12;
  letter-spacing: -0.03em;
}

.login-brand__desc,
.login-card__head p {
  margin: 0;
  color: var(--text-muted);
  line-height: 1.75;
}

.login-brand__panel {
  display: flex;
  align-items: stretch;
}

.brand-panel__window {
  flex: 1;
  border-radius: 26px;
  border: 1px solid rgba(221, 225, 230, 0.92);
  background: linear-gradient(180deg, rgba(250, 251, 252, 0.96), rgba(244, 246, 249, 0.92));
  padding: 16px;
  display: grid;
  grid-template-rows: auto 1fr;
  gap: 14px;
}

.brand-panel__chrome {
  display: flex;
  gap: 8px;
}

.brand-panel__chrome span {
  width: 10px;
  height: 10px;
  border-radius: 999px;
  background: rgba(165, 172, 183, 0.42);
}

.brand-panel__layout {
  display: grid;
  grid-template-columns: 132px minmax(0, 1fr);
  gap: 14px;
}

.brand-panel__sidebar,
.brand-panel__hero,
.brand-panel__grid span {
  border-radius: 18px;
}

.brand-panel__sidebar {
  padding: 12px;
  border: 1px solid rgba(221, 225, 230, 0.92);
  background: rgba(255, 255, 255, 0.86);
  display: grid;
  align-content: start;
  gap: 10px;
}

.brand-panel__sidebar span {
  height: 16px;
  border-radius: 999px;
  background: rgba(197, 205, 216, 0.58);
}

.brand-panel__content {
  display: grid;
  grid-template-rows: 180px 1fr;
  gap: 12px;
}

.brand-panel__hero {
  border: 1px solid rgba(221, 225, 230, 0.92);
  background: linear-gradient(135deg, rgba(230, 245, 238, 0.95), rgba(239, 233, 250, 0.9));
}

.brand-panel__grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
}

.brand-panel__grid span {
  min-height: 92px;
  border: 1px solid rgba(221, 225, 230, 0.92);
  background: rgba(255, 255, 255, 0.88);
}

.login-card {
  padding: 32px 28px;
  display: grid;
  align-content: center;
  gap: 22px;
}

.login-card__head {
  display: grid;
  gap: 10px;
}

.login-card h3 {
  font-size: clamp(28px, 3vw, 38px);
  line-height: 1.1;
}

.login-form {
  display: grid;
  gap: 14px;
}

.login-form label {
  display: grid;
  gap: 8px;
  font-size: 14px;
  font-weight: 700;
  color: var(--text-main);
}

.login-form input {
  min-height: 52px;
  border: 1px solid #d7dde3;
  border-radius: 16px;
  padding: 0 16px;
  background: rgba(251, 252, 253, 0.92);
  transition: border-color 0.2s ease, box-shadow 0.2s ease, background 0.2s ease;
}

.login-form input:focus {
  outline: none;
  border-color: #b8c0f0;
  box-shadow: 0 0 0 4px rgba(184, 192, 240, 0.16);
  background: #fff;
}

.login-error {
  margin: 0;
  color: var(--danger-text);
  font-size: 13px;
}

.login-submit {
  min-height: 52px;
  border-radius: 16px;
}

.login-back {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-height: 48px;
  border-radius: 16px;
  border: 1px solid var(--line);
  background: rgba(255, 255, 255, 0.8);
  color: var(--text-main);
  text-decoration: none;
  font-weight: 700;
  transition: transform 0.2s ease, background 0.2s ease, box-shadow 0.2s ease;
}

.login-back:hover {
  transform: translateY(-1px);
  background: rgba(255, 255, 255, 0.96);
  box-shadow: var(--shadow-soft);
}

@media (max-width: 980px) {
  .login-shell {
    grid-template-columns: 1fr;
  }

  .login-brand,
  .login-card {
    min-height: auto;
  }
}

@media (max-width: 640px) {
  .login-page {
    padding: 16px;
  }

  .login-brand,
  .login-card {
    padding: 22px;
  }

  .brand-panel__layout,
  .brand-panel__grid {
    grid-template-columns: 1fr;
  }
}
</style>
