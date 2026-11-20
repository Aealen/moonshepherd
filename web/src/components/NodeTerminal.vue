<script setup lang="ts">
// 节点交互式终端(方案 B:Web 终端,docs/04 §8)。
// 真 PTY:vi/top/Ctrl-C 全可用;会话绑定 Agent 连接,节点离线/空闲 15 分钟即终结。
// 输入全程审计(面板侧按行落 audit_logs,docs/05 §3.3)。
// 交互:会话头部(徽章+状态+时长) / 一键全屏(Esc 退出) / 常用命令速插条(只插入不执行)
//      Ctrl+Shift+C 复制选区 / Ctrl+Shift+V 粘贴 / 会话溯源水印 / 夜空单色 ANSI 色板
// 多会话:同一节点可并存多个 shell(切换条切换);其他节点的活跃会话也在切换条里一键跳转。
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { NButton, NModal, NPopconfirm, useMessage } from 'naive-ui'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { api } from '../api'
import { onEvent, onWsResumed, sendClient } from '../api/ws'
import { useTerminalStore } from '../stores/terminals'

const props = defineProps<{ nodeID: string; nodeName: string; online: boolean; hasPty: boolean }>()
const message = useMessage()
const router = useRouter()
const termStore = useTerminalStore()

// 终端 tab 内的活跃会话切换条:本节点多会话就地切换,其他节点的会话跨页跳转
const otherSessions = computed(() => termStore.sessions.filter((s) => s.nodeID !== props.nodeID))
const showSessionTabs = computed(() => insts.value.length > 0 || otherSessions.value.length > 0)

function switchToSession(nodeID: string) {
  if (nodeID === props.nodeID) return
  termStore.requestTerminal(nodeID)
  router.push(`/nodes/${nodeID}`)
}

// 切换条标签上的单会话时长(hh:mm 内联展示)
function sessElapsed(inst: TermInst): string {
  const total = inst.elapsed
  const h = Math.floor(total / 3600)
  const m = Math.floor((total % 3600) / 60)
  const s = total % 60
  return h > 0 ? `${h}:${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}` : `${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`
}

// 关闭指定会话(切换条 × / 头部「断开」共用):REST 权威路径
async function closeInst(inst: TermInst) {
  if (!inst || inst.closed) return
  try {
    await api.delete(`/nodes/${props.nodeID}/sessions/${inst.sessionID}`)
  } catch { /* 面板/节点侧已关则忽略 */ }
  markClosed(inst, '已关闭')
}

const container = ref<HTMLElement | null>(null)
const showConfirm = ref(false)
const fullscreen = ref(false)
const username = ref('')
const opening = ref(false)

// 一个终端会话实例:独立 xterm/定时器/生命周期;insts 为本节点并存会话列表。
// localId 为创建期稳定 key(sessionID 是 POST 后才回填的,不能做 key,否则
// key 变化触发 DOM 重建会毁掉已挂载的 xterm)。
// histBuf/history:本地命令记录(与面板审计同语义——遇回车切行,Ctrl-C 记 ^C)。
type TermInst = {
  localId: number
  sessionID: string
  term: Terminal | null
  fit: FitAddon | null
  closed: boolean
  closeReason: string
  openedAt: number
  elapsed: number
  timer: ReturnType<typeof setInterval> | undefined
  histBuf: string
  histEsc: number // 转义序列吞除状态:0 无 / 1 收到 ESC / 2 ESC[ CSI 中间态(焦点报告等序列不进历史)
  history: { t: number; cmd: string }[]
}
let instSeq = 0
const insts = ref<TermInst[]>([])
const activeIdx = ref(-1)
const closed = ref(false) // 全部会话结束后 true,控制占位/水印等
const closeReason = ref('')
const histOpen = ref(false) // 右侧命令记录面板,默认收起

const activeInst = computed(() => insts.value[activeIdx.value] ?? null)

let resizeObserver: ResizeObserver | null = null
let lastCols = 0
let lastRows = 0

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

const elapsedText = computed(() => {
  const s = activeInst.value
  if (!s) return ''
  const m = Math.floor(s.elapsed / 60)
  return `${String(m).padStart(2, '0')}:${String(s.elapsed % 60).padStart(2, '0')}`
})
const stateText = computed(() => {
  if (activeInst.value) return `已连接 ${elapsedText.value}`
  if (opening.value) return '正在建立会话…'
  if (closed.value) return `已结束(${closeReason.value}) · 可重新打开`
  return props.online ? '未连接' : '节点离线'
})

// 会话溯源水印:极淡平铺,防截屏外传可定位到人
const watermark = computed(() => {
  if (insts.value.length === 0) return 'none'
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
  const inst = activeInst.value
  if (!inst || inst.closed) return
  feedHistory(inst, data)
  sendClient({ type: 'session.input', session_id: inst.sessionID, data_b64: b64encode(data) })
}

// 本地命令记录:与面板审计同语义(docs/05 §3.3)——回车切行、Ctrl-C 记 ^C、
// 退格回退缓冲。仅存内存,随会话终结消失(持久审计在面板侧)。
function feedHistory(inst: TermInst, data: string) {
  for (const ch of data) {
    const code = ch.codePointAt(0)!
    if (inst.histEsc === 1) {
      if (code !== 0x5b) { inst.histEsc = 0; continue } // ESC+单字符序列:丢弃
      inst.histEsc = 2
      continue
    }
    if (inst.histEsc === 2) {
      if (code >= 0x40 && code <= 0x7e) inst.histEsc = 0 // CSI 最终字节
      continue
    }
    if (code === 27) { inst.histEsc = 1; continue }
    if (code === 13 || code === 10) {
      const line = inst.histBuf.trim()
      inst.histBuf = ''
      if (line) inst.history.unshift({ t: Date.now(), cmd: line })
    } else if (code === 3) {
      inst.histBuf = ''
      inst.history.unshift({ t: Date.now(), cmd: '^C' })
    } else if (code === 127 || code === 8) {
      inst.histBuf = inst.histBuf.slice(0, -1)
    } else if (code >= 32) {
      inst.histBuf += ch
    }
  }
}

function fmtHistTime(t: number): string {
  const d = new Date(t)
  return [d.getHours(), d.getMinutes(), d.getSeconds()].map((n) => String(n).padStart(2, '0')).join(':')
}

// 早到帧缓冲:POST 建会话期间(sessionID 尚未知)收到的本节点会话帧先入队,
// 响应到达后按新 session_id 精确回放。Agent fork PTY 后立即写 shell prompt,
// 而 POST 响应还要等审计落库 + 公网 RTT,帧常先到;此前因 session_id 不匹配被丢,
// 表现为"已建立会话但迟迟等不到 shell 输出"。帧自带 session_id,回放时过滤即可。
let buffering = false
const pendingFrames: { topic: string; payload?: any }[] = []
const PENDING_MAX = 256 // 仅 POST 窗口内短暂缓冲;超限丢最旧,保留最新屏内容

// applySessionEvent 将帧写入对应会话实例(实时与回放共用);返回是否已消费。
function applySessionEvent(ev: { topic: string; payload?: any }): boolean {
  const inst = insts.value.find((i) => i.sessionID === ev.payload?.session_id)
  if (!inst) return false
  if (ev.topic === 'session.output') {
    inst.term?.write(b64decode(ev.payload.data_b64))
    return true
  }
  if (ev.topic === 'session.close') {
    markClosed(inst, ev.payload?.reason || '已关闭')
    return true
  }
  return false
}

async function openSession() {
  if (opening.value || !props.online) return
  opening.value = true
  buffering = true
  pendingFrames.length = 0
  let inst: TermInst | null = null
  try {
    // 先铺 DOM(xterm 需要可见容器才有尺寸),再建实例
    inst = { localId: ++instSeq, sessionID: '', term: null, fit: null, closed: false, closeReason: '', openedAt: Date.now(), elapsed: 0, timer: undefined, histBuf: '', histEsc: 0, history: [] }
    insts.value.push(inst)
    activeIdx.value = insts.value.length - 1
    await nextTick()

    const mountEl = container.value?.querySelectorAll('.term-mount')[insts.value.length - 1] as HTMLElement | undefined
    if (!mountEl) throw new Error('终端容器未就绪')
    const term = new Terminal({
      cursorBlink: true,
      fontSize: 13,
      fontFamily: '"Cascadia Mono", Consolas, "Courier New", monospace',
      theme: XTERM_THEME,
      scrollback: 5000,
      convertEol: false,
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(mountEl)
    try { fit.fit() } catch { /* 首帧尺寸 0 时忽略 */ }
    const cur = inst as TermInst // await 之后 TS 丢失非空窄化,显式断言
    cur.term = term
    cur.fit = fit

    const rows = term.rows || 24
    const cols = term.cols || 80
    const data = await api.post<{ session_id: string }>(`/nodes/${props.nodeID}/sessions`, { rows, cols })
    cur.sessionID = data.session_id
    termStore.open(props.nodeID, props.nodeName, data.session_id)
    closed.value = false
    closeReason.value = ''
    cur.timer = setInterval(() => { cur.elapsed++ }, 1000)
    // 键盘输入在拿到 sessionID 后生效;本地命令记录同路收集(速插/历史点击走 sendInput)
    term.onData((d) => {
      if (!cur.sessionID || cur.closed) return
      feedHistory(cur, d)
      sendClient({ type: 'session.input', session_id: cur.sessionID, data_b64: b64encode(d) })
    })
    // Ctrl+Shift+C/V:复制选区/粘贴(不抢 Ctrl+C 的中断语义)
    term.attachCustomKeyEventHandler((e) => {
      if (e.type !== 'keydown') return true
      if (e.ctrlKey && e.shiftKey && (e.key === 'C' || e.key === 'c')) {
        const sel = term.getSelection()
        if (sel) { navigator.clipboard.writeText(sel); message.success('已复制选区') }
        return false
      }
      if (e.ctrlKey && e.shiftKey && (e.key === 'V' || e.key === 'v')) {
        navigator.clipboard.readText().then((t) => { if (t) sendClient({ type: 'session.input', session_id: cur.sessionID, data_b64: b64encode(t) }) })
        return false
      }
      return true
    })
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
    if (inst) {
      if (inst.sessionID) termStore.close(inst.sessionID)
      inst.term?.dispose()
      insts.value = insts.value.filter((i) => i !== inst)
      activeIdx.value = Math.min(activeIdx.value, insts.value.length - 1)
    }
    message.error(e.message)
  } finally {
    opening.value = false
    showConfirm.value = false
  }
}

// Ctrl+Shift+C/V 等键盘细节在各实例创建时绑定
let off: (() => void) | null = null
let offResumed: (() => void) | null = null

// 容器尺寸变化 → 激活实例 fit + 上报 resize(节流 200ms);全屏切换同样走这里自适应
let resizeTimer: number | null = null
function setupResizeObserver() {
  resizeObserver = new ResizeObserver(() => {
    if (resizeTimer) clearTimeout(resizeTimer)
    resizeTimer = window.setTimeout(() => {
      const inst = activeInst.value
      if (!inst?.fit || !inst.term) return
      try { inst.fit.fit() } catch { return }
      if (inst.closed || !inst.sessionID) return
      if (inst.term.cols !== lastCols || inst.term.rows !== lastRows) {
        lastCols = inst.term.cols
        lastRows = inst.term.rows
        sendClient({ type: 'session.resize', session_id: inst.sessionID, rows: inst.term.rows, cols: inst.term.cols })
      }
    }, 200)
  })
  if (container.value) resizeObserver.observe(container.value)
}

watch(activeIdx, () => {
  // 切换标签:让新激活实例重算尺寸并聚焦
  nextTick(() => {
    const inst = activeInst.value
    try { inst?.fit?.fit() } catch { /* 隐藏容器无尺寸 */ }
    inst?.term?.focus()
  })
})

function toggleFullscreen() {
  fullscreen.value = !fullscreen.value
  if (!fullscreen.value && activeInst.value) activeInst.value.term?.focus()
}

function onWindowKey(e: KeyboardEvent) {
  if (fullscreen.value && e.key === 'Escape') fullscreen.value = false
}

function clearScreen() {
  activeInst.value?.term?.clear()
  activeInst.value?.term?.focus()
}

// 速插命令:写入 PTY 输入行但不带换行,执行权留给用户
function insertSnippet(cmd: string) {
  const inst = activeInst.value
  if (!inst || inst.closed) return
  sendInput(cmd)
  inst.term?.focus()
}

// markClosed 单会话实例终结(可能来自 Agent 回执/节点离线/本地断开回执)
function markClosed(inst: TermInst, reason: string) {
  if (inst.closed) return
  inst.closed = true
  if (inst.timer) { clearInterval(inst.timer); inst.timer = undefined }
  inst.closeReason = reason
  termStore.close(inst.sessionID)
  inst.term?.write(`\r\n\x1b[90m── 会话已结束(${reason}) ──\x1b[0m\r\n`)
  // 关闭的是激活会话:优先聚焦剩余会话;全部结束则进入"已结束"占位
  const idx = insts.value.indexOf(inst)
  setTimeout(() => {
    insts.value = insts.value.filter((i) => i !== inst)
    inst.term?.dispose()
    if (insts.value.length === 0) {
      closed.value = true
      closeReason.value = reason
      activeIdx.value = -1
    } else {
      activeIdx.value = Math.min(Math.max(activeIdx.value, 0), insts.value.length - 1)
    }
  }, 120) // 略等结束行渲染完再摘 DOM
  void idx
}

async function closeActive() {
  await closeInst(activeInst.value)
}

// WS 重连成功:面板/网络曾中断。面板重启会清空内存会话表,浏览器保活的
// 会话成了"僵尸"(UI 显示已连接,输入被静默丢弃)——对照面板侧活跃列表,
// 不在本地的就地标记失效。
async function verifySessionsAlive() {
  const alive = insts.value.filter((i) => !i.closed && i.sessionID)
  if (alive.length === 0) return
  try {
    const data = await api.get<{ data: string[] }>(`/nodes/${props.nodeID}/sessions`)
    const set = new Set(data.data ?? [])
    for (const inst of alive) {
      if (!set.has(inst.sessionID)) markClosed(inst, '面板重启,会话已失效')
    }
  } catch { /* 面板不可达:等下次重连再校验 */ }
}

onMounted(async () => {
  setupResizeObserver()
  window.addEventListener('keydown', onWindowKey)
  try {
    const me = await api.get<{ username: string }>('/auth/me')
    username.value = me.username
  } catch { /* 水印署名缺省 admin */ }
  offResumed = onWsResumed(verifySessionsAlive)
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
      // 节点离线:全部会话终结
      for (const inst of [...insts.value]) {
        if (!inst.closed) markClosed(inst, '节点离线')
      }
    }
  })
})

onBeforeUnmount(() => {
  // 权威关闭路径是 REST /sessions/{id}(下发 close 给 Agent+审计+广播);WS 上行无 session.close 通道。
  // KeepAlive 保活下页面切换不触发本钩子,会话跨页存活;仅真正销毁(登出/关页)时走到这里。
  for (const inst of insts.value) {
    if (inst.sessionID && !inst.closed) {
      api.delete(`/nodes/${props.nodeID}/sessions/${inst.sessionID}`).catch(() => {})
      termStore.close(inst.sessionID)
    }
    if (inst.timer) clearInterval(inst.timer)
    inst.term?.dispose()
  }
  window.removeEventListener('keydown', onWindowKey)
  off?.()
  offResumed?.()
  resizeObserver?.disconnect()
})
</script>

<template>
  <div v-if="!hasPty" class="muted" style="padding:24px; text-align:center">
    该节点 Agent 版本不支持终端(需 capabilities 含 pty 的版本),升级 Agent 后可用。
  </div>
  <div v-else class="term-wrap" :class="{ fullscreen }">
    <!-- 活跃会话切换条:本节点多会话就地切换(#1/#2…),其他节点的会话跨页跳转 -->
    <div v-if="showSessionTabs" class="sess-tabs">
      <span class="sess-tabs-label">会话</span>
      <button v-for="(inst, i) in insts" :key="inst.localId" class="sess-tab"
        :class="{ current: i === activeIdx, dead: inst.closed }" :title="`本节点会话 #${i + 1}${inst.closed ? ' · 已结束' : ''}`"
        @click="activeIdx = i">
        <span class="sess-tab-dot"></span>
        <span class="sess-tab-name">#{{ i + 1 }}</span>
        <span v-if="!inst.closed" class="sess-tab-time mono">{{ sessElapsed(inst) }}</span>
        <span v-else class="sess-tab-dead">已结束</span>
        <span v-if="!inst.closed" class="sess-tab-x" title="关闭此会话"
          @click.stop="closeInst(inst)">×</span>
      </button>
      <button v-for="s in otherSessions" :key="s.sessionID" class="sess-tab sess-tab-remote"
        :title="`切换到 ${s.nodeName} 的活跃会话`" @click="switchToSession(s.nodeID)">
        <span class="sess-tab-dot dim"></span>
        <span class="sess-tab-name">{{ s.nodeName }}</span>
        <span class="sess-tab-go">→</span>
      </button>
      <button class="sess-tab sess-tab-new" :disabled="opening || !props.online" title="在该节点再开一个 shell 会话"
        @click="showConfirm = true">+ 新建</button>
    </div>

    <!-- 会话头部:徽章 + 主机名 + 状态副标题 + 操作按钮组 -->
    <div class="term-head">
      <div class="term-badge mono">&gt;_</div>
      <div class="term-title-block">
        <div class="term-title">
          {{ nodeName }}<span v-if="insts.length > 1" class="term-title-sub mono">#{{ activeIdx + 1 }}</span>
        </div>
        <div class="term-sub">
          <span class="term-dot" :class="insts.length ? 'live' : 'idle'" />
          <span :class="{ 'term-sub-strong': insts.length > 0 }">{{ stateText }}</span>
          <span class="term-sub-dim">· 输入全程审计 · 空闲 15 分钟自动回收</span>
        </div>
      </div>
      <div class="term-actions">
        <n-button v-if="insts.length" size="tiny" quaternary :type="histOpen ? 'primary' : 'default'"
          @click="histOpen = !histOpen">记录</n-button>
        <n-button v-if="insts.length" size="tiny" quaternary @click="clearScreen">清屏</n-button>
        <n-button size="tiny" quaternary @click="toggleFullscreen">{{ fullscreen ? '退出全屏 (Esc)' : '全屏' }}</n-button>
        <n-button v-if="insts.length" size="tiny" quaternary :disabled="opening || !online" @click="showConfirm = true">
          新建会话
        </n-button>
        <n-popconfirm v-if="insts.length" @positive-click="closeActive">
          <template #trigger>
            <n-button size="tiny" type="warning" quaternary>断开</n-button>
          </template>
          关闭当前激活的 shell 会话,确定?
        </n-popconfirm>
        <n-button v-if="!insts.length" size="tiny" type="primary" :disabled="!online" :loading="opening"
          @click="showConfirm = true">打开终端</n-button>
      </div>
    </div>

    <!-- 常用命令速插条:仅插入不执行,回车权留给用户 -->
    <div v-if="insts.length" class="term-snippets">
      <span class="term-snippets-label">速插</span>
      <button v-for="s in SNIPPETS" :key="s.label" class="term-snippet" :title="`${s.cmd}(插入但不执行)`"
        @click="insertSnippet(s.cmd)">{{ s.label }}</button>
    </div>

    <div class="term-stage">
      <div class="term-main">
        <div ref="container" class="term-boxes">
          <div v-for="(inst, i) in insts" :key="inst.localId" v-show="i === activeIdx" class="term-box">
            <div class="term-mount"></div>
          </div>
        </div>
        <!-- 空态占位:开完会话即聚焦 -->
        <div v-if="!insts.length" class="term-placeholder">
          <div class="term-placeholder-badge mono">&gt;_</div>
          <div style="font-weight:600; margin:10px 0 4px">交互式终端</div>
          <div class="muted" style="font-size:12.5px; line-height:1.9">
            <span style="color:#a4abb8">真 shell · vi / top / Ctrl-C 全可用 · 支持多会话</span><br>
            <span style="color:#676e7a">Ctrl+Shift+C 复制 · Ctrl+Shift+V 粘贴</span>
          </div>
          <n-button size="small" type="primary" :disabled="!online" :loading="opening" style="margin-top:14px"
            @click="showConfirm = true">打开终端</n-button>
        </div>
      </div>
      <!-- 命令记录面板:右侧滑出,默认收起;点击条目插入到提示符(不执行) -->
      <div v-if="histOpen && activeInst" class="hist-panel">
        <div class="hist-head">
          <span class="hist-title mono">命令记录 · #{{ activeIdx + 1 }}</span>
          <span class="hist-close" title="收起" @click="histOpen = false">×</span>
        </div>
        <div v-if="activeInst.history.length" class="hist-list">
          <div v-for="(h, i) in activeInst.history" :key="activeInst.history.length - i"
            class="hist-item" :title="`${h.cmd}\n点击插入到提示符(不执行)`" @click="insertSnippet(h.cmd)">
            <span class="hist-time mono">{{ fmtHistTime(h.t) }}</span>
            <span class="hist-cmd mono">{{ h.cmd }}</span>
          </div>
        </div>
        <div v-else class="hist-empty muted">暂无命令,回车执行的命令会记录在此</div>
      </div>
      <!-- 会话溯源水印 -->
      <div v-if="insts.length" class="term-watermark" :style="{ backgroundImage: watermark }" />
    </div>
  </div>

  <n-modal v-model:show="showConfirm" preset="dialog" type="warning" title="打开交互式终端"
    positive-text="打开" negative-text="取消" @positive-click="openSession">
    将在节点 <b>{{ nodeName }}</b> 上再启动一个交互式 shell 会话(已有会话不受影响)。
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

/* 活跃会话切换条:面板化展示,与终端头同级视觉重量 */
.sess-tabs {
  display: flex; align-items: center; gap: 6px; flex-wrap: wrap;
  padding: 7px 10px;
  background: rgba(238, 241, 245, 0.035);
  border: 1px solid rgba(238, 241, 245, 0.08);
  border-radius: 10px;
}
.sess-tabs-label {
  font-size: 11px; color: #676e7a; letter-spacing: 1px;
  margin-right: 4px; flex: none;
}
.sess-tab {
  display: inline-flex; align-items: center; gap: 8px;
  height: 32px; padding: 0 12px;
  font-size: 12.5px; font-family: "JetBrains Mono", Consolas, monospace;
  color: var(--text-2, #a4abb8);
  background: rgba(238, 241, 245, 0.05);
  border: 1px solid rgba(238, 241, 245, 0.1);
  border-radius: 8px;
  cursor: pointer;
  transition: background 0.15s, color 0.15s, border-color 0.15s;
}
.sess-tab:hover { background: rgba(238, 241, 245, 0.1); color: #eef1f5; }
.sess-tab.current {
  background: rgba(99, 211, 166, 0.13);
  border-color: rgba(99, 211, 166, 0.45);
  color: #63d3a6; font-weight: 600;
  box-shadow: inset 0 0 0 1px rgba(99, 211, 166, 0.12), 0 0 10px rgba(99, 211, 166, 0.12);
}
.sess-tab.dead { opacity: 0.55; }
.sess-tab-dot {
  width: 7px; height: 7px; border-radius: 50%; flex: none;
  background: #63d3a6; box-shadow: 0 0 6px rgba(99, 211, 166, 0.7);
  animation: sess-breathe 2.4s ease-in-out infinite;
}
@keyframes sess-breathe {
  0%, 100% { box-shadow: 0 0 3px rgba(99, 211, 166, 0.4); }
  50% { box-shadow: 0 0 9px rgba(99, 211, 166, 0.9); }
}
.sess-tab-dot.dim { background: #a4abb8; box-shadow: none; animation: none; }
.sess-tab-name { font-weight: 600; }
.sess-tab-time { font-size: 11px; color: #676e7a; font-variant-numeric: tabular-nums; }
.sess-tab.current .sess-tab-time { color: rgba(99, 211, 166, 0.75); }
.sess-tab-dead { font-size: 11px; color: #676e7a; }
.sess-tab-x {
  display: inline-flex; align-items: center; justify-content: center;
  width: 16px; height: 16px; border-radius: 4px;
  margin-right: -4px;
  font-size: 13px; line-height: 1; color: #676e7a;
  opacity: 0; transition: opacity 0.12s, background 0.12s, color 0.12s;
}
.sess-tab:hover .sess-tab-x { opacity: 1; }
.sess-tab-x:hover { background: rgba(239, 157, 147, 0.2); color: #ef9d93; }
.sess-tab-remote .sess-tab-name { font-weight: 500; }
.sess-tab-go { font-size: 11px; color: #676e7a; }
.sess-tab-new { border-style: dashed; color: #63d3a6; }
.sess-tab-new:disabled { opacity: 0.45; cursor: not-allowed; }
.sess-tab-new:hover { color: #7ce0b8; border-color: rgba(99, 211, 166, 0.4); }

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
.term-title-sub { margin-left: 8px; color: #676e7a; font-weight: 500; font-size: 12px; }
.term-sub { display: flex; align-items: center; gap: 6px; font-size: 12px; color: var(--text-2, #a4abb8); margin-top: 2px; }
.term-sub-strong { color: #eef1f5; font-weight: 600; font-variant-numeric: tabular-nums; }
.term-sub-dim { color: #676e7a; }
.term-dot { width: 7px; height: 7px; border-radius: 50%; background: #676e7a; flex: none; }
.term-dot.live { background: #63d3a6; box-shadow: 0 0 8px rgba(99, 211, 166, 0.7); animation: term-breathe 2.4s ease-in-out infinite; }
.term-dot.idle { background: #676e7a; }
@keyframes term-breathe {
  0%, 100% { box-shadow: 0 0 3px rgba(99, 211, 166, 0.4); }
  50% { box-shadow: 0 0 10px rgba(99, 211, 166, 0.9); }
}
.term-actions { margin-left: auto; display: flex; gap: 6px; flex: none; }

/* 常用命令速插:纯文字态降噪,hover 才浮出底色 */
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

/* 终端舞台:命令记录面板在右,主区在左;面板展开时主区收窄 */
.term-stage { position: relative; min-height: 380px; flex: 1; display: flex; gap: 8px; align-items: stretch; }
.term-wrap.fullscreen .term-stage { min-height: 0; }
.term-main { flex: 1; min-width: 0; position: relative; display: flex; flex-direction: column; }
.term-boxes { height: 100%; }
.term-boxes > .term-box {
  background: #0d0f13;
  border: 1px solid rgba(238, 241, 245, 0.1);
  border-radius: 8px;
  padding: 10px 12px;
  height: calc(100vh - 380px);
  min-height: 300px;
}
.term-wrap.fullscreen .term-boxes > .term-box { height: 100%; border-color: rgba(238, 241, 245, 0.08); }

/* 命令记录面板 */
.hist-panel {
  width: 280px; flex: none;
  display: flex; flex-direction: column;
  background: #101216;
  border: 1px solid rgba(238, 241, 245, 0.1);
  border-radius: 8px;
  overflow: hidden;
}
.hist-head {
  display: flex; align-items: center; justify-content: space-between;
  padding: 8px 12px;
  border-bottom: 1px solid rgba(238, 241, 245, 0.08);
}
.hist-title { font-size: 12px; font-weight: 600; color: #eef1f5; }
.hist-close {
  cursor: pointer; color: #676e7a; font-size: 15px; line-height: 1;
  padding: 0 3px; border-radius: 4px;
  transition: color 0.12s, background 0.12s;
}
.hist-close:hover { color: #eef1f5; background: rgba(238, 241, 245, 0.1); }
.hist-list { flex: 1; overflow-y: auto; padding: 6px; }
.hist-item {
  display: flex; align-items: baseline; gap: 8px;
  padding: 6px 8px; border-radius: 6px;
  cursor: pointer;
  transition: background 0.12s;
}
.hist-item:hover { background: rgba(99, 211, 166, 0.08); }
.hist-time { font-size: 10.5px; color: #676e7a; flex: none; font-variant-numeric: tabular-nums; }
.hist-cmd {
  font-size: 12px; color: #d6dae2;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.hist-empty {
  flex: 1; display: flex; align-items: center; justify-content: center;
  padding: 16px; font-size: 12px; text-align: center; line-height: 1.8;
}
.hist-list::-webkit-scrollbar { width: 8px; }
.hist-list::-webkit-scrollbar-thumb { background: rgba(238, 241, 245, 0.16); border-radius: 4px; }
.hist-list::-webkit-scrollbar-track { background: transparent; }

.term-boxes :deep(.xterm-viewport)::-webkit-scrollbar { width: 8px; }
.term-boxes :deep(.xterm-viewport)::-webkit-scrollbar-thumb { background: rgba(238, 241, 245, 0.16); border-radius: 4px; }
.term-boxes :deep(.xterm-viewport)::-webkit-scrollbar-track { background: transparent; }

/* 空态占位 */
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
