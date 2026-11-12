<script setup lang="ts">
// 牧群网络吞吐卡:手写 SVG 折线(下行白/上行绿),数据源 /metrics/net-series。
// 心跳到达(node.snapshot)节流刷新 + 60s 兜底轮询;悬停显示竖线与读数气泡。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { NCard } from 'naive-ui'
import { api } from '../api'
import { onEvent } from '../api/ws'

type NetPoint = { ts: number; rx: number; tx: number }

const WINDOW_MIN = 60
const points = ref<NetPoint[]>([])
const hover = ref<{ x: number; p: NetPoint } | null>(null)
let timer: ReturnType<typeof setInterval> | undefined
let throttleAt = 0
let off: (() => void) | null = null

async function load() {
  try {
    const data = await api.get<{ points: NetPoint[] }>(`/metrics/net-series?minutes=${WINDOW_MIN}`)
    points.value = data.points ?? []
    hover.value = null // 旧 points 引用已失效,清掉悬停读数防显示陈旧桶
  } catch { /* 首页非关键路径,静默 */ }
}

// Y 轴量程:0 基线 + 1/2/5×10^n 上取整,避免 max=3.2 时网格出现 3.2 这种脏刻度
const yMax = computed(() => {
  const raw = Math.max(1, ...points.value.map((p) => Math.max(p.rx, p.tx)))
  const pow = Math.pow(10, Math.floor(Math.log10(raw)))
  const unit = raw / pow
  return (unit <= 1 ? 1 : unit <= 2 ? 2 : unit <= 5 ? 5 : 10) * pow
})

// —— 图形几何(viewBox 等比缩放,文字不变形) ——
const W = 960
const H = 220
const PAD = { l: 58, r: 14, t: 14, b: 26 }
const plotW = W - PAD.l - PAD.r
const plotH = H - PAD.t - PAD.b
const windowS = WINDOW_MIN * 60
const now = () => Math.floor(Date.now() / 1000)
const xOf = (ts: number) => PAD.l + Math.max(0, Math.min(1, (ts - (now() - windowS)) / windowS)) * plotW
const yOf = (v: number) => PAD.t + (1 - v / yMax.value) * plotH

const pathOf = (key: 'rx' | 'tx') =>
  points.value.length < 2 ? '' : points.value.map((p, i) => `${i ? 'L' : 'M'}${xOf(p.ts).toFixed(1)} ${yOf(p[key]).toFixed(1)}`).join(' ')
const pathRx = computed(() => pathOf('rx'))
const pathTx = computed(() => pathOf('tx'))
// 下行线下淡面积:与折线同形,闭合到零基线
const areaRx = computed(() => {
  if (!pathRx.value) return ''
  const first = points.value[0]
  const last = points.value[points.value.length - 1]
  const y0 = yOf(0).toFixed(1)
  return `${pathRx.value} L${xOf(last.ts).toFixed(1)} ${y0} L${xOf(first.ts).toFixed(1)} ${y0} Z`
})

const yTicks = computed(() => [0, 0.25, 0.5, 0.75, 1].map((f) => ({ y: yOf(yMax.value * f), label: fmtRate(yMax.value * f) })))
const xTicks = computed(() => [0, 0.5, 1].map((f) => ({ x: PAD.l + f * plotW, label: fmtClock(now() - windowS + f * windowS) })))
const hasData = computed(() => points.value.length >= 2)

function fmtRate(kbps: number): string {
  return kbps >= 1024 ? `${(kbps / 1024).toFixed(1)} MB/s` : `${kbps >= 100 ? Math.round(kbps) : kbps.toFixed(1)} KB/s`
}
function fmtClock(ts: number): string {
  const d = new Date(ts * 1000)
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
}
function fmtClockS(ts: number): string {
  const d = new Date(ts * 1000)
  return `${fmtClock(ts)}:${String(d.getSeconds()).padStart(2, '0')}`
}

function onMove(e: MouseEvent) {
  if (!hasData.value) return
  const svg = e.currentTarget as SVGSVGElement
  const vx = ((e.clientX - svg.getBoundingClientRect().left) / svg.getBoundingClientRect().width) * W
  if (vx < PAD.l || vx > PAD.l + plotW) { hover.value = null; return }
  // 最近的桶:窗口时间反解 + 邻域线性找
  const ts = now() - windowS + ((vx - PAD.l) / plotW) * windowS
  let best = points.value[0]
  for (const p of points.value) if (Math.abs(p.ts - ts) < Math.abs(best.ts - ts)) best = p
  hover.value = { x: xOf(best.ts), p: best }
}

onMounted(() => {
  load()
  off = onEvent((ev) => {
    if (ev.topic !== 'node.snapshot') return
    const t = Date.now()
    if (t - throttleAt < 15000) return
    throttleAt = t
    load()
  })
  timer = setInterval(load, 60000)
})
onUnmounted(() => { off?.(); if (timer) clearInterval(timer) })
</script>

<template>
  <n-card size="small" class="net-card">
    <template #header>
      <div class="net-head">
        <span class="net-title">牧群网络吞吐</span>
        <span class="muted net-sub">近 1 小时 · 全节点聚合 · 30s 粒度</span>
        <span class="net-legend"><i class="rx" />下行 <i class="tx" />上行</span>
      </div>
    </template>

    <div class="net-wrap">
      <svg :viewBox="`0 0 ${W} ${H}`" class="net-svg" @mousemove="onMove" @mouseleave="hover = null">
        <defs>
          <linearGradient id="net-rx-area" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0" stop-color="#eef1f5" stop-opacity="0.10" />
            <stop offset="1" stop-color="#eef1f5" stop-opacity="0.02" />
          </linearGradient>
        </defs>

        <!-- 网格:水平虚线 + Y 轴刻度 -->
        <g v-for="t in yTicks" :key="t.y">
          <line :x1="PAD.l" :x2="PAD.l + plotW" :y1="t.y" :y2="t.y" stroke="#23272e" stroke-dasharray="3 5" vector-effect="non-scaling-stroke" />
          <text :x="PAD.l - 8" :y="t.y + 3.5" text-anchor="end" class="net-axis">{{ t.label }}</text>
        </g>
        <!-- X 轴时间刻度 -->
        <g v-for="t in xTicks" :key="t.x">
          <text :x="t.x" :y="H - 8" :text-anchor="t.x === PAD.l ? 'start' : t.x === PAD.l + plotW ? 'end' : 'middle'" class="net-axis">{{ t.label }}</text>
        </g>

        <template v-if="hasData">
          <path :d="areaRx" fill="url(#net-rx-area)" />
          <path :d="pathRx" fill="none" stroke="#eef1f5" stroke-width="1.6" stroke-linejoin="round" vector-effect="non-scaling-stroke" />
          <path :d="pathTx" fill="none" stroke="#63d3a6" stroke-width="1.4" stroke-linejoin="round" vector-effect="non-scaling-stroke" />

          <!-- 悬停:竖线 + 桶点 + 读数气泡 -->
          <g v-if="hover">
            <line :x1="hover.x" :x2="hover.x" :y1="PAD.t" :y2="PAD.t + plotH" stroke="#3a3f47" vector-effect="non-scaling-stroke" />
            <circle :cx="hover.x" :cy="yOf(hover.p.rx)" r="2.6" fill="#eef1f5" />
            <circle :cx="hover.x" :cy="yOf(hover.p.tx)" r="2.6" fill="#63d3a6" />
            <g :transform="`translate(${hover.x > PAD.l + plotW / 2 ? hover.x - 150 : hover.x + 10}, ${PAD.t + 6})`">
              <rect width="140" height="46" rx="8" fill="#1b1f25" stroke="#3a3f47" />
              <text x="10" y="17" class="net-tip-strong">{{ fmtClockS(hover.p.ts) }}</text>
              <text x="10" y="35" class="net-tip"><tspan fill="#eef1f5">↓ {{ fmtRate(hover.p.rx) }}</tspan><tspan fill="#63d3a6" dx="10">↑ {{ fmtRate(hover.p.tx) }}</tspan></text>
            </g>
          </g>
        </template>
        <text v-else x="480" y="110" text-anchor="middle" class="net-empty">暂无心跳数据——节点接入后自动汇聚</text>
      </svg>
    </div>
  </n-card>
</template>

<style>
/* 非 scoped:模板元素无 scoped 属性;类名 net-* 仅本卡使用 */
.net-card { margin-bottom: 14px; }
.net-head { display: flex; align-items: baseline; gap: 16px; }
.net-title { font-weight: 600; font-size: 14px; }
.net-sub { font-size: 12px; }
.net-legend { margin-left: auto; display: inline-flex; align-items: center; gap: 6px; font-size: 12px; color: var(--text-2); }
.net-legend i { width: 14px; height: 2px; border-radius: 2px; display: inline-block; }
.net-legend .rx { background: #eef1f5; margin-right: 4px; }
.net-legend .tx { background: #63d3a6; margin-right: 4px; }
.net-svg { display: block; width: 100%; height: auto; cursor: crosshair; }
.net-axis { font-size: 11px; fill: var(--text-2); font-family: "JetBrains Mono", Consolas, monospace; }
.net-empty { font-size: 13px; fill: #676e7a; }
.net-tip { font-size: 11.5px; fill: var(--text-2); font-family: "JetBrains Mono", Consolas, monospace; }
.net-tip-strong { font-size: 12px; fill: #eef1f5; font-family: "JetBrains Mono", Consolas, monospace; }
</style>
