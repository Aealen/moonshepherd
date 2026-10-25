<script setup lang="ts">
import { h, onMounted, ref } from 'vue'
import {
  NCard, NDataTable, NSpace, NTag, NInput, NButton, NPagination, NModal, NForm, NFormItem,
  useMessage, type DataTableColumns,
} from 'naive-ui'
import { api, fmtTime, type AuditLog, type Page } from '../api'

const message = useMessage()
const info = ref<{ panel_version: string; node_total: number; node_online: number; command_total: number } | null>(null)
const logs = ref<AuditLog[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 20

// 修改密码
const showPwd = ref(false)
const oldPwd = ref('')
const newPwd = ref('')
const newPwd2 = ref('')

const logColumns: DataTableColumns<AuditLog> = [
  { title: '时间', key: 'created_at', width: 160, render: (r) => h('span', { class: 'muted' }, fmtTime(r.created_at)) },
  { title: '用户', key: 'username', width: 100 },
  {
    title: '动作', key: 'action', width: 170,
    render: (r) => h(NTag, { size: 'tiny', bordered: false,
      type: r.action.includes('failed') ? 'error' : r.action.startsWith('auth') ? 'info' : 'default' },
      { default: () => r.action }),
  },
  { title: '目标', key: 'target', width: 220, render: (r) => h('span', { class: 'mono muted', style: 'font-size:12px' }, r.target || '—') },
  { title: 'IP', key: 'ip', width: 130, render: (r) => h('span', { class: 'mono muted' }, r.ip) },
  {
    title: '', key: 'op', width: 60,
    render: (r) => r.detail && Object.keys(r.detail).length
      ? h(NButton, { size: 'tiny', quaternary: true, type: 'primary', onClick: () => openAuditDetail(r) }, { default: () => '详情' })
      : null,
  },
]

// 审计详情（操作参数原始 JSON）
const auditDetail = ref<AuditLog | null>(null)
const showAuditDetail = ref(false)
function openAuditDetail(r: AuditLog) {
  auditDetail.value = r
  showAuditDetail.value = true
}

async function load() {
  try {
    info.value = await api.get('/system/info')
    const data = await api.get<Page<AuditLog>>(`/audit-logs?page=${page.value}&page_size=${pageSize}`)
    logs.value = data.data ?? []
    total.value = data.total
  } catch (e: any) { message.error(e.message) }
}

async function changePassword() {
  if (newPwd.value.length < 10) {
    message.error('新密码最短 10 位')
    return
  }
  if (newPwd.value !== newPwd2.value) {
    message.error('两次输入不一致')
    return
  }
  try {
    await api.put('/auth/password', { old_password: oldPwd.value, new_password: newPwd.value })
    message.success('密码已修改；其他设备的会话已全部失效')
    showPwd.value = false
    oldPwd.value = newPwd.value = newPwd2.value = ''
  } catch (e: any) { message.error(e.message) }
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-title">
      ⚙️ 系统
      <n-button size="small" style="margin-left:auto" @click="showPwd = true">修改密码</n-button>
    </div>

    <n-space :size="16" style="margin-bottom:18px">
      <n-card title="面板" size="small" style="width:240px">
        <div class="mono" style="font-size:18px; font-weight:700">{{ info?.panel_version ?? '…' }}</div>
        <div class="muted" style="font-size:12px; margin-top:4px">单二进制 · SQLite · 前端内嵌</div>
      </n-card>
      <n-card title="节点" size="small" style="width:240px">
        <div style="font-size:18px; font-weight:700">
          {{ info?.node_online ?? 0 }} <span class="muted" style="font-size:13px; font-weight:400">/ {{ info?.node_total ?? 0 }} 在线</span>
        </div>
      </n-card>
      <n-card title="累计指令" size="small" style="width:240px">
        <div style="font-size:18px; font-weight:700">{{ info?.command_total ?? 0 }}</div>
        <div class="muted" style="font-size:12px">审计不可删除（docs/05 §3.3）</div>
      </n-card>
    </n-space>

    <div style="font-weight:600; margin-bottom:10px">操作审计</div>
    <n-data-table :columns="logColumns" :data="logs" :bordered="false" size="small" />
    <div style="display:flex; justify-content:flex-end; margin-top:14px" v-if="total > pageSize">
      <n-pagination v-model:page="page" :page-size="pageSize" :item-count="total" @update:page="load" />
    </div>

    <n-modal v-model:show="showPwd" preset="dialog" title="修改密码" positive-text="修改" negative-text="取消" @positive-click="changePassword">
      <n-form label-placement="top" style="margin-top:10px">
        <n-form-item label="原密码"><n-input v-model:value="oldPwd" type="password" show-password-on="click" /></n-form-item>
        <n-form-item label="新密码（≥10 位）"><n-input v-model:value="newPwd" type="password" show-password-on="click" /></n-form-item>
        <n-form-item label="重复新密码"><n-input v-model:value="newPwd2" type="password" show-password-on="click" /></n-form-item>
      </n-form>
      <div class="muted" style="font-size:12px">修改成功后其他设备的会话将全部失效</div>
    </n-modal>

    <!-- 审计详情：操作参数原始 JSON -->
    <n-modal v-model:show="showAuditDetail" preset="card" :title="`审计详情 · ${auditDetail?.action ?? ''}`" style="width:560px">
      <pre class="term">{{ JSON.stringify(auditDetail?.detail ?? {}, null, 2) }}</pre>
      <div class="muted" style="font-size:12px; margin-top:8px">
        {{ auditDetail?.username }} @ {{ auditDetail?.ip }} · {{ fmtTime(auditDetail?.created_at) }}
      </div>
    </n-modal>
  </div>
</template>
