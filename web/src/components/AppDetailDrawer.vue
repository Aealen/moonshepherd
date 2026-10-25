<script setup lang="ts">
// 应用详情抽屉（共享组件）：应用中心 + 节点详情·应用 tab 复用。
import { computed, h } from 'vue'
import {
  NDrawer, NDrawerContent, NDescriptions, NDescriptionsItem, NDataTable, NTag,
} from 'naive-ui'
import { fmtTime, type AppRow } from '../api'

const props = defineProps<{ show: boolean; app: AppRow | null }>()
const emit = defineEmits<{ (e: 'update:show', v: boolean): void }>()

interface PortEntry { ip: string; public: number; private: number; type: string }
interface MountEntry { source: string; dest: string; rw: boolean; mode?: string }
interface NetEntry { name: string; ip: string }

const ports = computed<PortEntry[]>(() => {
  const p = props.app?.detail?.ports
  return Array.isArray(p) ? p : []
})
const mounts = computed<MountEntry[]>(() => {
  const m = props.app?.detail?.mounts
  return Array.isArray(m) ? m : []
})
const nets = computed<NetEntry[]>(() => {
  const n = props.app?.detail?.networks
  return Array.isArray(n) ? n : []
})
// env "KEY=VAL" → 表格行；无 = 的整行当 key
const envRows = computed(() => {
  const e = props.app?.detail?.env
  if (!Array.isArray(e)) return []
  return e.map((s) => {
    const i = String(s).indexOf('=')
    return i > 0 ? { k: String(s).slice(0, i), v: String(s).slice(i + 1) } : { k: String(s), v: '' }
  })
})
// 兜底：未结构化展示的剩余字段（含 compose_* 等）
const restEntries = computed<Array<[string, any]>>(() => {
  if (!props.app) return []
  const skip = new Set(['image', 'id', 'ports', 'mounts', 'networks', 'env'])
  return Object.entries(props.app.detail ?? {}).filter(([k]) => !skip.has(k))
})

const portCols = [
  { title: '宿主地址', key: 'ip', width: 120, render: (p: PortEntry) => h('span', { class: 'mono' }, p.ip || '0.0.0.0') },
  { title: '宿主端口', key: 'public', width: 100, render: (p: PortEntry) => h('span', { class: 'mono' }, p.public ? String(p.public) : '—') },
  { title: '容器端口', key: 'private', width: 100, render: (p: PortEntry) => h('span', { class: 'mono' }, String(p.private)) },
  { title: '协议', key: 'type', width: 80, render: (p: PortEntry) => h(NTag, { size: 'tiny', bordered: false }, { default: () => p.type || 'tcp' }) },
]
const mountCols = [
  { title: '宿主路径', key: 'source', render: (m: MountEntry) => h('span', { class: 'mono', style: 'font-size:12px; word-break:break-all' }, m.source || m.mode || '—') },
  { title: '容器路径', key: 'dest', width: 200, render: (m: MountEntry) => h('span', { class: 'mono muted', style: 'font-size:12px; word-break:break-all' }, m.dest) },
  { title: '权限', key: 'rw', width: 70, render: (m: MountEntry) => h(NTag, { size: 'tiny', bordered: false, type: m.rw ? 'warning' : 'default' }, { default: () => m.rw ? '读写' : '只读' }) },
]
const envCols = [
  { title: '变量', key: 'k', width: 190, render: (e: { k: string }) => h('span', { class: 'mono', style: 'font-weight:600' }, e.k) },
  { title: '值', key: 'v', render: (e: { v: string }) => h('span', { class: 'mono muted', style: 'font-size:12px; word-break:break-all' }, e.v || '（空）') },
]
</script>

<template>
  <n-drawer :show="show" :width="640" placement="right" @update:show="emit('update:show', $event)">
    <n-drawer-content v-if="app" closable>
      <template #header>
        <span style="font-weight:700">{{ app.app_name }}</span>
        <n-tag size="tiny" round :bordered="false" style="margin-left:10px"
          :type="app.status === 'running' ? 'success' : app.status === 'stopped' ? 'warning' : 'default'">
          {{ app.status === 'running' ? '运行中' : app.status === 'stopped' ? '已停止' : app.status }}
        </n-tag>
        <n-tag size="tiny" :bordered="false" style="margin-left:6px">{{ app.app_type }}</n-tag>
      </template>

      <n-descriptions :column="1" size="small" bordered label-placement="left" style="margin-bottom:18px">
        <n-descriptions-item label="镜像"><span class="mono">{{ app.detail?.image || app.version || '—' }}</span></n-descriptions-item>
        <n-descriptions-item v-if="app.detail?.id" label="容器 ID">
          <span class="mono muted">{{ String(app.detail.id).slice(0, 12) }}</span>
        </n-descriptions-item>
        <n-descriptions-item v-if="app.detail?.compose_project" label="Compose 项目">
          <span class="mono">{{ app.detail.compose_project }}</span>
        </n-descriptions-item>
        <n-descriptions-item v-if="nets.length" label="网络">
          <span class="mono" style="font-size:12px">{{ nets.map((n) => `${n.name}${n.ip ? ' (' + n.ip + ')' : ''}`).join('，') }}</span>
        </n-descriptions-item>
        <n-descriptions-item v-if="app.node_name" label="所属节点">{{ app.node_name }}（{{ fmtTime(app.detected_at) }} 探测）</n-descriptions-item>
      </n-descriptions>

      <template v-if="ports.length">
        <div style="font-weight:600; margin:0 0 8px">端口映射</div>
        <n-data-table size="small" :bordered="false" :single-line="false" style="margin-bottom:18px"
          :columns="portCols" :data="ports" :row-key="(p: any) => `${p.ip}:${p.public}:${p.private}`" />
      </template>

      <template v-if="mounts.length">
        <div style="font-weight:600; margin:0 0 8px">目录挂载</div>
        <n-data-table size="small" :bordered="false" :single-line="false" style="margin-bottom:18px"
          :columns="mountCols" :data="mounts" :row-key="(m: any) => m.source + m.dest" />
      </template>

      <template v-if="envRows.length">
        <div style="font-weight:600; margin:0 0 8px">环境变量（{{ envRows.length }}）</div>
        <n-data-table size="small" :bordered="false" :single-line="false"
          :columns="envCols" :data="envRows" :row-key="(e: any) => e.k" />
      </template>

      <template v-if="restEntries.length">
        <div style="font-weight:600; margin:0 0 8px">其他</div>
        <div class="term" style="padding:8px 12px"><template v-for="[k, v] in restEntries" :key="k">
{{ k }}: {{ typeof v === 'object' ? JSON.stringify(v) : v }}
</template></div>
      </template>
    </n-drawer-content>
  </n-drawer>
</template>
