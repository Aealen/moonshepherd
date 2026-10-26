<script setup lang="ts">
// 操作审计独立页(自 System 拆出;docs/05 §3.3:审计不可删除,只读+详情)
import { h, onMounted, ref } from 'vue'
import {
  NDataTable, NTag, NSelect, NInput, NButton, NPagination, NModal, NSpace,
  type DataTableColumns,
} from 'naive-ui'
import { api, fmtTime, type AuditLog, type Page } from '../api'

const logs = ref<AuditLog[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 20
const loading = ref(false)
const actionFilter = ref<string | null>(null)
const targetQuery = ref('')

const logColumns: DataTableColumns<AuditLog> = [
  { title: '时间', key: 'created_at', width: 165, render: (r) => h('span', { class: 'muted' }, fmtTime(r.created_at)) },
  { title: '用户', key: 'username', width: 95 },
  {
    title: '动作', key: 'action', width: 175,
    render: (r) => {
      const a = r.action
      const type = a.includes('failed') || a.includes('password_reset') ? 'error'
        : a.startsWith('session.') ? 'success'
        : a.startsWith('auth') ? 'info'
        : a.startsWith('config') ? 'warning'
        : 'default'
      return h(NTag, { size: 'tiny', bordered: false, type }, { default: () => a })
    },
  },
  { title: '目标', key: 'target', width: 210, render: (r) => h('span', { class: 'mono muted', style: 'font-size:12px' }, r.target || '—') },
  { title: 'IP', key: 'ip', width: 130, render: (r) => h('span', { class: 'mono muted' }, r.ip) },
  {
    title: '', key: 'op', width: 60,
    render: (r) => r.detail && Object.keys(r.detail).length
      ? h(NButton, { size: 'tiny', quaternary: true, type: 'primary', onClick: () => openAuditDetail(r) }, { default: () => '详情' })
      : null,
  },
]

// 审计详情(操作参数原始 JSON)
const auditDetail = ref<AuditLog | null>(null)
const showAuditDetail = ref(false)
function openAuditDetail(r: AuditLog) {
  auditDetail.value = r
  showAuditDetail.value = true
}

async function load() {
  loading.value = true
  try {
    const params = new URLSearchParams({ page: String(page.value), page_size: String(pageSize) })
    if (actionFilter.value) params.set('action', actionFilter.value)
    const data = await api.get<Page<AuditLog>>(`/audit-logs?${params}`)
    logs.value = data.data ?? []
    total.value = data.total
  } finally {
    loading.value = false
  }
}

const actionOptions = ['auth', 'session', 'command', 'config', 'node', 'token'].map((p) => ({ label: p + '.*', value: p }))

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-title">操作审计</div>
    <div class="page-sub">全部操作留痕 · 只读不可删除（docs/05 §3.3）</div>

    <n-space style="margin-bottom:14px" align="center">
      <n-select v-model:value="actionFilter" size="small" style="width:150px" clearable
        placeholder="动作前缀" :options="actionOptions" @update:value="page = 1; load()" />
      <n-input v-model:value="targetQuery" size="small" style="width:220px" placeholder="目标(节点ID等)" clearable />
      <n-button size="small" @click="page = 1; load()">查询</n-button>
    </n-space>

    <n-data-table :columns="logColumns" :data="logs" :loading="loading" :bordered="false" size="small" />
    <div style="display:flex; justify-content:flex-end; margin-top:14px" v-if="total > pageSize">
      <n-pagination v-model:page="page" :page-size="pageSize" :item-count="total" @update:page="load" />
    </div>

    <n-modal v-model:show="showAuditDetail" preset="card" :title="`审计详情 · ${auditDetail?.action ?? ''}`" style="width:560px">
      <pre class="term">{{ JSON.stringify(auditDetail?.detail ?? {}, null, 2) }}</pre>
      <div class="muted" style="font-size:12px; margin-top:8px">
        {{ auditDetail?.username }} @ {{ auditDetail?.ip }} · {{ fmtTime(auditDetail?.created_at) }}
      </div>
    </n-modal>
  </div>
</template>
