<script setup lang="ts">
import { h, onMounted, onUnmounted, ref } from 'vue'
import {
  NCard, NDataTable, NTag, NButton, NInput, NInputNumber, NSelect, NSpace, NModal,
  NDescriptions, NDescriptionsItem, NPagination, NPopconfirm, useMessage, type DataTableColumns,
} from 'naive-ui'
import { api, fmtTime, type Command, type Node, type Page } from '../api'
import { onEvent } from '../api/ws'
import CommandDetailDrawer from '../components/CommandDetailDrawer.vue'

const message = useMessage()

// ---------- 发起指令 ----------
const nodes = ref<Node[]>([])
const execNode = ref<string | null>(null)
const execCmd = ref('')
const execTimeout = ref(120)
const execSending = ref(false)

// ---------- 历史 ----------
const commands = ref<Command[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 20
const loading = ref(false)
const statusFilter = ref<string | null>(null)
const typeFilter = ref<string | null>(null)

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
    if (!execNode.value && nodes.value.length) execNode.value = nodes.value[0].id
  } catch { /* 忽略 */ }
}

async function load() {
  loading.value = true
  try {
    const params = new URLSearchParams({ page: String(page.value), page_size: String(pageSize) })
    if (statusFilter.value) params.set('status', statusFilter.value)
    if (typeFilter.value) params.set('type', typeFilter.value)
    const data = await api.get<Page<Command>>(`/commands?${params}`)
    commands.value = data.data ?? []
    total.value = data.total
  } catch (e: any) {
    message.error(e.message)
  } finally {
    loading.value = false
  }
}

// exec 创建：二次确认弹窗必须展示目标节点与命令全文（docs/06 §4.2）
const showConfirm = ref(false)

async function sendExec() {
  if (!execNode.value || !execCmd.value.trim()) return
  execSending.value = true
  try {
    await api.post(`/nodes/${execNode.value}/commands`, {
      type: 'exec', timeout_s: execTimeout.value, payload: { cmd: execCmd.value },
    })
    message.success('指令已创建')
    execCmd.value = ''
    page.value = 1
    load()
  } catch (e: any) {
    message.error(e.message)
  } finally {
    execSending.value = false
    showConfirm.value = false
  }
}

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
      <n-space align="center">
        <n-select v-model:value="execNode" :options="nodes.map((n) => ({ label: n.name + (n.status === 'online' ? '' : '（离线）'), value: n.id }))"
          placeholder="目标节点" size="small" style="width:220px" />
        <n-input v-model:value="execCmd" placeholder="命令，如 df -h /（经 /bin/sh -c 执行）" size="small"
          style="width:420px" @keyup.enter="showConfirm = true" />
        <span class="muted" style="font-size:12px">超时</span>
        <n-input-number v-model:value="execTimeout" size="small" style="width:100px" :min="5" :max="600" />
        <span class="muted" style="font-size:12px">秒</span>
        <n-button type="primary" size="small" :disabled="!execNode || !execCmd.trim()" @click="showConfirm = true">执行</n-button>
      </n-space>
      <div class="muted" style="font-size:12px; margin-top:8px">
        命令输出上限 1MiB；全部指令不可删除，全程审计（docs/05 §3.3）
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

    <!-- 二次确认：目标节点 + 命令全文（docs/06 §4.2 交互规范） -->
    <n-modal v-model:show="showConfirm" preset="dialog" type="warning" title="确认执行远程命令"
      positive-text="执行" negative-text="取消" @positive-click="sendExec">
      <n-descriptions :column="1" size="small" style="margin-top:8px">
        <n-descriptions-item label="目标节点">
          <span style="font-weight:600">{{ nodes.find((n) => n.id === execNode)?.name ?? execNode }}</span>
        </n-descriptions-item>
        <n-descriptions-item label="命令">
          <span class="mono" style="color:#ffb84d">{{ execCmd }}</span>
        </n-descriptions-item>
        <n-descriptions-item label="超时">{{ execTimeout }} 秒</n-descriptions-item>
      </n-descriptions>
    </n-modal>

    <!-- 指令详情抽屉（共享组件）：输出 + config.apply 阶段时间线 -->
    <CommandDetailDrawer v-model:show="showDetail" :command-id="detailId" />
  </div>
</template>
