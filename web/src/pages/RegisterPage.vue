<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { register } from '../lib/authApi'

const router = useRouter()

const username = ref('')
const displayName = ref('')
const password = ref('')
const confirmPassword = ref('')
const loading = ref(false)
const error = ref('')

async function handleSubmit(): Promise<void> {
  error.value = ''
  if (!username.value.trim()) {
    error.value = '请输入用户名'
    return
  }
  if (!password.value) {
    error.value = '请输入密码'
    return
  }
  if (password.value !== confirmPassword.value) {
    error.value = '两次密码输入不一致'
    return
  }

  loading.value = true
  try {
    await register({
      username: username.value.trim(),
      password: password.value,
      displayName: displayName.value.trim() || username.value.trim(),
    })
    await router.replace('/')
  } catch (e) {
    error.value = e instanceof Error ? e.message : '注册失败'
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <main class="auth-page">
    <section class="auth-card">
      <h1>注册</h1>
      <p class="auth-hint">创建账号后即可保存你的个人资源。</p>

      <form class="auth-form" @submit.prevent="handleSubmit">
        <label>
          用户名
          <input v-model="username" type="text" placeholder="请输入用户名" autocomplete="username" />
        </label>

        <label>
          昵称
          <input v-model="displayName" type="text" placeholder="可选，默认与用户名一致" autocomplete="nickname" />
        </label>

        <label>
          密码
          <input v-model="password" type="password" placeholder="至少 6 位" autocomplete="new-password" />
        </label>

        <label>
          确认密码
          <input v-model="confirmPassword" type="password" placeholder="请再次输入密码" autocomplete="new-password" />
        </label>

        <p v-if="error" class="auth-error">{{ error }}</p>

        <button type="submit" class="btn-primary" :disabled="loading">
          {{ loading ? '注册中...' : '注册并登录' }}
        </button>
      </form>
    </section>
  </main>
</template>
