<script setup lang="ts">
import { h, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import {
  NDataTable, NTag, NInput, NSelect, NSpace, NPagination, NEmpty, NButton, useMessage, type DataTableColumns,
} from 'naive-ui'
import { api, fmtTime, type AppRow, type Node, type Page } from '../api'
import AppDetailDrawer from '../components/AppDetailDrawer.vue'

const router = useRouter()
const message = useMessage()

// ---------- 详情抽屉 ----------
const detailRow = ref<AppRow | null>(null)
const showDetail = ref(false)

function openDetail(r: AppRow) {
  detailRow.value = r
  showDetail.value = true
}

const apps = ref<AppRow[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 20
const loading = ref(false)
const probeFilter = ref<string | null>(null)
const typeFilter = ref<string | null>(null)
const nodeFilter = ref<string | null>(null)
const query = ref('')

// 探测器/节点下拉选项随数据动态生成
const probeOptions = ref<{ label: string; value: string }[]>([])
const nodeOptions = ref<{ label: string; value: string }[]>([])

async function loadNodes() {
  try {
    const data = await api.get<Page<Node>>(`/nodes?page_size=100`)
    nodeOptions.value = (data.data ?? []).map((n) => ({ label: n.name, value: n.id }))
  } catch { /* 忽略 */ }
}

const columns: DataTableColumns<AppRow> = [
  {
    title: '应用', key: 'app_name', minWidth: 160,
    render: (r) => h('div', [
      h('div', { style: 'font-weight:600' }, r.app_name),
      r.version ? h('div', { class: 'muted mono', style: 'font-size:11.5px' }, r.version) : null,
    ]),
  },
  { title: '节点', key: 'node_name', width: 140, render: (r) => h('span', { style: 'cursor:pointer; color:#7ab2ff', onClick: () => router.push(`/nodes/${r.node_id}`) }, r.node_name) },
  { title: '类型', key: 'app_type', width: 100, render: (r) => h(NTag, { size: 'tiny', bordered: false }, { default: () => r.app_type }) },
  { title: '探测器', key: 'probe_name', width: 90, render: (r) => h('span', { class: 'muted mono', style: 'font-size:12px' }, r.probe_name) },
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
    title: '端口/备注', key: 'detail', minWidth: 180,
    render: (r) => h('span', { class: 'muted mono', style: 'font-size:12px' }, portSummary(r)),
  },
  { title: '探测时间', key: 'detected_at', width: 150, render: (r) => h('span', { class: 'muted' }, fmtTime(r.detected_at)) },
  {
    title: '', key: 'op', width: 70,
    render: (r) => h(NButton, { size: 'tiny', quaternary: true, type: 'primary', onClick: () => openDetail(r) }, { default: () => '详情' }),
  },
]

// 端口摘要：兼容数组（sock 探测）与字符串（旧数据/CLI 降级）
function portSummary(r: AppRow): string {
  const parts: string[] = []
  const p = r.detail?.ports
  if (Array.isArray(p)) {
    for (const e of p) {
      if (e.public) parts.push(`${e.public}:${e.private}`)
    }
  } else if (typeof p === 'string' && p) {
    parts.push(p)
  }
  if (r.detail?.compose_project) parts.push(`compose:${r.detail.compose_project}`)
  return parts.join(' · ') || '—'
}

async function load() {
  loading.value = true
  try {
    const params = new URLSearchParams({ page: String(page.value), page_size: String(pageSize) })
    if (probeFilter.value) params.set('probe', probeFilter.value)
    if (typeFilter.value) params.set('app_type', typeFilter.value)
    if (nodeFilter.value) params.set('node_id', nodeFilter.value)
    if (query.value) params.set('q', query.value)
    const data = await api.get<Page<AppRow>>(`/apps?${params}`)
    apps.value = data.data ?? []
    total.value = data.total
    // 动态补全下拉选项
    const probes = new Map<string, string>()
    apps.value.forEach((a) => probes.set(a.probe_name, a.probe_name))
    probeOptions.value = [...probes.entries()].map(([value, label]) => ({ label, value }))
  } catch (e: any) {
    message.error(e.message)
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  loadNodes()
  load()
})
</script>

<template>
  <div class="page">
    <div class="page-title">📦 应用中心 <span class="muted" style="font-size:13px; font-weight:400">跨节点 · 谁装了什么</span></div>

    <n-space style="margin-bottom:14px" align="center">
      <n-select v-model:value="nodeFilter" size="small" style="width:150px" clearable filterable placeholder="节点"
        :options="nodeOptions" @update:value="page = 1; load()" />
      <n-select v-model:value="probeFilter" size="small" style="width:120px" clearable placeholder="探测器"
        :options="probeOptions" @update:value="page = 1; load()" />
      <n-select v-model:value="typeFilter" size="small" style="width:120px" clearable placeholder="类型"
        :options="[
          { label: 'docker', value: 'docker' }, { label: 'nginx', value: 'nginx' },
          { label: 'caddy', value: 'caddy' }, { label: 'systemd', value: 'systemd' },
        ]" @update:value="page = 1; load()" />
      <n-input v-model:value="query" size="small" style="width:200px" placeholder="搜索应用名" clearable
        @keyup.enter="page = 1; load()" />
    </n-space>

    <n-data-table :columns="columns" :data="apps" :loading="loading" :bordered="false" size="small" />
    <n-empty v-if="!loading && apps.length === 0" description="暂无应用数据——等待节点探测上报，或到节点页触发「立即探测」" style="margin-top:40px" />

    <div style="display:flex; justify-content:flex-end; margin-top:14px" v-if="total > pageSize">
      <n-pagination v-model:page="page" :page-size="pageSize" :item-count="total" @update:page="load" />
    </div>

    <!-- 应用详情抽屉（共享组件） -->
    <AppDetailDrawer v-model:show="showDetail" :app="detailRow" />
  </div>
</template>
