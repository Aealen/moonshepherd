<script setup lang="ts">
// 节点交互式终端(方案 B:Web 终端,docs/04 §8)。
// 真 PTY:vi/top/Ctrl-C 全可用;会话绑定 Agent 连接,节点离线/空闲 15 分钟即终结。
// 输入全程审计(面板侧按行落 audit_logs,docs/05 §3.3)。
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { NButton, NModal, NPopconfirm, useMessage } from 'naive-ui'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { api } from '../api'
import { onEvent, sendClient } from '../api/ws'

const props = defineProps<{ nodeID: string; nodeName: string; online: boolean; hasPty: boolean }>()
const message = useMessage()

const container = ref<HTMLElement | null>(null)
const sessionID = ref<string | null>(null)
const opening = ref(false)
const closed = ref(false)
const closeReason = ref('')
const showConfirm = ref(false)

let term: Terminal | null = null
let fit: FitAddon | null = null
let off: (() => void) | null = null
let resizeObserver: ResizeObserver | null = null
let lastCols = 0
let lastRows = 0

function b64encode(s: string): string {
  const bytes = new TextEncoder().encode(s)
  let bin = ''
  bytes.forEach((b) => { bin += String.fromCharCode(b) })
  return btoa(bin)
}

function b64decode(s: string): Uint8Array {
  const bin = atob(s)
  const arr = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) arr[i] = bin.charCodeAt(i)
  return arr
}

async function openSession() {
  if (!term || !container.value) return
  opening.value = true
  try {
    fit!.fit()
    const rows = term.rows || 24
    const cols = term.cols || 80
    const data = await api.post<{ session_id: string }>(`/nodes/${props.nodeID}/sessions`, { rows, cols })
    sessionID.value = data.session_id
    closed.value = false
    closeReason.value = ''
    term.focus()
    term.scrollToBottom()
  } catch (e: any) {
    message.error(e.message)
  } finally {
    opening.value = false
    showConfirm.value = false
  }
}

function ensureTerm() {
  if (term || !container.value) return
  term = new Terminal({
    cursorBlink: true,
    fontSize: 13,
    fontFamily: '"Cascadia Mono", Consolas, "Courier New", monospace',
    theme: {
      background: '#1a1a20',
      foreground: '#e8e8e8',
      cursor: '#7aa2f7',
      selectionBackground: '#33467c',
    },
    scrollback: 5000,
    convertEol: false,
  })
  fit = new FitAddon()
  term.loadAddon(fit)
  term.open(container.value)
  fit.fit()
  lastRows = term.rows
  lastCols = term.cols

  term.onData((data) => {
    if (!sessionID.value || closed.value) return
    sendClient({ type: 'session.input', session_id: sessionID.value, data_b64: b64encode(data) })
  })

  // 容器尺寸变化 → fit + 上报 resize(节流 200ms)
  let timer: number | null = null
  resizeObserver = new ResizeObserver(() => {
    if (timer) clearTimeout(timer)
    timer = window.setTimeout(() => {
      if (!term || !fit) return
      try { fit.fit() } catch { return }
      if (!sessionID.value || closed.value) return
      if (term.cols !== lastCols || term.rows !== lastRows) {
        lastCols = term.cols
        lastRows = term.rows
        sendClient({ type: 'session.resize', session_id: sessionID.value, rows: term.rows, cols: term.cols })
      }
    }, 200)
  })
  resizeObserver.observe(container.value)
}

function markClosed(reason: string) {
  if (closed.value) return
  closed.value = true
  closeReason.value = reason
  term?.write(`\r\n\x1b[90m── 会话已结束(${reason}) ──\x1b[0m\r\n`)
}

async function closeSession() {
  if (!sessionID.value) return
  try {
    await api.delete(`/nodes/${props.nodeID}/sessions/${sessionID.value}`)
  } catch { /* 面板/节点侧已关则忽略 */ }
  markClosed('已关闭')
  sessionID.value = null
}

onMounted(() => {
  ensureTerm()
  off = onEvent((ev) => {
    if (ev.node_id !== props.nodeID) return
    if (ev.topic === 'session.output' && ev.payload?.session_id === sessionID.value) {
      term?.write(b64decode(ev.payload.data_b64))
    } else if (ev.topic === 'session.close' && ev.payload?.session_id === sessionID.value) {
      markClosed(ev.payload?.reason || '已关闭')
      sessionID.value = null
    } else if (ev.topic === 'node.status' && ev.payload?.status === 'offline') {
      if (sessionID.value) {
        markClosed('节点离线')
        sessionID.value = null
      }
    }
  })
})

onBeforeUnmount(() => {
  if (sessionID.value && !closed.value) {
    sendClient({ type: 'session.close' }) // 尽力;面板侧由 REST 关闭兜底不可靠时 Agent 空闲超时回收
    api.delete(`/nodes/${props.nodeID}/sessions/${sessionID.value}`).catch(() => {})
  }
  off?.()
  resizeObserver?.disconnect()
  term?.dispose()
  term = null
})
</script>

<template>
  <div>
    <div v-if="!hasPty" class="muted" style="padding:24px; text-align:center">
      该节点 Agent 版本不支持终端(需 capabilities 含 pty 的版本),升级 Agent 后可用。
    </div>
    <template v-else>
      <div style="display:flex; gap:10px; align-items:center; margin-bottom:10px">
        <n-button size="small" type="primary" :disabled="!online || !!sessionID || opening"
          :loading="opening" @click="showConfirm = true">
          打开终端
        </n-button>
        <n-popconfirm v-if="sessionID" @positive-click="closeSession">
          <template #trigger>
            <n-button size="small" type="warning" secondary>关闭终端</n-button>
          </template>
          关闭将终止节点上的 shell 会话,确定?
        </n-popconfirm>
        <span v-if="sessionID" class="muted" style="font-size:12px">
          {{ nodeName }} · {{ sessionID }} · 输入全程审计
        </span>
        <span v-else-if="closed && closeReason" class="muted" style="font-size:12px">
          会话已结束({{ closeReason }}),可重新打开
        </span>
        <span v-else class="muted" style="font-size:12px">真 shell(vi/top/Ctrl-C 可用);空闲 15 分钟自动回收</span>
      </div>
      <div ref="container" class="term-box" :class="{ off: !sessionID }"></div>
    </template>

    <n-modal v-model:show="showConfirm" preset="dialog" type="warning" title="打开交互式终端"
      positive-text="打开" negative-text="取消" @positive-click="openSession">
      将在节点 <b>{{ nodeName }}</b> 上启动一个交互式 shell 会话,等同直接登录该服务器。
      你的全部输入将按行记录进审计日志(包含终端内交互式输入的敏感信息)。
    </n-modal>
  </div>
</template>

<style scoped>
.term-box {
  background: #1a1a20;
  border-radius: 8px;
  padding: 10px 12px;
  min-height: 420px;
  height: calc(100vh - 320px);
}
.term-box.off {
  opacity: 0.55;
}
</style>
