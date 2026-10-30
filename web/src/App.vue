<script setup lang="ts">
import { computed, h, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  NConfigProvider, NGlobalStyle, NLayout, NLayoutSider, NLayoutHeader, NLayoutContent,
  NMenu, NButton, NAvatar, NDropdown, NMessageProvider, NDialogProvider,
  darkTheme, type MenuOption, type GlobalThemeOverrides,
} from 'naive-ui'
import { RouterLink, RouterView } from 'vue-router'
import { ServerOutline, GridOutline, TerminalOutline, ShieldCheckmarkOutline, SettingsOutline, ExtensionPuzzleOutline } from '@vicons/ionicons5'
import { NIcon } from 'naive-ui'
import { onEvent, connectEvents } from './api/ws'
import logoUrl from './assets/logo-64.png'

const route = useRoute()
const router = useRouter()
const online = ref<number | null>(null)
const offline = ref<number | null>(null)
const collapsed = ref(false)

// 主题:暗色画廊 —— 单色分层表面 + 单一牧绿强调 + 胶囊控件
const themeOverrides: GlobalThemeOverrides = {
  common: {
    primaryColor: '#63d3a6',
    primaryColorHover: '#8ee6c3',
    primaryColorPressed: '#4bb98d',
    primaryColorSuppl: '#8ee6c3',
    infoColor: '#8fb8a8',
    warningColor: '#e3c47f',
    errorColor: '#ef9d93',
    borderRadius: '10px',
    bodyColor: '#101312',
    cardColor: '#1a201e',
    modalColor: '#1a201e',
    popoverColor: '#212826',
    inputColor: '#212826',
    tableColor: '#151a18',
    dividerColor: '#272e2c',
    borderColor: '#333c39',
    textColor1: '#eef2f0',
    textColor2: '#a3ada8',
    textColor3: '#66706b',
    fontWeightStrong: '600',
  },
  Button: {
    textColorPrimary: '#0d100f',
    fontWeight: '600',
    borderRadiusMedium: '999px',
    borderRadiusSmall: '999px',
    borderRadiusTiny: '999px',
    borderRadiusLarge: '999px',
  },
  Menu: {
    borderRadius: '10px',
    itemColorActive: 'rgba(99, 211, 166, 0.10)',
    itemColorHover: 'rgba(255, 255, 255, 0.04)',
    itemTextColorActive: '#8ee6c3',
    itemTextColorActiveHover: '#8ee6c3',
    itemIconColorActive: '#8ee6c3',
    itemTextColorChildActive: '#8ee6c3',
    itemIconColorChildActive: '#8ee6c3',
  },
  Tag: { borderRadius: '999px' },
  Input: { borderRadius: '10px' },
  Card: { borderRadius: '14px' },
  Dialog: { borderRadius: '16px' },
}

function renderIcon(icon: any) {
  return () => h(NIcon, { size: 18 }, { default: () => h(icon) })
}

const menuOptions = computed<MenuOption[]>(() => [
  {
    label: () => h(RouterLink, { to: '/nodes' }, { default: () => '节点' }),
    key: 'nodes', icon: renderIcon(ServerOutline),
  },
  {
    label: () => h(RouterLink, { to: '/apps' }, { default: () => '应用中心' }),
    key: 'apps', icon: renderIcon(GridOutline),
  },
  {
    label: () => h(RouterLink, { to: '/commands' }, { default: () => '指令中心' }),
    key: 'commands', icon: renderIcon(TerminalOutline),
  },
  {
    label: () => h(RouterLink, { to: '/audit' }, { default: () => '操作审计' }),
    key: 'audit', icon: renderIcon(ShieldCheckmarkOutline),
  },
  {
    label: () => h(RouterLink, { to: '/mcp' }, { default: () => 'MCP Keys' }),
    key: 'mcp', icon: renderIcon(ExtensionPuzzleOutline),
  },
  {
    label: () => h(RouterLink, { to: '/system' }, { default: () => '系统' }),
    key: 'system', icon: renderIcon(SettingsOutline),
  },
])

const activeMenu = computed(() => {
  if (route.path.startsWith('/apps')) return 'apps'
  if (route.path.startsWith('/commands')) return 'commands'
  if (route.path.startsWith('/audit')) return 'audit'
  if (route.path.startsWith('/mcp')) return 'mcp'
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
            bordered collapse-mode="width" :collapsed-width="64" :width="208"
            :collapsed="collapsed" show-trigger @collapse="collapsed = true" @expand="collapsed = false"
          >
            <div class="brand">
              <img class="brand-mark" :src="logoUrl" alt="牧云" />
              <transition name="fade">
                <div v-if="!collapsed" class="brand-text">
                  <div class="brand-name">牧云</div>
                  <div class="brand-en mono">MoonShepherd</div>
                </div>
              </transition>
            </div>
            <n-menu :collapsed="collapsed" :collapsed-width="64" :options="menuOptions" :value="activeMenu" />
            <div v-if="!collapsed" class="sider-foot mono">
              <span class="foot-dot"></span> herding
            </div>
          </n-layout-sider>

          <n-layout>
            <n-layout-header bordered style="height:54px; display:flex; align-items:center; justify-content:space-between; padding:0 24px;">
              <div class="stat-row">
                <div v-if="online !== null" class="stat-chip online">
                  <span class="stat-dot pulse"></span>{{ online }} 在线
                </div>
                <div v-if="offline !== null && offline > 0" class="stat-chip offline">
                  <span class="stat-dot"></span>{{ offline }} 离线
                </div>
              </div>
              <n-dropdown :options="userMenu" @select="logout">
                <n-button quaternary size="small" style="gap:9px; padding:0 10px;">
                  <n-avatar round :size="26" style="background:linear-gradient(135deg,#63d3a6,#4a7a68); color:#0d100f; font-weight:700">M</n-avatar>
                  <span style="font-size:13px">admin</span>
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
  gap: 11px;
  padding: 20px 20px 16px;
}
.brand-mark { width: 30px; height: 30px; }
.brand-name { font-weight: 800; font-size: 17px; letter-spacing: 1px; line-height: 1.2; }
.brand-en {
  font-size: 10.5px;
  color: #66706b;
  letter-spacing: 1.4px;
  text-transform: uppercase;
}
.sider-foot {
  position: absolute;
  bottom: 14px; left: 0; right: 0;
  text-align: center;
  font-size: 10.5px;
  color: #454f49;
  letter-spacing: 1px;
  display: flex; align-items: center; justify-content: center; gap: 6px;
}
.foot-dot {
  width: 6px; height: 6px; border-radius: 50%;
  background: #63d3a6; opacity: 0.55;
}
.fade-enter-active, .fade-leave-active { transition: opacity 0.15s; }
.fade-enter-from, .fade-leave-to { opacity: 0; }

/* 顶栏统计徽章胶囊 */
.stat-row { display: flex; gap: 10px; align-items: center; }
.stat-chip {
  display: inline-flex; align-items: center; gap: 7px;
  font-size: 12.5px; font-weight: 600;
  padding: 4px 13px;
  border-radius: 99px;
  font-family: "JetBrains Mono", Consolas, monospace;
}
.stat-chip.online {
  color: #8ee6c3;
  background: rgba(99, 211, 166, 0.09);
  border: 1px solid rgba(99, 211, 166, 0.22);
}
.stat-chip.offline {
  color: #e3c47f;
  background: rgba(227, 196, 127, 0.08);
  border: 1px solid rgba(227, 196, 127, 0.2);
}
.stat-dot {
  width: 7px; height: 7px; border-radius: 50%;
  background: #e3c47f;
}
.stat-chip.online .stat-dot {
  background: #63d3a6;
}
.pulse { animation: breathe 2.4s ease-in-out infinite; }
@keyframes breathe {
  0%, 100% { opacity: 0.55; }
  50% { opacity: 1; }
}
</style>
