<script setup lang="ts">
// 节点交互式终端(方案 B:Web 终端,docs/04 §8)。
// 真 PTY:vi/top/Ctrl-C 全可用;会话绑定 Agent 连接,节点离线/空闲 15 分钟即终结。
// 输入全程审计(面板侧按行落 audit_logs,docs/05 §3.3)。
// 交互:会话头部(徽章+状态+时长) / 一键全屏(Esc 退出) / 常用命令速插条(只插入不执行)
//      Ctrl+Shift+C 复制选区 / Ctrl+Shift+V 粘贴 / 会话溯源水印 / 夜空单色 ANSI 色板
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
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
const fullscreen = ref(false)
const elapsed = ref(0) // 会话时长(秒)
const username = ref('')

let term: Terminal | null = null
let fit: FitAddon | null = null
let off: (() => void) | null = null
let resizeObserver: ResizeObserver | null = null
let lastCols = 0
let lastRows = 0
let tickTimer: ReturnType<typeof setInterval> | undefined

// 常用巡检命令:点击仅插入到提示符后,回车权留给用户(不代执行)
const SNIPPETS: { label: string; cmd: string }[] = [
  { label: '磁盘', cmd: 'df -h' },
  { label: '内存', cmd: 'free -m' },
  { label: '负载', cmd: 'uptime' },
  { label: '进程TOP', cmd: 'ps aux --sort=-%mem | head -15' },
  { label: '端口', cmd: 'ss -tlnp | head -20' },
  { label: 'Docker', cmd: 'docker ps' },
  { label: '日志尾部', cmd: 'journalctl -n 30 --no-pager' },
]

// 夜空单色 ANSI 色板:终端内彩色输出(ls/top/日志级别)与面板 UI 同一色系
const XTERM_THEME = {
  background: '#0d0f13',
  foreground: '#d6dae2',
  cursor: '#63d3a6',
  cursorAccent: '#0d0f13',
  selectionBackground: '#2b3440',
  black: '#3b4252', red: '#ef9d93', green: '#63d3a6', yellow: '#e3c47f',
  blue: '#7da6d9', magenta: '#b48ead', cyan: '#8fc7bb', white: '#d6dae2',
  brightBlack: '#676e7a', brightRed: '#f0a8a0', brightGreen: '#7cdcb4', brightYellow: '#ecd09a',
  brightBlue: '#93b8e2', brightMagenta: '#c4a0c0', brightCyan: '#a3d6cb', brightWhite: '#eef1f5',
}

const stateOf = computed<'idle' | 'opening' | 'live' | 'ended'>(() => {
  if (sessionID.value && !closed.value) return 'live'
  if (opening.value) return 'opening'
  if (closed.value) return 'ended'
  return 'idle'
})
const elapsedText = computed(() => {
  const m = Math.floor(elapsed.value / 60)
  const s = elapsed.value % 60
  return `${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`
})
const stateText = computed(() => ({
  idle: props.online ? '未连接' : '节点离线',
  opening: '正在建立会话…',
  live: `已连接 ${elapsedText.value}`,
  ended: `已结束(${closeReason.value}) · 可重新打开`,
}[stateOf.value]))

// 会话溯源水印:极淡平铺,防截屏外传可定位到人
const watermark = computed(() => {
  if (stateOf.value !== 'live') return 'none'
  const text = `${username.value || 'admin'} @ ${props.nodeName} · ${new Date().toLocaleDateString('zh-CN')}`
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="420" height="220"><text x="24" y="120" transform="rotate(-16 210 110)" font-size="13" fill="rgba(238,241,245,0.045)" font-family="Consolas,monospace">${text}</text></svg>`
  return `url("data:image/svg+xml;utf8,${encodeURIComponent(svg)}")`
})

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

function sendInput(data: string) {
  if (!sessionID.value || closed.value) return
  sendClient({ type: 'session.input', session_id: sessionID.value, data_b64: b64encode(data) })
}

// 早到帧缓冲:POST 建会话期间(sessionID 尚未知)收到的本节点会话帧先入队,
// 响应到达后按新 session_id 精确回放。Agent fork PTY 后立即写 shell prompt,
// 而 POST 响应还要等审计落库 + 公网 RTT,帧常先到;此前因 session_id 不匹配被丢,
// 表现为"已建立会话但迟迟等不到 shell 输出"。帧自带 session_id,回放时过滤即可,
// 天然排除同节点其他标签页会话的输出。
let buffering = false
const pendingFrames: { topic: string; payload?: any }[] = []
const PENDING_MAX = 256 // 仅 POST 窗口内短暂缓冲;超限丢最旧,保留最新屏内容

// applySessionEvent 处理属于当前会话的帧(实时与回放共用);返回是否已消费。
function applySessionEvent(ev: { topic: string; payload?: any }): boolean {
  if (ev.payload?.session_id !== sessionID.value) return false
  if (ev.topic === 'session.output') {
    term?.write(b64decode(ev.payload.data_b64))
    return true
  }
  if (ev.topic === 'session.close') {
    markClosed(ev.payload?.reason || '已关闭')
    sessionID.value = null
    return true
  }
  return false
}

async function openSession() {
  if (!term || !container.value) return
  opening.value = true
  buffering = true
  pendingFrames.length = 0
  try {
    fit!.fit()
    const rows = term.rows || 24
    const cols = term.cols || 80
    const data = await api.post<{ session_id: string }>(`/nodes/${props.nodeID}/sessions`, { rows, cols })
    sessionID.value = data.session_id
    closed.value = false
    closeReason.value = ''
    elapsed.value = 0
    if (tickTimer) clearInterval(tickTimer)
    tickTimer = setInterval(() => { elapsed.value++ }, 1000)
    // 回放早到帧:prompt 已在队列里则直接渲染,不再写"等待 shell 输出"占位提示
    buffering = false
    const early = pendingFrames.splice(0)
    let consumed = false
    for (const ev of early) {
      if (applySessionEvent(ev)) consumed = true
    }
    if (!consumed) {
      // 本地回显:会话建立瞬间给反馈,避免 shell 欢迎语到达前"像没连上"
      term.write('\r\n\x1b[90m── 会话已建立,等待 shell 输出… ──\x1b[0m\r\n')
    }
    term.focus()
    term.scrollToBottom()
  } catch (e: any) {
    buffering = false
    pendingFrames.length = 0
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
    theme: XTERM_THEME,
    scrollback: 5000,
    convertEol: false,
  })
  fit = new FitAddon()
  term.loadAddon(fit)
  term.open(container.value)
  fit.fit()
  lastRows = term.rows
  lastCols = term.cols

  term.onData((data) => sendInput(data))

  // Ctrl+Shift+C/V:复制选区/粘贴(不抢 Ctrl+C 的中断语义)
  term.attachCustomKeyEventHandler((e) => {
    if (e.type !== 'keydown') return true
    if (e.ctrlKey && e.shiftKey && (e.key === 'C' || e.key === 'c')) {
      const sel = term!.getSelection()
      if (sel) { navigator.clipboard.writeText(sel); message.success('已复制选区') }
      return false
    }
    if (e.ctrlKey && e.shiftKey && (e.key === 'V' || e.key === 'v')) {
      navigator.clipboard.readText().then((t) => { if (t) sendInput(t) })
      return false
    }
    return true
  })

  // 容器尺寸变化 → fit + 上报 resize(节流 200ms);全屏切换同样走这里自适应
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

function toggleFullscreen() {
  fullscreen.value = !fullscreen.value
  if (!fullscreen.value && sessionID.value) term?.focus()
}

function onWindowKey(e: KeyboardEvent) {
  if (fullscreen.value && e.key === 'Escape') fullscreen.value = false
}

function clearScreen() {
  term?.clear()
  term?.focus()
}

// 速插命令:写入 PTY 输入行但不带换行,执行权留给用户
function insertSnippet(cmd: string) {
  if (!sessionID.value || closed.value) return
  sendInput(cmd)
  term?.focus()
}

function markClosed(reason: string) {
  if (closed.value) return
  closed.value = true
  if (tickTimer) { clearInterval(tickTimer); tickTimer = undefined }
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

onMounted(async () => {
  ensureTerm()
  window.addEventListener('keydown', onWindowKey)
  try {
    const me = await api.get<{ username: string }>('/auth/me')
    username.value = me.username
  } catch { /* 水印署名缺省 admin */ }
  off = onEvent((ev) => {
    if (ev.node_id !== props.nodeID) return
    if (ev.topic === 'session.output' || ev.topic === 'session.close') {
      if (buffering) {
        // POST 未返回:不知道哪个会话是我的,先入队,openSession 成功后按 id 回放
        if (pendingFrames.length >= PENDING_MAX) pendingFrames.shift()
        pendingFrames.push(ev)
        return
      }
      applySessionEvent(ev)
      return
    }
    if (ev.topic === 'node.status' && ev.payload?.status === 'offline') {
      if (sessionID.value) {
        markClosed('节点离线')
        sessionID.value = null
      }
    }
  })
})

onBeforeUnmount(() => {
  // 权威关闭路径是 REST /sessions/{id}(下发 close 给 Agent+审计+广播);WS 上行无 session.close 通道
  if (sessionID.value && !closed.value) {
    api.delete(`/nodes/${props.nodeID}/sessions/${sessionID.value}`).catch(() => {})
  }
  if (tickTimer) clearInterval(tickTimer)
  window.removeEventListener('keydown', onWindowKey)
  off?.()
  resizeObserver?.disconnect()
  term?.dispose()
  term = null
})
</script>

<template>
  <div v-if="!hasPty" class="muted" style="padding:24px; text-align:center">
    该节点 Agent 版本不支持终端(需 capabilities 含 pty 的版本),升级 Agent 后可用。
  </div>
  <div v-else class="term-wrap" :class="{ fullscreen }">
    <!-- 会话头部:徽章 + 主机名 + 状态副标题 + 操作按钮组 -->
    <div class="term-head">
      <div class="term-badge mono">&gt;_</div>
      <div class="term-title-block">
        <div class="term-title">{{ nodeName }}</div>
        <div class="term-sub">
          <span class="term-dot" :class="stateOf" />
          <span :class="{ 'term-sub-strong': stateOf === 'live' }">{{ stateText }}</span>
          <span class="term-sub-dim">· 输入全程审计 · 空闲 15 分钟自动回收</span>
        </div>
      </div>
      <div class="term-actions">
        <n-button size="tiny" quaternary :disabled="stateOf !== 'live'" @click="clearScreen">清屏</n-button>
        <n-button size="tiny" quaternary @click="toggleFullscreen">{{ fullscreen ? '退出全屏 (Esc)' : '全屏' }}</n-button>
        <n-button v-if="stateOf !== 'live'" size="tiny" type="primary" :disabled="!online" :loading="opening"
          @click="showConfirm = true">打开终端</n-button>
        <n-popconfirm v-else @positive-click="closeSession">
          <template #trigger>
            <n-button size="tiny" type="warning" quaternary>断开</n-button>
          </template>
          关闭将终止节点上的 shell 会话,确定?
        </n-popconfirm>
      </div>
    </div>

    <!-- 常用命令速插条:仅插入不执行,回车权留给用户 -->
    <div v-if="stateOf === 'live'" class="term-snippets">
      <span class="term-snippets-label">速插</span>
      <button v-for="s in SNIPPETS" :key="s.label" class="term-snippet" :title="`${s.cmd}(插入但不执行)`"
        @click="insertSnippet(s.cmd)">{{ s.label }}</button>
    </div>

    <div class="term-stage">
      <div ref="container" class="term-box"></div>
      <!-- 空态占位:终端实例已就绪,开完会话即聚焦 -->
      <div v-if="stateOf === 'idle'" class="term-placeholder">
        <div class="term-placeholder-badge mono">&gt;_</div>
        <div style="font-weight:600; margin:10px 0 4px">交互式终端</div>
        <div class="muted" style="font-size:12.5px; line-height:1.9">
          <span style="color:#a4abb8">真 shell · vi / top / Ctrl-C 全可用</span><br>
          <span style="color:#676e7a">Ctrl+Shift+C 复制 · Ctrl+Shift+V 粘贴</span>
        </div>
        <n-button size="small" type="primary" :disabled="!online" :loading="opening" style="margin-top:14px"
          @click="showConfirm = true">打开终端</n-button>
      </div>
      <!-- 会话溯源水印 -->
      <div v-if="stateOf === 'live'" class="term-watermark" :style="{ backgroundImage: watermark }" />
    </div>
  </div>

  <n-modal v-model:show="showConfirm" preset="dialog" type="warning" title="打开交互式终端"
    positive-text="打开" negative-text="取消" @positive-click="openSession">
    将在节点 <b>{{ nodeName }}</b> 上启动一个交互式 shell 会话,等同直接登录该服务器。
    你的全部输入将按行记录进审计日志(包含终端内交互式输入的敏感信息)。
  </n-modal>
</template>

<style scoped>
.term-wrap {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.term-wrap.fullscreen {
  position: fixed;
  inset: 0;
  z-index: 1000;
  background: var(--bg-0, #0a0b0d);
  padding: 14px 18px 18px;
  gap: 8px;
}

/* 会话头部 */
.term-head { display: flex; align-items: center; gap: 10px; }
.term-badge {
  width: 34px; height: 34px;
  display: flex; align-items: center; justify-content: center;
  background: rgba(238, 241, 245, 0.06);
  border: 1px solid rgba(238, 241, 245, 0.14);
  border-radius: 8px;
  font-size: 14px; font-weight: 700;
  color: #eef1f5;
  flex: none;
}
.term-title-block { min-width: 0; }
.term-title { font-weight: 600; font-size: 14px; line-height: 1.3; }
.term-sub { display: flex; align-items: center; gap: 6px; font-size: 12px; color: var(--text-2, #a4abb8); margin-top: 2px; }
.term-sub-strong { color: #eef1f5; font-weight: 600; font-variant-numeric: tabular-nums; }
.term-sub-dim { color: #676e7a; }
.term-dot { width: 7px; height: 7px; border-radius: 50%; background: #676e7a; flex: none; }
.term-dot.live { background: #63d3a6; box-shadow: 0 0 8px rgba(99, 211, 166, 0.7); animation: term-breathe 2.4s ease-in-out infinite; }
.term-dot.opening { background: #e3c47f; animation: term-breathe 1.2s ease-in-out infinite; }
.term-dot.ended { background: #676e7a; }
@keyframes term-breathe {
  0%, 100% { box-shadow: 0 0 3px rgba(99, 211, 166, 0.4); }
  50% { box-shadow: 0 0 10px rgba(99, 211, 166, 0.9); }
}
.term-actions { margin-left: auto; display: flex; gap: 6px; flex: none; }

/* 速插命令:纯文字态降噪,hover 才浮出底色 */
.term-snippets { display: flex; align-items: center; gap: 4px; flex-wrap: wrap; }
.term-snippets-label { font-size: 11.5px; color: #676e7a; margin-right: 4px; }
.term-snippet {
  font-size: 11.5px;
  font-family: "JetBrains Mono", Consolas, monospace;
  color: var(--text-2, #a4abb8);
  background: transparent;
  border: none;
  border-radius: 6px;
  padding: 3px 8px;
  cursor: pointer;
  transition: background 0.15s, color 0.15s;
}
.term-snippet:hover { background: rgba(238, 241, 245, 0.1); color: #eef1f5; }

/* 终端舞台 */
.term-stage { position: relative; min-height: 380px; flex: 1; }
.term-wrap.fullscreen .term-stage { min-height: 0; }
.term-box {
  background: #0d0f13;
  border: 1px solid rgba(238, 241, 245, 0.1);
  border-radius: 8px;
  padding: 10px 12px;
  height: calc(100vh - 380px);
  min-height: 300px;
}
.term-wrap.fullscreen .term-box { height: 100%; border-color: rgba(238, 241, 245, 0.08); }
.term-box :deep(.xterm-viewport)::-webkit-scrollbar { width: 8px; }
.term-box :deep(.xterm-viewport)::-webkit-scrollbar-thumb { background: rgba(238, 241, 245, 0.16); border-radius: 4px; }
.term-box :deep(.xterm-viewport)::-webkit-scrollbar-track { background: transparent; }

/* 空态占位(终端实例已在底层就绪) */
.term-placeholder {
  position: absolute; inset: 0;
  display: flex; flex-direction: column; align-items: center; justify-content: center;
  text-align: center;
}
.term-placeholder-badge {
  width: 52px; height: 52px;
  display: flex; align-items: center; justify-content: center;
  background: rgba(238, 241, 245, 0.06);
  border: 1px solid rgba(238, 241, 245, 0.14);
  border-radius: 12px;
  font-size: 20px; font-weight: 700;
  color: #eef1f5;
}

/* 会话溯源水印 */
.term-watermark {
  position: absolute; inset: 0;
  pointer-events: none;
  background-repeat: repeat;
  border-radius: 8px;
}
</style>
