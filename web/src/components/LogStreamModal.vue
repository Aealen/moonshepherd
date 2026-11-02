<script setup lang="ts">
// 容器日志实时弹窗(docs/04 §9):tail -f 体验——打开即流式滚动,
// 不经指令中心;停止/清屏/自动滚动可控。数据面走 events WSS 转推。
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { NButton, NModal, useMessage } from 'naive-ui'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { api } from '../api'
import { onEvent, sendClient } from '../api/ws'

const props = defineProps<{ nodeID: string; container: string }>()
const emit = defineEmits<{ (e: 'update:show', v: boolean): void }>()
const message = useMessage()

const show = defineModel<boolean>('show', { default: false })
const streaming = ref(false)
const waiting = ref(true)

const container = ref<HTMLElement | null>(null)
let term: Terminal | null = null
let fit: FitAddon | null = null
let streamID: string | null = null
let off: (() => void) | null = null
let autoScroll = true

function b64decode(s: string): Uint8Array {
  const bin = atob(s)
  const arr = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) arr[i] = bin.charCodeAt(i)
  return arr
}

function ensureTerm() {
  if (term || !container.value) return
  term = new Terminal({
    fontSize: 12.5,
    fontFamily: '"Cascadia Mono", "JetBrains Mono", Consolas, monospace',
    theme: { background: '#0d0f13', foreground: '#c6d0ca', cursor: '#eef1f5', selectionBackground: '#3a3f4a' },
    scrollback: 10000,
    disableStdin: true, // 只读日志
  })
  fit = new FitAddon()
  term.loadAddon(fit)
  term.open(container.value)
  fit.fit()
  term.onScroll(() => {})
  // 用户向上滚离底部 → 关自动滚动;滚回底部 → 恢复
  term.attachCustomKeyEventHandler(() => false)
}

async function start() {
  ensureTerm()
  waiting.value = true
  streaming.value = true
  try {
    const data = await api.post<{ stream_id: string }>(`/nodes/${props.nodeID}/logstream`, {
      container: props.container, tail_lines: 200,
    })
    streamID = data.stream_id
    term?.writeln(`\x1b[90m── 正在跟随 ${props.container} 的日志 ──\x1b[0m`)
  } catch (e: any) {
    message.error(e.message)
    streaming.value = false
  }
}

function stopStream(silent = false) {
  if (streamID) {
    api.delete(`/nodes/${props.nodeID}/logstream/${streamID}`).catch(() => {})
    streamID = null
  }
  streaming.value = false
  if (!silent) {
    term?.writeln(`\x1b[90m── 日志跟随已停止 ──\x1b[0m`)
  }
}

function clearTerm() {
  term?.clear()
  term?.write('\x1b[2J\x1b[H')
}

function scrollToBottom() {
  autoScroll = true
  term?.scrollToBottom()
}

onMounted(() => {
  off = onEvent((ev) => {
    if (ev.node_id !== props.nodeID) return
    if (ev.topic === 'log.stream') {
      const p = ev.payload
      if (p?.stream_id && p.stream_id !== streamID) return
      if (p?.data_b64) {
        waiting.value = false
        term?.write(b64decode(p.data_b64))
        if (autoScroll) term?.scrollToBottom()
      }
      if (p?.end || p?.reason) {
        const reason = p.reason ?? 'end'
        term?.writeln(`\x1b[90m── 日志流结束(${reason === 'conn_closed' ? '节点离线' : reason === 'end' ? '跟随终止' : reason}) ──\x1b[0m`)
        streaming.value = false
        streamID = null
      }
    } else if (ev.topic === 'node.status' && ev.payload?.status === 'offline' && streamID) {
      term?.writeln(`\x1b[90m── 节点离线,日志跟随终止 ──\x1b[0m`)
      streaming.value = false
      streamID = null
    }
  })
  if (show.value) start()
})

// 弹窗每次打开都重新开流
function onShowChange(v: boolean) {
  emit('update:show', v)
  if (v) start()
  else stopStream(true)
}

onBeforeUnmount(() => {
  stopStream(true)
  off?.()
  term?.dispose()
  term = null
})

// expose sendClient 引用防未用告警(预留 resize 场景)
void sendClient
</script>

<template>
  <n-modal :show="show" @update:show="onShowChange" preset="card"
    :title="`日志 · ${container}`" style="width:860px">
    <div style="display:flex; gap:8px; align-items:center; margin-bottom:10px">
      <n-tag size="tiny" :bordered="false" :type="streaming ? 'success' : 'default'">
        <span v-if="streaming" class="live-dot"></span> {{ streaming ? '跟随中' : '已停止' }}
      </n-tag>
      <span v-if="streaming && waiting" class="muted" style="font-size:12px">等待日志…(容器无新输出时为静默)</span>
      <div style="flex:1"></div>
      <n-button size="tiny" quaternary @click="clearTerm">清屏</n-button>
      <n-button size="tiny" quaternary @click="scrollToBottom">回到底部</n-button>
      <n-button v-if="streaming" size="tiny" type="warning" secondary @click="stopStream()">停止跟随</n-button>
      <n-button v-else size="tiny" type="primary" @click="start">重新跟随</n-button>
    </div>
    <div ref="container" style="height:60vh; border-radius:8px; overflow:hidden; border:1px solid #23272e"></div>
    <div class="muted" style="font-size:11.5px; margin-top:8px">
      tail -f 语义:打开即回放最近 200 行,此后实时推送;向上滚动可翻历史,点「回到底部」恢复跟随。
    </div>
  </n-modal>
</template>

<style scoped>
.live-dot {
  display: inline-block;
  width: 7px; height: 7px; border-radius: 50%;
  background: #eef1f5;
  margin-right: 5px;
  box-shadow: 0 0 7px rgba(238, 241, 245, 0.7);
  animation: log-breathe 2.2s ease-in-out infinite;
}
@keyframes log-breathe {
  0%, 100% { opacity: 0.6; }
  50% { opacity: 1; }
}
</style>
