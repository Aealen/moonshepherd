<script setup lang="ts">
import { ref } from 'vue'
import logoUrl from '../assets/logo-64.png'
import { useRoute, useRouter } from 'vue-router'
import { NForm, NFormItem, NInput, NButton } from 'naive-ui'

const router = useRouter()
const route = useRoute()
const year = new Date().getFullYear()


const username = ref('admin')
const password = ref('')
const loading = ref(false)
const lockHint = ref('')

// 星空点缀:固定随机种子风格的星点(每次会话一致,不用真随机避免重绘闪烁)
const stars = Array.from({ length: 42 }, (_, i) => {
  const size = 1 + (i * 7) % 3
  return {
    id: i,
    style: {
      left: `${(i * 23.7) % 100}%`,
      top: `${(i * 37.3) % 100}%`,
      width: `${size}px`,
      height: `${size}px`,
      animationDelay: `${(i * 0.37) % 4}s`,
    },
  }
})

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
    <!-- 星空+月晕背景层(纯 CSS,牧云意象) -->
    <div class="login-bg" aria-hidden="true">
      <div class="moon"></div>
      <div v-for="s in stars" :key="s.id" class="star" :style="s.style"></div>
    </div>

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
        <div v-if="lockHint" style="color:#ef9d93; font-size:12.5px; margin-bottom:10px;">{{ lockHint }}</div>
        <n-button type="primary" block :loading="loading" :disabled="!password" @click="login">进入面板</n-button>
      </n-form>
      <div class="login-features">
        <span>节点总览</span><i>·</i><span>应用探测</span><i>·</i><span>Web 终端</span><i>·</i><span>配置管理</span>
      </div>
    </div>

    <div class="login-footer">
      <span class="mono">© {{ year }} MoonShepherd</span>
    </div>
  </div>
</template>

<style scoped>
.login-bg {
  position: absolute;
  inset: 0;
  overflow: hidden;
  pointer-events: none;
}
.moon {
  position: absolute;
  top: 9%;
  right: 12%;
  width: 130px;
  height: 130px;
  border-radius: 50%;
  background: radial-gradient(circle at 38% 35%, #ffffff 0%, #e9ebef 45%, #b8bdc7 100%);
  box-shadow: 0 0 70px 22px rgba(255, 255, 255, 0.12), 0 0 160px 60px rgba(255, 255, 255, 0.05);
  opacity: 0.9;
}
.star {
  position: absolute;
  border-radius: 50%;
  background: #cfd4dd;
  animation: twinkle 4s ease-in-out infinite;
}
@keyframes twinkle {
  0%, 100% { opacity: 0.15; }
  50% { opacity: 0.8; }
}
.login-features {
  display: flex;
  justify-content: center;
  gap: 8px;
  margin-top: 22px;
  font-size: 12px;
  color: #6b7280;
}
.login-features i { font-style: normal; color: #3a3f47; }
.login-footer {
  position: absolute;
  bottom: 22px;
  left: 0;
  right: 0;
  text-align: center;
  font-size: 12px;
  color: #4d535e;
}
</style>
