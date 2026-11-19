// 浏览器实时事件通道（docs/06 §3）：WS 只做增量提示，页面数据以 REST 为源。
export type HubEvent = {
  topic: string
  node_id?: string
  payload: any
}

type Handler = (ev: HubEvent) => void

let ws: WebSocket | null = null
let reconnectTimer: number | null = null
let pingTimer: number | null = null
const handlers = new Set<Handler>()

// 订阅过滤条件（页面进入时设置；单实例面板，全局一份订阅足够）
let subFilter: { node_ids: string[]; topics: string[] } = { node_ids: ['*'], topics: [] }

export function setSubscription(filter: { node_ids: string[]; topics: string[] }) {
  subFilter = filter
  if (ws && ws.readyState === WebSocket.OPEN) {
    ws.send(JSON.stringify({ sub: filter }))
  }
}

export function onEvent(h: Handler): () => void {
  handlers.add(h)
  return () => handlers.delete(h)
}

// 终端上行消息(docs/04 §8):session.input / session.resize。
// 返回 false 表示通道未连接(调用方提示)。
export function sendClient(msg: Record<string, unknown>): boolean {
  if (ws && ws.readyState === WebSocket.OPEN) {
    ws.send(JSON.stringify(msg))
    return true
  }
  return false
}

export function connectEvents() {
  if (ws) return
  open()
}

function open() {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  ws = new WebSocket(`${proto}://${location.host}/api/v1/events`)
  ws.onopen = () => {
    if (reconnectTimer) { clearTimeout(reconnectTimer); reconnectTimer = null }
    ws!.send(JSON.stringify({ sub: subFilter }))
    // 心跳保活:反代对无字节流动的 WS 有空闲超时(如 nginx proxy_read_timeout 60s),
    // 静默连接会被掐断导致终端输入/输出断流——25s 应用层 ping 维持字节流
    if (pingTimer) { clearInterval(pingTimer) }
    pingTimer = window.setInterval(() => {
      try { ws?.send(JSON.stringify({ type: 'ping' })) } catch { /* 断线由 onclose 处理 */ }
    }, 25000)
  }
  ws.onmessage = (msg) => {
    try {
      const ev = JSON.parse(msg.data) as HubEvent
      handlers.forEach((h) => h(ev))
    } catch { /* 忽略坏帧 */ }
  }
  ws.onclose = () => {
    if (pingTimer) { clearInterval(pingTimer); pingTimer = null }
    ws = null
    // 断线重连：10s 退避；页面以 REST 兜底，不追求秒级（docs/06 §3）
    if (!reconnectTimer) {
      reconnectTimer = window.setTimeout(() => { reconnectTimer = null; open() }, 10000)
    }
  }
}
