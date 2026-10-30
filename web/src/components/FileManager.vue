<script setup lang="ts">
// 节点文件管理器(docs/04 §11):左目录树 + 右条目表(面包屑/右键菜单/拖拽上传/文本编辑)。
import { computed, h, onBeforeUnmount, onMounted, ref } from 'vue'
import {
  NButton, NModal, NInput, NTag, NDataTable, NDropdown, NIcon, NTree,
  useMessage, type DataTableColumns,
} from 'naive-ui'
import { FolderOpenOutline, DocumentOutline, LockClosedOutline } from '@vicons/ionicons5'
import { api } from '../api'
import { onEvent } from '../api/ws'
import FileViewerModal from './FileViewerModal.vue'

const props = defineProps<{ nodeID: string; online: boolean }>()
const message = useMessage()

const cwd = ref('/')
const entries = ref<any[]>([])
const loading = ref(false)

interface TreeNode { name: string; path: string; children?: any[]; noPerm?: boolean }
const treeData = ref<TreeNode[]>([{ name: '/', path: '/', children: undefined }])

const pending = new Map<string, (ack: any) => void>()
let off: (() => void) | null = null

function waitFor(requestID: string): Promise<any> {
  return new Promise((resolve) => {
    pending.set(requestID, resolve)
    setTimeout(() => {
      if (pending.has(requestID)) { pending.delete(requestID); resolve({ error: '响应超时(节点繁忙或离线)' }) }
    }, 15000)
  })
}

async function fileOp(op: string, body: Record<string, unknown>): Promise<any> {
  try {
    const resp = await api.post<{ request_id: string }>(`/nodes/${props.nodeID}/file/op`, { op, ...body })
    return await waitFor(resp.request_id)
  } catch (e: any) {
    return { error: e.message }
  }
}

off = onEvent((ev) => {
  if (ev.topic !== 'file.op' || ev.node_id !== props.nodeID) return
  const cb = pending.get(ev.payload?.request_id)
  if (cb) { pending.delete(ev.payload?.request_id); cb(ev.payload) }
})

const join = (dir: string, name: string) => (dir === '/' ? '' : dir) + '/' + name

async function loadDir(path: string) {
  loading.value = true
  try {
    const ack = await fileOp('list', { path })
    if (ack.error) { message.error(ack.denied ? `权限不足:${ack.error}` : ack.error); return }
    cwd.value = path
    entries.value = ack.entries ?? []
  } finally { loading.value = false }
}

function renderTreeIcon(o: any) {
  return () => h(NIcon, { size: 15 }, { default: () => h(o.noPerm ? LockClosedOutline : FolderOpenOutline) })
}

async function loadChildren(node: any): Promise<void> {
  const ack = await fileOp('list', { path: node.path })
  if (ack.error) { node.children = [{ name: '(无权限)', path: node.path + '/__lock__', noPerm: true, children: [] }]; return }
  node.children = (ack.entries ?? [])
    .filter((e: any) => e.dir)
    .map((d: any) => ({ name: d.name, path: join(node.path, d.name), children: undefined }))
}

const showInput = ref(false)
const inputTitle = ref('')
const inputValue = ref('')
let inputResolve: ((v: string | null) => void) | null = null
function askText(title: string, def = ''): Promise<string | null> {
  inputTitle.value = title
  inputValue.value = def
  showInput.value = true
  return new Promise((r) => { inputResolve = r })
}
function inputOk() { showInput.value = false; inputResolve?.(inputValue.value); inputResolve = null }
function inputCancel() { showInput.value = false; inputResolve?.(null); inputResolve = null }

const clip = ref<{ path: string } | null>(null)

const fmtSize = (n?: number) => {
  if (!n) return '—'
  if (n >= 1073741824) return (n / 1073741824).toFixed(1) + ' GB'
  if (n >= 1048576) return (n / 1048576).toFixed(1) + ' MB'
  if (n >= 1024) return (n / 1024).toFixed(1) + ' KB'
  return n + ' B'
}

const viewerShow = ref(false)
const viewerPath = ref('')
function openViewer(path: string) { viewerPath.value = path; viewerShow.value = true }

function enter(r: any) {
  if (r.no_perm) return
  if (r.dir) loadDir(join(cwd.value, r.name))
  else openViewer(join(cwd.value, r.name))
}

function rowMenu(r: any) {
  const items: { label: string; key: string }[] = []
  if (!r.no_perm) {
    if (!r.dir) items.push({ label: '查看/编辑', key: 'edit' })
    items.push({ label: '重命名', key: 'rename' })
    items.push({ label: '移动到…', key: 'move' })
    items.push({ label: '复制到…', key: 'copy' })
    items.push({ label: '暂存(供粘贴)', key: 'stash' })
    items.push({ label: r.dir ? '删除(含内容)' : '删除', key: 'delete' })
  }
  return items
}

async function onRowMenu(key: string, r: any) {
  const full = join(cwd.value, r.name)
  if (key === 'edit') return openViewer(full)
  if (key === 'rename') {
    const name = await askText('重命名 → 新名称', r.name)
    if (!name || name === r.name) return
    const ack = await fileOp('rename', { path: full, dest: join(cwd.value, name) })
    ack.error ? message.error(ack.denied ? '权限不足' : ack.error) : (message.success('已重命名'), loadDir(cwd.value))
    return
  }
  if (key === 'move') {
    const dest = await askText('移动 → 目标绝对路径(含文件名)', full)
    if (!dest || dest === full) return
    const ack = await fileOp('move', { path: full, dest })
    ack.error ? message.error(ack.denied ? '权限不足(或目标不可写)' : ack.error) : (message.success('已移动'), loadDir(cwd.value))
    return
  }
  if (key === 'copy') {
    const dest = await askText('复制 → 目标绝对路径(含文件名)', full + '.copy')
    if (!dest) return
    const ack = await fileOp('copy', { path: full, dest })
    ack.error ? message.error(ack.error) : (message.success('已复制'), loadDir(cwd.value))
    return
  }
  if (key === 'stash') { clip.value = { path: full }; message.info(`已暂存 ${r.name},到目标目录点「粘贴」`); return }
  if (key === 'delete') {
    if (r.dir && !confirm(`删除目录「${r.name}」及其全部内容?不可恢复!`)) return
    const ack = await fileOp('delete', { path: full, recursive: !!r.dir })
    if (ack.error) {
      if (String(ack.error).includes('not empty')) { message.error('目录非空,请确认递归删除'); return }
      message.error(ack.denied ? '权限不足' : ack.error); return
    }
    message.success('已删除'); loadDir(cwd.value)
  }
}

async function pasteHere() {
  if (!clip.value) return
  const name = clip.value.path.split('/').pop()!
  const ack = await fileOp('copy', { path: clip.value.path, dest: join(cwd.value, name) })
  if (ack.error) { message.error(ack.denied ? '权限不足' : ack.error); return }
  message.success('已粘贴'); clip.value = null; loadDir(cwd.value)
}

async function mkdir() {
  const name = await askText('新建文件夹名称', 'new-folder')
  if (!name) return
  const ack = await fileOp('mkdir', { path: join(cwd.value, name) })
  ack.error ? message.error(ack.denied ? '权限不足' : ack.error) : (message.success('已创建'), loadDir(cwd.value))
}

const uploading = ref(false)
const uploadInput = ref<HTMLInputElement | null>(null)
const dragOver = ref(false)

async function uploadFiles(files: FileList | File[]) {
  if (!props.online) { message.error('节点不在线'); return }
  uploading.value = true
  try {
    for (const f of Array.from(files)) {
      const dest = join(cwd.value, f.name)
      const fd = new FormData()
      fd.append('file', f)
      const resp = await fetch(`/api/v1/nodes/${props.nodeID}/file/upload?path=${encodeURIComponent(dest)}`, {
        method: 'POST', body: fd, credentials: 'same-origin',
      })
      if (!resp.ok) {
        const e = await resp.json().catch(() => ({ error: { message: '上传失败' } }))
        message.error(`${f.name}: ${e.error?.message ?? resp.status}`)
        continue
      }
      const data = await resp.json().catch(() => ({}))
      message.success(`${f.name} 已上传(${Math.round((data.size ?? f.size) / 1024)}KB,校验通过)`)
    }
  } finally { uploading.value = false; loadDir(cwd.value) }
}

function onDrop(e: DragEvent) {
  dragOver.value = false
  if (e.dataTransfer?.files?.length) uploadFiles(e.dataTransfer.files)
}

const columns = computed<DataTableColumns<any>>(() => [
  {
    title: '名称', key: 'name', minWidth: 260,
    render: (r) => h('span', { style: 'cursor:pointer; display:inline-flex; align-items:center; gap:8px', onClick: () => enter(r) }, [
      h(NIcon, { size: 16, style: r.dir ? 'color:#e3c47f' : r.no_perm ? 'color:#66706b' : 'color:#8fb8a8' },
        { default: () => h(r.dir ? FolderOpenOutline : r.no_perm ? LockClosedOutline : DocumentOutline) }),
      h('span', { style: 'font-weight:500' }, r.name),
      r.no_perm ? h(NTag, { size: 'tiny', bordered: false }, { default: () => '无权限' }) : null,
    ]),
  },
  { title: '大小', key: 'size', width: 100, render: (r) => h('span', { class: 'mono muted', style: 'font-size:12px' }, r.dir ? '—' : fmtSize(r.size)) },
  { title: '权限', key: 'mode', width: 125, render: (r) => h('span', { class: 'mono muted', style: 'font-size:11.5px' }, r.mode ?? '') },
  { title: '修改时间', key: 'mtime', width: 165, render: (r) => h('span', { class: 'muted', style: 'font-size:12px' }, r.mtime ? new Date(r.mtime * 1000).toLocaleString() : '') },
  {
    title: '', key: 'op', width: 70,
    render: (r) => h(NDropdown, { trigger: 'click', options: rowMenu(r), onSelect: (k: string) => onRowMenu(k, r) }, {
      trigger: () => h(NButton, { size: 'tiny', quaternary: true }, { default: () => '···' }),
    }),
  },
])

const crumbs = computed(() => {
  const parts = cwd.value.split('/').filter(Boolean)
  return [{ label: '/', path: '/' }, ...parts.map((p, i) => ({ label: p, path: '/' + parts.slice(0, i + 1).join('/') }))]
})

onMounted(() => { if (props.online) loadDir('/') })
onBeforeUnmount(() => off?.())
</script>
<template>
  <div>
    <div v-if="!online" class="muted" style="padding:24px; text-align:center">节点离线,文件管理不可用</div>
    <div v-else style="display:flex; gap:14px; height:calc(100vh - 300px); min-height:440px">
      <!-- 左:目录树 -->
      <div class="fm-tree glass-card">
        <div class="fm-tree-title">目录</div>
        <n-tree block-line expand-on-click :data="treeData" :load-children="loadChildren"
          key-field="path" label-field="name" :render-icon="renderTreeIcon"
          @update:selected-keys="(k: any) => k?.[0] && !String(k[0]).endsWith('/__lock__') && loadDir(k[0])" />
      </div>
      <!-- 右:主区 -->
      <div style="flex:1; display:flex; flex-direction:column; min-width:0">
        <div style="display:flex; gap:8px; align-items:center; margin-bottom:10px; flex-wrap:wrap">
NaN
          <div class="fm-crumbs">
            <template v-for="(c, i) in crumbs" :key="c.path">
              <span v-if="i > 0" class="muted">/</span>
              <span class="crumb" @click="loadDir(c.path)">{{ c.label }}</span>
            </template>
          </div>
          <div style="flex:1"></div>
          <n-button size="tiny" @click="mkdir">＋ 文件夹</n-button>
          <n-button size="tiny" type="primary" :loading="uploading" @click="uploadInput?.click()">上传</n-button>
          <n-button v-if="clip" size="tiny" type="info" secondary @click="pasteHere">粘贴:{{ clip.path.split('/').pop() }}</n-button>
          <input ref="uploadInput" type="file" multiple hidden @change="uploadInput?.files && uploadFiles(uploadInput.files)" />
        </div>
        <div class="fm-drop" :class="{ over: dragOver }" @dragover.prevent="dragOver = true" @dragleave="dragOver = false" @drop.prevent="onDrop">
          <n-data-table :columns="columns" :data="entries" :loading="loading" :bordered="false" size="small" flex-height style="flex:1" />
          <div v-if="dragOver" class="fm-drop-hint">松开以上传到当前目录</div>
        </div>
      </div>
    </div>

    <n-modal v-model:show="showInput" preset="dialog" :title="inputTitle"
      positive-text="确定" negative-text="取消" @positive-click="inputOk" @negative-click="inputCancel">
      <n-input v-model:value="inputValue" class="mono" style="margin-top:8px" @keyup.enter="inputOk" />
    </n-modal>

    <FileViewerModal v-model:show="viewerShow" :nodeID="nodeID" :path="viewerPath" />
  </div>
</template>

<style scoped>
.fm-tree { width: 235px; flex-shrink: 0; padding: 10px 8px; overflow: auto; }
.fm-tree-title { font-size: 12px; color: #66706b; letter-spacing: 1px; margin: 0 0 8px 6px; }
.fm-crumbs { display: flex; gap: 4px; align-items: center; font-size: 13px; flex-wrap: wrap; }
.crumb { cursor: pointer; color: #edf2ef; }
.crumb:hover { text-decoration: underline; }
.fm-drop { flex: 1; display: flex; flex-direction: column; position: relative; border: 1px dashed transparent; border-radius: 10px; }
.fm-drop.over { border-color: rgba(240, 244, 241, 0.5); background: rgba(240, 244, 241, 0.04); }
.fm-drop-hint { position: absolute; inset: 0; display: flex; align-items: center; justify-content: center; color: #edf2ef; font-size: 15px; pointer-events: none; background: rgba(16, 19, 18, 0.55); }
</style>
