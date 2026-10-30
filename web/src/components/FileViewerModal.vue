<script setup lang="ts">
// 远端文件查看/编辑弹窗:file.read 只读查看;白名单文本可切编辑态(file.op write 原子保存)。
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

// 编辑态
const editable = ref(false)
const editing = ref(false)
const editBuf = ref('')
const saving = ref(false)

// ack 等待(request_id → resolve;file.read 与 file.op 共用监听)
const pending = new Map<string, (ack: any) => void>()
let off: (() => void) | null = null
off = onEvent((ev) => {
  const rid = ev.payload?.request_id
  if (!rid) return
  const cb = pending.get(rid)
  if (cb && (ev.topic === 'file.read' || ev.topic === 'file.op')) {
    pending.delete(rid)
    cb(ev.payload)
  }
})

function wait(rid: string, ms = 15000): Promise<any> {
  return new Promise((resolve) => {
    pending.set(rid, resolve)
    setTimeout(() => { if (pending.has(rid)) { pending.delete(rid); resolve({ error: '响应超时' }) } }, ms)
  })
}

async function fetchContent() {
  loading.value = true
  error.value = ''
  content.value = ''
  meta.value = null
  try {
    const resp = await api.post<{ request_id: string }>(`/nodes/${props.nodeID}/file/read`, { path: props.path })
    const ack = await wait(resp.request_id)
    if (ack.error) { error.value = ack.error; return }
    content.value = String(ack.content ?? '')
    meta.value = { size: Number(ack.size ?? 0), truncated: !!ack.truncated }
    // 可编辑性:文本且未截断(≤512KB)
    editable.value = !ack.truncated && meta.value.size <= 524288
  } catch (e: any) {
    error.value = e.message
  } finally {
    loading.value = false
  }
}

function startEdit() {
  editBuf.value = content.value
  editing.value = true
}

async function saveEdit() {
  saving.value = true
  try {
    const resp = await api.post<{ request_id: string }>(`/nodes/${props.nodeID}/file/op`, {
      op: 'write', path: props.path, content: editBuf.value,
    })
    const ack = await wait(resp.request_id)
    if (ack.error) { message.error(ack.denied ? `权限不足:${ack.error}` : ack.error); return }
    message.success('已保存(原子写入)')
    editing.value = false
    fetchContent()
  } catch (e: any) {
    message.error(e.message)
  } finally {
    saving.value = false
  }
}

const sizeLabel = () => {
  if (!meta.value) return ''
  const kb = meta.value.size / 1024
  return kb >= 1024 ? (kb / 1024).toFixed(1) + ' MB' : kb.toFixed(1) + ' KB'
}

watch(show, (v) => { if (v) fetchContent(); else { editing.value = false } })
onBeforeUnmount(() => off?.())
</script>

<template>
  <n-modal v-model:show="show" preset="card" :title="`文件 · ${path}`" style="width:880px">
    <div style="display:flex; gap:8px; align-items:center; margin-bottom:10px">
      <n-tag size="tiny" :bordered="false" :type="editing ? 'warning' : 'info'">{{ editing ? '编辑' : '只读' }}</n-tag>
      <span v-if="meta" class="mono muted" style="font-size:12px">
        {{ sizeLabel() }}{{ meta.truncated ? ' · 超出 512KB 已截断' : '' }}
      </span>
      <div style="flex:1"></div>
      <n-button size="tiny" quaternary @click="fetchContent" :disabled="editing">刷新</n-button>
      <template v-if="!editing">
        <n-button v-if="editable" size="tiny" type="primary" secondary @click="startEdit">编辑</n-button>
      </template>
      <template v-else>
        <n-button size="tiny" @click="editing = false">取消</n-button>
        <n-button size="tiny" type="primary" :loading="saving" @click="saveEdit">保存</n-button>
      </template>
    </div>
    <div v-if="loading" class="muted" style="padding:30px; text-align:center">正在读取…</div>
    <div v-else-if="error" style="padding:20px; color:#ef9d93; font-size:13px">{{ error }}</div>
    <textarea v-else-if="editing" v-model="editBuf" class="fm-edit-area mono"></textarea>
    <pre v-else class="term" style="max-height:58vh; margin:0">{{ content || '（空文件）' }}</pre>
    <div class="muted" style="font-size:11.5px; margin-top:8px">
      {{ editing ? '保存为原子写入(临时文件+rename);超 512KB 请用文件管理器的上传' : '只读查看;文本文件可切编辑态,二进制/超限只读' }}
    </div>
  </n-modal>
</template>

<style scoped>
.fm-edit-area {
  width: 100%; min-height: 52vh; resize: vertical;
  background: #0c0f0e; color: #c6d0ca; border: 1px solid #272e2c; border-radius: 8px;
  padding: 12px 14px; font-size: 12.5px; line-height: 1.65; outline: none;
}
.fm-edit-area:focus { border-color: rgba(99, 211, 166, 0.4); }
</style>
