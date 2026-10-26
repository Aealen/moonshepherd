<script setup lang="ts">
import { ref } from 'vue'
import logoUrl from '../assets/logo-64.png'
import { useRoute, useRouter } from 'vue-router'
import { NForm, NFormItem, NInput, NButton } from 'naive-ui'

const router = useRouter()
const route = useRoute()


const username = ref('admin')
const password = ref('')
const loading = ref(false)
const lockHint = ref('')

async function login() {
  if (!username.value || !password.value) return
  loading.value = true
  lockHint.value = ''
  try {
    const resp = await fetch('/api/v1/auth/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'same-origin',
      body: JSON.stringify({ username: username.value, password: password.value }),
    })
    if (resp.status === 429) {
      const retry = resp.headers.get('Retry-After')
      lockHint.value = `尝试次数过多已锁定${retry ? `，约 ${Math.ceil(Number(retry) / 60)} 分钟后解锁` : ''}`
      return
    }
    const data = await resp.json().catch(() => ({}))
    if (!resp.ok) {
      lockHint.value = data?.error?.message ?? '登录失败'
      return
    }
    localStorage.setItem('herd_auth_ok', '1')
    router.push((route.query.redirect as string) || '/nodes')
  } catch (e: any) {
    lockHint.value = '网络错误：' + e.message
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="login-wrap">
    <div class="login-card">
      <div style="display:flex; align-items:center; gap:14px; margin-bottom:8px;">
        <img class="brand-mark" :src="logoUrl" alt="牧云" />
        <div>
          <div style="font-size:22px; font-weight:700;">牧云 MoonShepherd</div>
          <div class="muted" style="font-size:12px;">把散落在云端的服务器，当羊群来放牧</div>
        </div>
      </div>
      <n-form @keyup.enter="login" style="margin-top:26px">
        <n-form-item label="用户名" label-placement="top">
          <n-input v-model:value="username" placeholder="admin" autofocus />
        </n-form-item>
        <n-form-item label="密码" label-placement="top">
          <n-input v-model:value="password" type="password" show-password-on="click" placeholder="至少 10 位" />
        </n-form-item>
        <div v-if="lockHint" style="color:#ff8f8f; font-size:12.5px; margin-bottom:10px;">{{ lockHint }}</div>
        <n-button type="primary" block :loading="loading" :disabled="!password" @click="login">进入面板</n-button>
      </n-form>
    </div>
  </div>
</template>
