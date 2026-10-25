<script setup lang="ts">
import { computed, h, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  NConfigProvider, NGlobalStyle, NLayout, NLayoutSider, NLayoutHeader, NLayoutContent,
  NMenu, NButton, NTag, NAvatar, NDropdown, NMessageProvider, NDialogProvider,
  darkTheme, type MenuOption, type GlobalThemeOverrides,
} from 'naive-ui'
import { RouterLink, RouterView } from 'vue-router'
import { onEvent, connectEvents } from './api/ws'

const route = useRoute()
const router = useRouter()
const online = ref<number | null>(null)
const offline = ref<number | null>(null)
const collapsed = ref(false)

// 主题：暗色 + 草原绿主色（牧云意象）
const themeOverrides: GlobalThemeOverrides = {
  common: {
    primaryColor: '#5bbf7f',
    primaryColorHover: '#7ad39a',
    primaryColorPressed: '#4aa96c',
    primaryColorSuppl: '#7ad39a',
    borderRadius: '6px',
    cardColor: '#17171d',
    bodyColor: '#101014',
  },
}

function renderIcon(emoji: string) {
  return () => h('span', { style: { fontSize: '16px', lineHeight: '1' } }, emoji)
}

const menuOptions = computed<MenuOption[]>(() => [
  {
    label: () => h(RouterLink, { to: '/nodes' }, { default: () => '节点' }),
    key: 'nodes', icon: renderIcon('🖥️'),
  },
  {
    label: () => h(RouterLink, { to: '/apps' }, { default: () => '应用中心' }),
    key: 'apps', icon: renderIcon('📦'),
  },
  {
    label: () => h(RouterLink, { to: '/commands' }, { default: () => '指令中心' }),
    key: 'commands', icon: renderIcon('⌘'),
  },
  {
    label: () => h(RouterLink, { to: '/system' }, { default: () => '系统' }),
    key: 'system', icon: renderIcon('⚙️'),
  },
])

const activeMenu = computed(() => {
  if (route.path.startsWith('/apps')) return 'apps'
  if (route.path.startsWith('/commands')) return 'commands'
  if (route.path.startsWith('/system')) return 'system'
  return 'nodes'
})

const isLogin = computed(() => route.path === '/login')

async function logout() {
  await fetch('/api/v1/auth/logout', { method: 'POST', credentials: 'same-origin' })
  localStorage.removeItem('herd_auth_ok')
  router.push('/login')
}

const userMenu = [{ label: '退出登录', key: 'logout' }]

// 顶栏统计：进入受保护页时拉取 + 事件驱动刷新
async function refreshStats() {
  try {
    const resp = await fetch('/api/v1/nodes?page=1&page_size=1', { credentials: 'same-origin' })
    if (!resp.ok) return
    online.value = Number(resp.headers.get('X-Total-Online') ?? 0)
    offline.value = Number(resp.headers.get('X-Total-Offline') ?? 0)
  } catch { /* 离线容忍 */ }
}

if (!isLogin.value) {
  connectEvents()
  refreshStats()
  onEvent((ev) => {
    if (ev.topic === 'node.status') refreshStats()
  })
}
</script>

<template>
  <n-config-provider v-if="isLogin" :theme="darkTheme" :theme-overrides="themeOverrides">
    <n-global-style />
    <n-message-provider>
      <router-view />
    </n-message-provider>
  </n-config-provider>

  <n-config-provider v-else :theme="darkTheme" :theme-overrides="themeOverrides">
    <n-global-style />
    <n-message-provider>
      <n-dialog-provider>
        <n-layout style="height: 100vh" has-sider>
          <n-layout-sider
            bordered collapse-mode="width" :collapsed-width="64" :width="200"
            :collapsed="collapsed" show-trigger @collapse="collapsed = true" @expand="collapsed = false"
          >
            <div class="brand">
              <span class="brand-mark">🐑</span>
              <transition name="fade">
                <span v-if="!collapsed" class="brand-name">牧云 Herd</span>
              </transition>
            </div>
            <n-menu :collapsed="collapsed" :collapsed-width="64" :options="menuOptions" :value="activeMenu" />
          </n-layout-sider>

          <n-layout>
            <n-layout-header bordered style="height:52px; display:flex; align-items:center; justify-content:space-between; padding:0 20px;">
              <div style="display:flex; gap:10px; align-items:center;">
                <n-tag v-if="online !== null" type="success" size="small" round>在线 {{ online }}</n-tag>
                <n-tag v-if="offline !== null" :type="offline ? 'warning' : 'default'" size="small" round>
                  离线 {{ offline }}
                </n-tag>
              </div>
              <n-dropdown :options="userMenu" @select="logout">
                <n-button quaternary size="small" style="gap:8px">
                  <n-avatar round size="small" style="background:#5bbf7f">M</n-avatar>
                  admin
                </n-button>
              </n-dropdown>
            </n-layout-header>
            <n-layout-content style="height:calc(100vh - 52px); overflow:auto">
              <router-view :key="route.fullPath" />
            </n-layout-content>
          </n-layout>
        </n-layout>
      </n-dialog-provider>
    </n-message-provider>
  </n-config-provider>
</template>

<style scoped>
.brand {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 18px 20px 14px;
  font-weight: 700;
  font-size: 17px;
}
.brand-name { white-space: nowrap; }
.fade-enter-active, .fade-leave-active { transition: opacity 0.15s; }
.fade-enter-from, .fade-leave-to { opacity: 0; }
</style>
