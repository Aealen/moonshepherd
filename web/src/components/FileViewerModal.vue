<script setup lang="ts">
// 远端文件只读查看弹窗(docs/04 §10):POST 发起 → events WSS 收 file.read ack。
// 纯只读:无编辑能力,编辑走配置管理纳管工作流。
import { onBeforeUnmount, ref, watch } from 'vue'
import { NButton, NModal, NTag, useMessage } from 'naive-ui'
import { api } from '../api'
import { onEvent } from '../api/ws'

const props = defineProps<{ nodeID: string; path: string }>()
const show = defineModel<boolean>('show', { default: false })
const message = useMessage()

const loading = ref(false)
const meta = ref<{ size: number; truncated: boolean } | null>(null)
const content = ref('')
const error = ref('')
let requestID: string | null = null
let off: (() => void) | null = null

const sizeLabel = () => {
  if (!meta.value) return ''
  const kb = meta.value.size / 1024
  return kb >= 1024 ? (kb / 1024).toFixed(1) + ' MB' : kb.toFixed(1) + ' KB'
}

async function fetchContent() {
  loading.value = true
  error.value = ''
  content.value = ''
  meta.value = null
  try {
    const data = await api.post<{ request_id: string }>(`/nodes/${props.nodeID}/file/read`, { path: props.path })
    requestID = data.request_id
  } catch (e: any) {
    message.error(e.message)
    error.value = e.message
    loading.value = false
  }
}

off = onEvent((ev) => {
  if (ev.topic !== 'file.read' || ev.payload?.request_id !== requestID) return
  loading.value = false
  const p = ev.payload
  if (p.error) {
    error.value = p.error
    return
  }
  content.value = String(p.content ?? '')
  meta.value = { size: Number(p.size ?? 0), truncated: !!p.truncated }
})

watch(show, (v) => { if (v) fetchContent() })

onBeforeUnmount(() => off?.())
</script>

<template>
  <n-modal v-model:show="show" preset="card" :title="`文件查看 · ${path}`" style="width:880px">
    <div style="display:flex; gap:8px; align-items:center; margin-bottom:10px">
      <n-tag size="tiny" :bordered="false" type="info">只读</n-tag>
      <span v-if="meta" class="mono muted" style="font-size:12px">
        {{ sizeLabel() }}{{ meta.truncated ? ' · 超出 512KB 已截断' : '' }}
      </span>
      <div style="flex:1"></div>
      <n-button size="tiny" quaternary @click="fetchContent">刷新</n-button>
    </div>
    <div v-if="loading" class="muted" style="padding:30px; text-align:center">正在读取…</div>
    <div v-else-if="error" style="padding:20px; color:#ff8f9a; font-size:13px">{{ error }}</div>
    <pre v-else class="term" style="max-height:60vh; margin:0">{{ content || '（空文件）' }}</pre>
    <div class="muted" style="font-size:11.5px; margin-top:8px">
      只读查看;要编辑请走「配置管理」纳管后在线下发。
    </div>
  </n-modal>
</template>
