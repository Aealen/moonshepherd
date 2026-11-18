<script setup lang="ts">
import { computed, h, onMounted, onUnmounted, ref } from 'vue'
import {
  NCard, NDataTable, NTag, NButton, NInput, NInputNumber, NSelect, NSpace, NModal,
  NDescriptions, NDescriptionsItem, NPagination, NPopconfirm, useMessage, type DataTableColumns,
} from 'naive-ui'
import { api, fmtTime, type Command, type Node, type Page } from '../api'
import { onEvent } from '../api/ws'
import CommandDetailDrawer from '../components/CommandDetailDrawer.vue'

const message = useMessage()

// ---------- 发起指令(单选走同一 batch 端点;多选即批量下发) ----------
const nodes = ref<Node[]>([])
const execNodes = ref<string[]>([])
const execCmd = ref('')
const execTimeout = ref(120)
const execSending = ref(false)

const nodeOptions = computed(() =>
  nodes.value.map((n) => ({ label: n.name + (n.status === 'online' ? '' : '（离线）'), value: n.id })))
const selectAllOnline = () => {
  execNodes.value = nodes.value.filter((n) => n.status === 'online' && n.enabled).map((n) => n.id)
}

// ---------- 历史 ----------
const commands = ref<Command[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 20
const loading = ref(false)
const statusFilter = ref<string | null>(null)
const typeFilter = ref<string | null>(null)
// 批次视图:非空时列表只显示该批次的子指令,顶部显示聚合条
const batchFilter = ref('')
const batchStats = ref<Record<string, number>>({})

// ---------- 详情抽屉 ----------
const detailId = ref<string | null>(null)
const showDetail = ref(false)

const statusTag = (s: string) => {
  const map: Record<string, [string, string]> = {
    success: ['success', 'success'], failed: ['error', 'failed'],
    lost: ['warning', 'lost（结果未知）'], running: ['info', 'running'],
    pending: ['default', 'pending'], sent: ['info', 'sent'], expired: ['default', 'expired'],
  }
  const [type, label] = map[s] ?? ['default', s]
  return h(NTag, { size: 'tiny', round: true, bordered: false, type: type as any }, { default: () => label })
}

const columns: DataTableColumns<Command> = [
  { title: '时间', key: 'created_at', width: 160, render: (r) => h('span', { class: 'muted' }, fmtTime(r.created_at)) },
  { title: '指令', key: 'type', width: 110, render: (r) => h('span', { class: 'mono' }, r.type) },
  {
    title: '目标', key: 'payload', minWidth: 260,
    render: (r) => h('span', { class: 'mono muted', style: 'font-size:12px' }, payloadPreview(r)),
  },
  { title: '节点', key: 'node_id', width: 110, render: (r) => nodeName(r.node_id) },
  { title: '状态', key: 'status', width: 120, render: (r) => statusTag(r.status) },
  {
    title: '', key: 'op', width: 140,
    render: (r) => h(NSpace, { size: 4 }, {
      default: () => [
        h(NButton, { size: 'tiny', quaternary: true, onClick: () => openDetail(r) }, { default: () => '详情' }),
        (r.status === 'lost' || r.status === 'failed')
          ? h(NPopconfirm, { onPositiveClick: () => retry(r) }, {
              trigger: () => h(NButton, { size: 'tiny', quaternary: true, type: 'warning' }, { default: () => '重发' }),
              default: () => '重发将生成新指令再次执行（原指令保留在历史中），确定？',
            })
          : null,
      ],
    }),
  },
]

function payloadPreview(c: Command): string {
  if (c.type === 'exec') return c.payload?.cmd ?? ''
  if (c.type === 'probe') return (c.payload?.probe_names ?? []).join(', ') || '全部探测器'
  if (c.type === 'config.apply') return c.payload?.target_path ?? ''
  return ''
}

function nodeName(id: string): string {
  return nodes.value.find((n) => n.id === id)?.name ?? id.slice(0, 10) + '…'
}

async function loadNodes() {
  try {
    const data = await api.get<Page<Node>>('/nodes?page_size=100')
    nodes.value = data.data ?? []
    if (!execNodes.value.length && nodes.value.length) execNodes.value = [nodes.value[0].id]
  } catch { /* 忽略 */ }
}

async function loadBatchStats(batchID: string) {
  try {
    batchStats.value = await api.get<Record<string, number>>(`/commands/batch/${batchID}/stats`)
  } catch { batchStats.value = {} }
}

function exitBatchView() {
  batchFilter.value = ''
  batchStats.value = {}
  page.value = 1
  load()
}

async function load() {
  loading.value = true
  try {
    const params = new URLSearchParams({ page: String(page.value), page_size: String(pageSize) })
    if (statusFilter.value) params.set('status', statusFilter.value)
    if (typeFilter.value) params.set('type', typeFilter.value)
    if (batchFilter.value) params.set('parent', batchFilter.value)
    const data = await api.get<Page<Command>>(`/commands?${params}`)
    commands.value = data.data ?? []
    total.value = data.total
    if (batchFilter.value) loadBatchStats(batchFilter.value)
  } catch (e: any) {
    message.error(e.message)
  } finally {
    loading.value = false
  }
}

// 下发(单/多节点统一走 batch 端点):二次确认展示目标节点数与命令全文(docs/06 §4.2)
const showConfirm = ref(false)

async function sendExec() {
  if (!execNodes.value.length || !execCmd.value.trim()) return
  execSending.value = true
  try {
    const data = await api.post<{ batch_id: string; created: any[]; skipped: any[] }>('/commands/batch', {
      node_ids: execNodes.value, type: 'exec', timeout_s: execTimeout.value, payload: { cmd: execCmd.value },
    })
    const skippedNote = data.skipped.length ? `，${data.skipped.length} 个节点跳过（离线/禁用）` : ''
    message.success(`已下发 ${data.created.length} 条指令${skippedNote}`)
    execCmd.value = ''
    page.value = 1
    statusFilter.value = null
    typeFilter.value = null
    batchFilter.value = data.batch_id // 下发后进入批次视图,按节点分组看结果
    load()
  } catch (e: any) {
    message.error(e.message)
  } finally {
    execSending.value = false
    showConfirm.value = false
  }
}

// 批次聚合条:进行中(pending/sent/running) 成功 失败(lost 归失败侧展示) 跳过数
const batchSummary = computed(() => {
  const s = batchStats.value
  const done = (s.success ?? 0) + (s.failed ?? 0) + (s.lost ?? 0) + (s.expired ?? 0)
  const totalN = Object.values(s).reduce((a, b) => a + b, 0)
  return {
    total: totalN,
    running: (s.pending ?? 0) + (s.sent ?? 0) + (s.running ?? 0),
    success: s.success ?? 0,
    failed: (s.failed ?? 0) + (s.lost ?? 0) + (s.expired ?? 0),
    finished: totalN > 0 && done === totalN,
  }
})

function openDetail(c: Command) {
  detailId.value = c.command_id
  showDetail.value = true
}

async function retry(c: Command) {
  try {
    const data = await api.post<{ command_id: string }>(`/commands/${c.command_id}/retry`)
    message.success(`已重发：${data.command_id}`)
    load()
  } catch (e: any) { message.error(e.message) }
}

let off: (() => void) | null = null
onMounted(() => {
  loadNodes()
  load()
  off = onEvent((ev) => { if (ev.topic === 'command.status') load() })
})
onUnmounted(() => off?.())
</script>

<template>
  <div class="page">
    <div class="page-title">指令中心</div>
    <div class="page-sub">远程命令 · 全程审计留痕</div>

    <n-card title="发起远程命令" size="small" :bordered="false" style="margin-bottom:18px">
      <n-space align="center" :wrap="false" style="margin-bottom:10px">
        <n-select v-model:value="execNodes" :options="nodeOptions" multiple filterable
          placeholder="目标节点（可多选批量下发）" size="small" style="min-width:340px; flex:1" :max-tag-count="3" />
        <n-button size="small" @click="selectAllOnline">全部在线</n-button>
        <n-button size="small" quaternary @click="execNodes = nodeOptions.map((o) => o.value)">全选</n-button>
      </n-space>
      <n-space align="center">
        <n-input v-model:value="execCmd" placeholder="命令，如 df -h /（经 /bin/sh -c 执行，对各节点分别执行）" size="small"
          style="width:420px" @keyup.enter="showConfirm = execNodes.length > 0 && !!execCmd.trim()" />
        <span class="muted" style="font-size:12px">超时</span>
        <n-input-number v-model:value="execTimeout" size="small" style="width:100px" :min="5" :max="600" />
        <span class="muted" style="font-size:12px">秒</span>
        <n-button type="primary" size="small" :disabled="!execNodes.length || !execCmd.trim()" @click="showConfirm = true">
          执行（{{ execNodes.length }} 节点）
        </n-button>
      </n-space>
      <div class="muted" style="font-size:12px; margin-top:8px">
        命令输出上限 1MiB；批量下发为每节点一条独立指令，全程审计（docs/05 §3.3）；离线节点自动跳过
      </div>
    </n-card>

    <!-- 批次视图:聚合条 + 退出 -->
    <n-card v-if="batchFilter" size="small" :bordered="false" style="margin-bottom:14px">
      <div style="display:flex; align-items:center; gap:14px; flex-wrap:wrap">
        <span style="font-weight:600">批次结果</span>
        <span class="mono muted" style="font-size:12px">{{ batchFilter.slice(0, 14) }}…</span>
        <n-tag size="small" :bordered="false" :type="batchSummary.finished ? 'success' : 'info'">
          {{ batchSummary.finished ? '已完成' : `进行中 ${batchSummary.running}` }}
        </n-tag>
        <span style="font-size:12.5px">
          共 {{ batchSummary.total }} 节点 ·
          <span style="color:#63d3a6">成功 {{ batchSummary.success }}</span> ·
          <span style="color:#ef9d93">失败 {{ batchSummary.failed }}</span>
        </span>
        <n-button size="tiny" quaternary style="margin-left:auto" @click="exitBatchView">查看全部历史</n-button>
      </div>
    </n-card>

    <n-space style="margin-bottom:14px" align="center">
      <n-select v-model:value="statusFilter" size="small" style="width:130px" clearable placeholder="状态"
        :options="['pending', 'sent', 'running', 'success', 'failed', 'lost', 'expired'].map((s) => ({ label: s, value: s }))"
        @update:value="page = 1; load()" />
      <n-select v-model:value="typeFilter" size="small" style="width:130px" clearable placeholder="类型"
        :options="['exec', 'probe', 'config.apply'].map((t) => ({ label: t, value: t }))"
        @update:value="page = 1; load()" />
    </n-space>

    <n-data-table :columns="columns" :data="commands" :loading="loading" :bordered="false" size="small" />

    <div style="display:flex; justify-content:flex-end; margin-top:14px" v-if="total > pageSize">
      <n-pagination v-model:page="page" :page-size="pageSize" :item-count="total" @update:page="load" />
    </div>

    <!-- 二次确认：目标节点数 + 命令全文（docs/06 §4.2 交互规范） -->
    <n-modal v-model:show="showConfirm" preset="dialog" type="warning" title="确认批量执行远程命令"
      positive-text="执行" negative-text="取消" @positive-click="sendExec">
      <n-descriptions :column="1" size="small" style="margin-top:8px">
        <n-descriptions-item label="目标节点（{{ execNodes.length }}）">
          <span style="font-weight:600">{{ execNodes.map((id) => nodeName(id)).join('、') }}</span>
        </n-descriptions-item>
        <n-descriptions-item label="命令">
          <span class="mono" style="color:#ffb84d">{{ execCmd }}</span>
        </n-descriptions-item>
        <n-descriptions-item label="超时">{{ execTimeout }} 秒（每节点独立计时）</n-descriptions-item>
        <n-descriptions-item label="说明">每节点一条独立指令、独立审计；离线节点自动跳过</n-descriptions-item>
      </n-descriptions>
    </n-modal>

    <!-- 指令详情抽屉（共享组件）：输出 + config.apply 阶段时间线 -->
    <CommandDetailDrawer v-model:show="showDetail" :command-id="detailId" />
  </div>
</template>
