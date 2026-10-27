<script setup lang="ts">
import { h, onMounted, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import {
  NDataTable, NTag, NButton, NInput, NSelect, NSpace, NModal, NForm, NFormItem,
  NPagination, NEmpty, NPopconfirm, useMessage, type DataTableColumns,
} from 'naive-ui'
import { api, fmtAgo, fmtTime, type Node, type Page, type RegistrationToken } from '../api'
import { onEvent } from '../api/ws'

const router = useRouter()
const message = useMessage()

const nodes = ref<Node[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 20
const loading = ref(false)
const statusFilter = ref<string | null>(null)
const query = ref('')
// 面板自身版本(升级比对基准;/system/info 面板卡片同源)
const panelVersion = ref('')

// updateAgent 触发自更新(POST /nodes/{id}/update);成败以重连后版本号为准
async function updateAgent(row: Node) {
  try {
    await api.post(`/nodes/${row.id}/update`)
    message.success(`升级指令已下发:${row.agent_version} → ${panelVersion.value}。节点将在 1~2 分钟内重连`)
    setTimeout(load, 8000) // 8 秒后刷新版本列观察
  } catch (e: any) { message.error(e.message) }
}

async function loadPanelVersion() {
  try {
    const info = await api.get<{ panel_version: string }>('/system/info')
    panelVersion.value = info.panel_version
  } catch { /* 忽略 */ }
}

const statusTag = (s: string, enabled: boolean) => {
  if (!enabled) return h('span', { class: 'node-state disabled' }, [h('i'), '已禁用'])
  if (s === 'online') return h('span', { class: 'node-state online' }, [h('i', { class: 'pulse' }), '在线'])
  return h('span', { class: 'node-state offline' }, [h('i'), '离线'])
}

const columns: DataTableColumns<Node> = [
  {
    title: '节点', key: 'name', minWidth: 180,
    render: (row) => h('div', { style: 'cursor:pointer', onClick: () => router.push(`/nodes/${row.id}`) }, [
      h('div', { style: 'font-weight:600' }, row.name),
      h('div', { class: 'muted', style: 'font-size:12px' }, `${row.hostname} · ${row.os_name} ${row.os_version} (${row.os_arch})`),
    ]),
  },
  { title: '状态', key: 'status', width: 90, render: (row) => statusTag(row.status, row.enabled) },
  {
    title: 'Agent', key: 'agent_version', width: 120,
    render: (row) => {
      if (!row.agent_version) return h('span', { class: 'muted' }, '—')
      const outdated = row.status === 'online' && panelVersion.value && row.agent_version !== panelVersion.value
      return h('span', { class: 'mono ' + (outdated ? 'ver-stale' : 'muted') }, [
        row.agent_version,
        outdated ? h('span', { class: 'ver-badge', title: '待升级,操作列可更新' }, '待升级') : null,
      ])
    },
  },
  {
    title: '标签', key: 'tags', width: 160,
    render: (row) => row.tags.length
      ? h(NSpace, { size: 4 }, { default: () => row.tags.map((t) => h(NTag, { size: 'tiny', bordered: false }, { default: () => t })) })
      : h('span', { class: 'muted' }, '—'),
  },
  { title: '最后心跳', key: 'last_seen', width: 120, render: (row) => h('span', { class: 'muted' }, fmtAgo(row.last_seen)) },
  {
    title: '操作', key: 'actions', width: 150,
    render: (row) => h(NSpace, { size: 6 }, {
      default: () => [
        h(NButton, { size: 'tiny', quaternary: true, type: 'primary', onClick: () => router.push(`/nodes/${row.id}`) }, { default: () => '详情' }),
        (row.status === 'online' && panelVersion.value && row.agent_version && row.agent_version !== panelVersion.value && !(row.capabilities ?? []).includes('supervisor:unknown'))
          ? h(NPopconfirm, { onPositiveClick: () => updateAgent(row) }, {
              trigger: () => h(NButton, { size: 'tiny', quaternary: true, type: 'info' }, { default: () => '升级' }),
              default: () => `将 Agent 从 ${row.agent_version} 更新到 ${panelVersion.value}。更新中连接会中断 1~2 分钟,失败自动回滚旧版(不会失联)。确定?`,
            })
          : null,
        row.enabled
          ? h(NPopconfirm, { onPositiveClick: () => toggleNode(row, false) }, {
              trigger: () => h(NButton, { size: 'tiny', quaternary: true, type: 'warning' }, { default: () => '禁用' }),
              default: () => '禁用后 Agent 将立即断开且无法重连，确定？',
            })
          : h(NButton, { size: 'tiny', quaternary: true, type: 'success', onClick: () => toggleNode(row, true) }, { default: () => '解禁' }),
        h(NPopconfirm, { onPositiveClick: () => deleteNode(row) }, {
          trigger: () => h(NButton, { size: 'tiny', quaternary: true, type: 'error' }, { default: () => '删除' }),
          default: () => `删除节点「${row.name}」将清除其应用清单与配置目标（指令审计保留）。输入节点名确认的操作在确认框内完成。确定删除？`,
        }),
      ],
    }),
  },
]

async function load() {
  loading.value = true
  try {
    const params = new URLSearchParams({ page: String(page.value), page_size: String(pageSize) })
    if (statusFilter.value) params.set('status', statusFilter.value)
    if (query.value) params.set('q', query.value)
    const data = await api.get<Page<Node>>(`/nodes?${params}`)
    nodes.value = data.data ?? []
    total.value = data.total
  } catch (e: any) {
    message.error(e.message)
  } finally {
    loading.value = false
  }
}

async function toggleNode(row: Node, enable: boolean) {
  try {
    await api.post(`/nodes/${row.id}/${enable ? 'enable' : 'disable'}`)
    message.success(enable ? '已解禁' : '已禁用并断开连接')
    load()
  } catch (e: any) { message.error(e.message) }
}

async function deleteNode(row: Node) {
  try {
    await api.delete(`/nodes/${row.id}?confirm=${encodeURIComponent(row.name)}`)
    message.success('节点已删除')
    load()
  } catch (e: any) { message.error(e.message) }
}

// ---------- 注册 Token 弹窗 ----------

const showTokenModal = ref(false)
const tokenNote = ref('')
const tokenHours = ref(24)
const freshToken = ref('')
const tokenList = ref<RegistrationToken[]>([])

async function openTokenModal() {
  showTokenModal.value = true
  freshToken.value = ''
  await loadTokens()
}

async function loadTokens() {
  try {
    const data = await api.get<{ data: RegistrationToken[] }>('/nodes/registration-tokens')
    tokenList.value = data.data ?? []
  } catch { /* 忽略 */ }
}

async function createToken() {
  try {
    const data = await api.post<{ token: string }>('/nodes/registration-tokens', {
      note: tokenNote.value, expires_hours: tokenHours.value,
    })
    freshToken.value = data.token
    loadTokens()
  } catch (e: any) { message.error(e.message) }
}

async function revokeToken(id: number) {
  try {
    await api.delete(`/nodes/registration-tokens/${id}`)
    loadTokens()
  } catch (e: any) { message.error(e.message) }
}

const tokenStatusLabels: Record<string, string> = { active: '可用', used: '已使用', revoked: '已撤销' }

const tokenColumns: DataTableColumns<RegistrationToken> = [
  { title: '备注', key: 'note', width: 140, render: (t) => t.note || '—' },
  {
    title: '状态', key: 'status', width: 90,
    render: (t) => h(NTag, { size: 'tiny', bordered: false,
      type: t.status === 'active' ? 'success' : t.status === 'used' ? 'info' : 'default' },
      { default: () => tokenStatusLabels[t.status] ?? t.status }),
  },
  { title: '过期时间', key: 'expires_at', width: 170, render: (t) => fmtTime(t.expires_at) },
  {
    title: '', key: 'op', width: 70,
    render: (t) => t.status === 'active'
      ? h(NPopconfirm, { onPositiveClick: () => revokeToken(t.id) }, {
          trigger: () => h(NButton, { size: 'tiny', quaternary: true, type: 'warning' }, { default: () => '撤销' }),
          default: () => '确定撤销该 Token？',
        })
      : null,
  },
]

function installCmd() {
  const base = location.origin
  return `curl -s ${base}/install.sh | bash -s -- --server ${base.replace(/^http/, 'ws').replace(/^https/, 'wss')} --token ${freshToken.value}`
}

async function copy(text: string) {
  await navigator.clipboard.writeText(text)
  message.success('已复制')
}

let off: (() => void) | null = null
onMounted(() => {
  load()
  loadPanelVersion()
  off = onEvent((ev) => {
    if (ev.topic === 'node.status' || ev.topic === 'node.registered') load()
  })
})
onUnmounted(() => off?.())
</script>

<style scoped>
/* 待升级版本徽章 */
.ver-stale { color: #f2c97d; }
.ver-badge.warn {
  background: rgba(157, 165, 180, 0.1);
  color: #9aa3b8;
  border-color: rgba(157, 165, 180, 0.3);
}
.ver-badge {
  margin-left: 7px;
  font-size: 10.5px;
  font-family: "Inter", system-ui, sans-serif;
  padding: 1px 7px;
  border-radius: 99px;
  background: rgba(242, 201, 125, 0.12);
  color: #f2c97d;
  border: 1px solid rgba(242, 201, 125, 0.3);
}
</style>

<template>
  <div class="page">
    <div style="display:flex; align-items:flex-start; justify-content:space-between">
      <div>
        <div class="page-title">节点</div>
        <div class="page-sub">牧群一览 · 状态心跳实时刷新</div>
      </div>
      <n-button type="primary" size="small" class="glow-hover" @click="openTokenModal">＋ 纳管新节点</n-button>
    </div>

    <n-space style="margin-bottom:14px" align="center">
      <n-select
        v-model:value="statusFilter" size="small" style="width:130px" clearable placeholder="状态"
        :options="[{ label: '在线', value: 'online' }, { label: '离线', value: 'offline' }]"
        @update:value="page = 1; load()"
      />
      <n-input v-model:value="query" size="small" style="width:220px" placeholder="搜索名称/主机名" clearable
        @keyup.enter="page = 1; load()" />
      <n-button size="small" @click="page = 1; load()">搜索</n-button>
    </n-space>

    <n-data-table :columns="columns" :data="nodes" :loading="loading" :bordered="false" size="small"
      :row-key="(r: Node) => r.id" />
    <n-empty v-if="!loading && nodes.length === 0" description="还没有节点——点击右上角「纳管新节点」发放注册 Token" style="margin-top:40px" />

    <div style="display:flex; justify-content:flex-end; margin-top:14px" v-if="total > pageSize">
      <n-pagination v-model:page="page" :page-size="pageSize" :item-count="total" @update:page="load" />
    </div>

    <!-- 注册 Token 弹窗 -->
    <n-modal v-model:show="showTokenModal" preset="card" title="纳管新节点" style="width:640px">
      <n-form label-placement="left" :show-feedback="false" inline>
        <n-form-item label="备注">
          <n-input v-model:value="tokenNote" placeholder="如：家庭 NAS" size="small" style="width:180px" />
        </n-form-item>
        <n-form-item label="有效期(时)">
          <n-select v-model:value="tokenHours" size="small" style="width:90px"
            :options="[24, 48, 72].map((h) => ({ label: String(h), value: h }))" />
        </n-form-item>
        <n-button size="small" type="primary" @click="createToken">生成 Token</n-button>
      </n-form>

      <div v-if="freshToken" style="margin-top:18px">
        <div style="font-weight:600; margin-bottom:8px">一次性注册 Token（仅显示这一次，24h 内有效）</div>
        <div class="term" style="margin-bottom:10px">{{ freshToken }}</div>
        <div style="font-weight:600; margin-bottom:8px">在目标机器执行（二进制形态）：</div>
        <div class="term" style="margin-bottom:10px">{{ installCmd() }}</div>
        <div class="muted" style="font-size:12.5px; margin-bottom:8px">
          Unraid / Docker 机用容器形态：<code>docker run -d --restart unless-stopped -e HERD_SERVER=… -e HERD_TOKEN=… herd/shepherd</code>
          （详见 docs/01 §3.1 挂载清单）
        </div>
        <n-space>
          <n-button size="small" @click="copy(freshToken)">复制 Token</n-button>
          <n-button size="small" @click="copy(installCmd())">复制安装命令</n-button>
        </n-space>
      </div>

      <div v-if="tokenList.length" style="margin-top:22px">
        <div style="font-weight:600; margin-bottom:8px">历史 Token</div>
        <n-data-table size="small" :bordered="false" :data="tokenList"
          :row-key="(t: RegistrationToken) => t.id" :columns="tokenColumns" />
      </div>
    </n-modal>
  </div>
</template>

<style scoped>
/* 节点状态:圆点呼吸灯胶囊(替代 tag) */
.node-state {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  font-size: 12.5px;
  font-weight: 600;
  font-family: "JetBrains Mono", Consolas, monospace;
}
.node-state i {
  width: 8px; height: 8px; border-radius: 50%;
  background: #5d6579;
}
.node-state.online { color: #99f0e4; }
.node-state.online i {
  background: #6ee7d8;
  box-shadow: 0 0 8px rgba(110, 231, 216, 0.7);
}
.node-state.offline { color: #9aa3b8; }
.node-state.offline i { background: #8a7a4e; }
.node-state.disabled { color: #5d6579; }
.pulse { animation: node-breathe 2.4s ease-in-out infinite; }
@keyframes node-breathe {
  0%, 100% { box-shadow: 0 0 3px rgba(110, 231, 216, 0.5); }
  50% { box-shadow: 0 0 11px rgba(110, 231, 216, 0.95); }
}
</style>
