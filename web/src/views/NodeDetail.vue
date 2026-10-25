<script setup lang="ts">
import { computed, h, onMounted, onUnmounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  NCard, NTabs, NTabPane, NTag, NSpace, NButton, NDataTable, NDescriptions, NDescriptionsItem,
  NProgress, NEmpty, NModal, NInput, NSelect, useMessage, type DataTableColumns,
} from 'naive-ui'
import { api, fmtAgo, fmtTime, type AppRow, type Command, type ConfigTarget, type Metric, type Node } from '../api'
import { onEvent } from '../api/ws'
import AppDetailDrawer from '../components/AppDetailDrawer.vue'
import CommandDetailDrawer from '../components/CommandDetailDrawer.vue'

const route = useRoute()
const router = useRouter()
const message = useMessage()
const nodeId = route.params.id as string

const node = ref<Node | null>(null)
const metric = ref<Metric | null>(null)
const apps = ref<AppRow[]>([])
const commands = ref<Command[]>([])
const configs = ref<ConfigTarget[]>([])
const tab = ref('overview')

async function loadNode() {
  try {
    const data = await api.get<{ node: Node; latest_metric: Metric | null }>(`/nodes/${nodeId}`)
    node.value = data.node
    metric.value = data.latest_metric
  } catch (e: any) { message.error(e.message) }
}

async function loadApps() {
  const data = await api.get<{ data: AppRow[] }>(`/nodes/${nodeId}/apps`)
  apps.value = data.data ?? []
}

async function loadCommands() {
  const data = await api.get<{ data: Command[] }>(`/commands?node_id=${nodeId}&page_size=20`)
  commands.value = data.data ?? []
}

async function loadConfigs() {
  const data = await api.get<{ data: ConfigTarget[] }>(`/nodes/${nodeId}/configs`)
  configs.value = data.data ?? []
}

async function probeNow() {
  try {
    await api.post(`/nodes/${nodeId}/probe`, {})
    message.success('已触发即时探测，稍后刷新查看')
    setTimeout(loadApps, 2000)
  } catch (e: any) { message.error(e.message) }
}

// 实时：心跳快照直达概览卡片
let off: (() => void) | null = null
onMounted(() => {
  loadNode(); loadApps(); loadCommands(); loadConfigs()
  off = onEvent((ev) => {
    if (ev.node_id !== nodeId) return
    if (ev.topic === 'node.snapshot' && tab.value === 'overview') {
      const p = ev.payload
      metric.value = {
        node_id: nodeId,
        cpu: p.cpu_percent ?? 0,
        mem_used: p.mem?.used_mb ?? 0, mem_total: p.mem?.total_mb ?? 0,
        disk_used: p.disk?.used_gb ?? 0, disk_total: p.disk?.total_gb ?? 0,
        net_rx: p.net?.rx_kb_s ?? 0, net_tx: p.net?.tx_kb_s ?? 0,
        load1: p.load1 ?? 0,
      }
    } else if (ev.topic === 'report.updated') {
      loadApps()
    } else if (ev.topic === 'command.status') {
      loadCommands()
    }
  })
})
onUnmounted(() => off?.())

const memPct = computed(() =>
  metric.value && metric.value.mem_total ? Math.round((metric.value.mem_used / metric.value.mem_total) * 100) : null)
const diskPct = computed(() =>
  metric.value && metric.value.disk_total ? Math.round((metric.value.disk_used / metric.value.disk_total) * 100) : null)

const appColumns: DataTableColumns<AppRow> = [
  { title: '应用', key: 'app_name', minWidth: 150, render: (r) => h('span', { style: 'font-weight:600' }, r.app_name) },
  { title: '类型', key: 'app_type', width: 90, render: (r) => h(NTag, { size: 'tiny', bordered: false }, { default: () => r.app_type }) },
  { title: '版本', key: 'version', width: 150, render: (r) => h('span', { class: 'mono muted' }, r.version || '—') },
  {
    title: '状态', key: 'status', width: 90,
    render: (r) => {
      const map: Record<string, [string, string]> = {
        running: ['success', '运行中'], stopped: ['warning', '已停止'],
        installed: ['info', '已安装'], unknown: ['default', '未知'],
      }
      const [type, label] = map[r.status] ?? ['default', r.status]
      return h(NTag, { size: 'tiny', round: true, bordered: false, type: type as any }, { default: () => label })
    },
  },
  {
    title: '详情', key: 'detail', minWidth: 220,
    render: (r) => h('span', { class: 'muted mono', style: 'font-size:12px' }, detailSummary(r)),
  },
  { title: '探测时间', key: 'detected_at', width: 150, render: (r) => fmtTime(r.detected_at) },
  {
    title: '', key: 'op', width: 70,
    render: (r) => h(NButton, { size: 'tiny', quaternary: true, type: 'primary', onClick: () => openAppDetail(r) }, { default: () => '详情' }),
  },
]

function detailSummary(r: AppRow): string {
  const d = r.detail ?? {}
  const parts: string[] = []
  if (Array.isArray(d.ports)) {
    for (const p of d.ports) {
      if (p.public) parts.push(`${p.public}:${p.private}`)
    }
  } else if (d.ports) {
    parts.push(String(d.ports))
  }
  if (d.compose_project) parts.push(`compose:${d.compose_project}`)
  if (d.description) parts.push(String(d.description))
  if (d.reason) parts.push(String(d.reason))
  return parts.join(' · ') || '—'
}

const cmdColumns: DataTableColumns<Command> = [
  { title: '指令', key: 'type', width: 110, render: (r) => h('span', { class: 'mono' }, r.type) },
  {
    title: '状态', key: 'status', width: 90,
    render: (r) => {
      const map: Record<string, string> = {
        success: 'success', failed: 'error', lost: 'warning',
        running: 'info', pending: 'default', sent: 'info', expired: 'default',
      }
      return h(NTag, { size: 'tiny', round: true, bordered: false, type: (map[r.status] ?? 'default') as any }, { default: () => r.status })
    },
  },
  {
    title: '内容', key: 'payload', minWidth: 200,
    render: (r) => h('span', { class: 'mono muted', style: 'font-size:12px' },
      r.type === 'exec' ? (r.payload?.cmd ?? '') : r.type === 'config.apply' ? (r.payload?.target_path ?? '') : ''),
  },
  { title: '发起人', key: 'created_by', width: 80 },
  { title: '时间', key: 'created_at', width: 150, render: (r) => fmtTime(r.created_at) },
  {
    title: '', key: 'op', width: 70,
    render: (r) => h(NButton, { size: 'tiny', quaternary: true, type: 'primary', onClick: () => openCmdDetail(r) }, { default: () => '详情' }),
  },
]

// ---------- 共享抽屉 ----------
const appDetailRow = ref<AppRow | null>(null)
const showAppDetail = ref(false)
function openAppDetail(r: AppRow) {
  appDetailRow.value = r
  showAppDetail.value = true
}

const cmdDetailId = ref<string | null>(null)
const showCmdDetail = ref(false)
function openCmdDetail(c: Command) {
  cmdDetailId.value = c.command_id
  showCmdDetail.value = true
}

const cfgColumns: DataTableColumns<ConfigTarget> = [
  { title: '应用', key: 'app_type', width: 90, render: (r) => h(NTag, { size: 'tiny', bordered: false }, { default: () => r.app_type }) },
  { title: '目标路径', key: 'target_path', minWidth: 260, render: (r) => h('span', { class: 'mono' }, r.target_path) },
  { title: '最近下发', key: 'last_apply_status', width: 110, render: (r) => r.last_apply_status || '—' },
  { title: '', key: 'op', width: 110, render: (r) => h(NButton, { size: 'tiny', type: 'primary', quaternary: true, onClick: () => openConfig(r) }, { default: () => '版本管理 →' }) },
]

function openConfig(t: ConfigTarget) {
  router.push(`/nodes/${nodeId}/configs/${t.id}`)
}

// ---------- 新建配置目标 ----------

const showCreateCfg = ref(false)
const createAppType = ref<string | null>('nginx')
const createPath = ref('')
const createContent = ref('')

async function createConfig() {
  if (!createPath.value.startsWith('/')) {
    message.error('目标路径必须为绝对路径')
    return
  }
  try {
    const resp = await api.post<{ target_id: number; version_id: number }>(`/nodes/${nodeId}/configs`, {
      app_type: createAppType.value, target_path: createPath.value, content: createContent.value,
    })
    message.success(`配置目标已创建（首版草稿 v${resp.version_id}）`)
    showCreateCfg.value = false
    createPath.value = ''
    createContent.value = ''
    loadConfigs()
    router.push(`/nodes/${nodeId}/configs/${resp.target_id}`)
  } catch (e: any) { message.error(e.message) }
}
</script>

<template>
  <div class="page">
    <div class="page-title">
      <n-tag v-if="node" :type="node.status === 'online' ? 'success' : 'warning'" round size="small" :bordered="false">
        {{ node.status === 'online' ? '在线' : '离线' }}
      </n-tag>
      {{ node?.name ?? nodeId }}
      <span class="muted" style="font-size:13px; font-weight:400">
        最后心跳 {{ fmtAgo(node?.last_seen) }}
      </span>
      <n-button size="small" style="margin-left:auto" @click="probeNow">⟳ 立即探测</n-button>
    </div>

    <n-tabs v-model:value="tab" type="line" animated>
      <!-- 概览 -->
      <n-tab-pane name="overview" tab="概览">
        <n-card v-if="node" :bordered="false" style="margin-bottom:16px">
          <n-descriptions :column="3" size="small" label-placement="left">
            <n-descriptions-item label="主机名">{{ node.hostname }}</n-descriptions-item>
            <n-descriptions-item label="系统">{{ node.os_name }} {{ node.os_version }}</n-descriptions-item>
            <n-descriptions-item label="架构">{{ node.os_arch }}</n-descriptions-item>
            <n-descriptions-item label="Agent 版本"><span class="mono">{{ node.agent_version }}</span></n-descriptions-item>
            <n-descriptions-item label="协议版本"><span class="mono">{{ node.proto_version }}</span></n-descriptions-item>
            <n-descriptions-item label="纳管时间">{{ fmtTime(node.created_at) }}</n-descriptions-item>
            <n-descriptions-item label="探测能力">
              <n-space :size="4">
                <n-tag v-for="c in node.capabilities" :key="c" size="tiny" :bordered="false">{{ c }}</n-tag>
              </n-space>
            </n-descriptions-item>
          </n-descriptions>
        </n-card>

        <n-space :size="16" v-if="metric">
          <n-card title="CPU" size="small" style="width:260px">
            <n-progress type="line" :percentage="Math.round(metric.cpu)" indicator-placement="inside"
              :status="metric.cpu > 85 ? 'error' : metric.cpu > 60 ? 'warning' : 'success'" />
          </n-card>
          <n-card title="内存" size="small" style="width:260px">
            <n-progress v-if="memPct !== null" type="line" :percentage="memPct" indicator-placement="inside"
              :status="memPct > 90 ? 'error' : memPct > 75 ? 'warning' : 'success'" />
            <div class="muted" style="margin-top:6px; font-size:12px">
              {{ metric.mem_used }} / {{ metric.mem_total }} MB
            </div>
          </n-card>
          <n-card title="磁盘 /" size="small" style="width:260px">
            <n-progress v-if="diskPct !== null" type="line" :percentage="diskPct" indicator-placement="inside"
              :status="diskPct > 90 ? 'error' : diskPct > 75 ? 'warning' : 'success'" />
            <div class="muted" style="margin-top:6px; font-size:12px">
              {{ metric.disk_used }} / {{ metric.disk_total }} GB
            </div>
          </n-card>
          <n-card title="负载" size="small" style="width:180px">
            <div style="font-size:26px; font-weight:700">{{ metric.load1.toFixed(2) }}</div>
            <div class="muted" style="font-size:12px">load1（历史曲线 P1 提供）</div>
          </n-card>
        </n-space>
        <n-empty v-else description="暂无心跳快照" style="margin:30px 0" />
      </n-tab-pane>

      <!-- 应用 -->
      <n-tab-pane name="apps" tab="应用">
        <n-data-table :columns="appColumns" :data="apps" :bordered="false" size="small" />
        <n-empty v-if="apps.length === 0" description="该节点暂无应用清单（等待探测上报或点击「立即探测」）" style="margin:30px 0" />
      </n-tab-pane>

      <!-- 指令 -->
      <n-tab-pane name="commands" tab="指令">
        <n-data-table :columns="cmdColumns" :data="commands" :bordered="false" size="small" />
        <div class="muted" style="margin-top:10px; font-size:12px">最近 20 条；点击「详情」查看输出与阶段结果，全部历史见「指令中心」</div>
      </n-tab-pane>

      <!-- 配置 -->
      <n-tab-pane name="configs" tab="配置">
        <n-space style="margin-bottom:12px">
          <n-button size="small" type="primary" @click="showCreateCfg = true">＋ 新建配置目标</n-button>
        </n-space>
        <n-data-table :columns="cfgColumns" :data="configs" :bordered="false" size="small" />
        <n-empty v-if="configs.length === 0" description="暂无配置目标——点击「新建配置目标」开始管理该节点的应用配置" style="margin:30px 0" />

        <n-modal v-model:show="showCreateCfg" preset="dialog" title="新建配置目标" positive-text="创建" negative-text="取消" @positive-click="createConfig">
          <div style="margin-top:10px; display:flex; flex-direction:column; gap:12px">
            <div>
              <div class="muted" style="font-size:12px; margin-bottom:4px">应用类型（须已注册校验命令）</div>
              <n-select v-model:value="createAppType" :options="[{ label: 'nginx', value: 'nginx' }, { label: 'caddy', value: 'caddy' }]" />
            </div>
            <div>
              <div class="muted" style="font-size:12px; margin-bottom:4px">目标路径（绝对路径，如 /etc/nginx/conf.d/blog.conf）</div>
              <n-input v-model:value="createPath" class="mono" placeholder="/etc/nginx/conf.d/xxx.conf" />
            </div>
            <div>
              <div class="muted" style="font-size:12px; margin-bottom:4px">初始内容（保存为首版草稿，编辑/下发随时可做）</div>
              <n-input v-model:value="createContent" type="textarea" :rows="8" class="mono" placeholder="# 初始配置内容" />
            </div>
          </div>
        </n-modal>
      </n-tab-pane>
    </n-tabs>

    <!-- 共享详情抽屉 -->
    <AppDetailDrawer v-model:show="showAppDetail" :app="appDetailRow" />
    <CommandDetailDrawer v-model:show="showCmdDetail" :command-id="cmdDetailId" />
  </div>
</template>
