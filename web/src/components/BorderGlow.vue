<script setup lang="ts">
// 边框光晕胶囊:指针移入时,朝指针方向的边缘亮起一圈渐变光,
// 越靠近边缘越亮(纯 CSS 变量 + 指针角度/边距计算,无库依赖)。
// 用于「新版本」提示等需要吸引注意但又不喧宾夺主的小控件。
import { ref } from 'vue'

const props = withDefaults(defineProps<{
  /** 主色(HSL 三段:色相 饱和度 亮度),默认夜空牧绿 */
  hue?: number
  sat?: number
  light?: number
  /** 胶囊高度(px) */
  height?: number
  /** 外发光扩散半径(px) */
  glowRadius?: number
  /** 边缘点亮灵敏度:越小越容易点亮 */
  edgeSensitivity?: number
}>(), {
  hue: 156, sat: 55, light: 68, // #63d3a6 ≈ hsl(156 55% 68%)
  height: 28,
  glowRadius: 14,
  edgeSensitivity: 18,
})

const cardRef = ref<HTMLElement | null>(null)

// 指针位置 → 极角(光晕朝向) + 边缘接近度(0-100,越靠边越亮)
function onPointerMove(e: PointerEvent) {
  const el = cardRef.value
  if (!el) return
  const rect = el.getBoundingClientRect()
  const x = e.clientX - rect.left
  const y = e.clientY - rect.top
  const cx = rect.width / 2
  const cy = rect.height / 2
  const dx = x - cx
  const dy = y - cy
  // 边缘接近度:中心 0,边缘 1(按长宽比归一)
  const nx = cx > 0 ? Math.abs(dx) / cx : 0
  const ny = cy > 0 ? Math.abs(dy) / cy : 0
  const edge = Math.min(1, Math.max(nx, ny)) * 100
  // 极角:atan2 反推光晕朝向(0deg = 正上方)
  let deg = 0
  if (dx !== 0 || dy !== 0) {
    deg = (Math.atan2(dy, dx) * 180) / Math.PI + 90
    if (deg < 0) deg += 360
  }
  el.style.setProperty('--edge-proximity', edge.toFixed(2))
  el.style.setProperty('--cursor-angle', `${deg.toFixed(2)}deg`)
}

function onPointerLeave() {
  const el = cardRef.value
  if (!el) return
  el.style.setProperty('--edge-proximity', '0')
}
</script>

<template>
  <div ref="cardRef" class="bglow" :style="{
    '--h': String(props.hue), '--s': props.sat + '%', '--l': props.light + '%',
    '--hgt': props.height + 'px', '--glow-pad': props.glowRadius + 'px',
    '--edge-sens': String(props.edgeSensitivity),
  }" @pointermove="onPointerMove" @pointerleave="onPointerLeave">
    <span class="bglow-edge" aria-hidden="true" />
    <div class="bglow-inner"><slot /></div>
  </div>
</template>

<style scoped>
/* 容器:浅描边胶囊,内层实底 + 外层光晕层 */
.bglow {
  --edge-proximity: 0;
  --cursor-angle: 45deg;
  position: relative;
  display: inline-flex;
  align-items: center;
  height: var(--hgt);
  border-radius: 999px;
  border: 1px solid rgba(238, 241, 245, 0.14);
  background: #14171c;
  isolation: isolate;
  cursor: pointer;
  transition: border-color 0.2s ease;
}
.bglow:hover { border-color: hsla(var(--h), var(--s), var(--l), 0.45); }

/* 渐变描边层:朝指针方向的锥形遮罩点亮彩色边框 */
.bglow::before {
  content: "";
  position: absolute;
  inset: 0;
  border-radius: inherit;
  border: 1px solid transparent;
  background:
    linear-gradient(#14171c 0 100%) padding-box,
    conic-gradient(
      from var(--cursor-angle) at center,
      hsla(var(--h), var(--s), var(--l), 0.95) 0deg,
      hsla(calc(var(--h) + 40), var(--s), var(--l), 0.55) 60deg,
      hsla(calc(var(--h) - 40), var(--s), var(--l), 0.45) 120deg,
      transparent 170deg,
      transparent 190deg,
      hsla(var(--h), var(--s), var(--l), 0.4) 240deg,
      hsla(var(--h), var(--s), var(--l), 0.95) 360deg
    ) border-box;
  opacity: calc((var(--edge-proximity) - var(--edge-sens)) / (100 - var(--edge-sens)));
  transition: opacity 0.35s ease-out;
  z-index: -1;
  pointer-events: none;
}

/* 外发光层:向四周晕开的柔光,越近边缘越强 */
.bglow-edge {
  position: absolute;
  inset: calc(var(--glow-pad) * -1);
  border-radius: inherit;
  pointer-events: none;
  z-index: 1;
  mask-image: conic-gradient(
    from var(--cursor-angle) at center,
    black 4%, transparent 18%, transparent 82%, black 96%
  );
  opacity: calc((var(--edge-proximity) - var(--edge-sens)) / (100 - var(--edge-sens)));
  transition: opacity 0.35s ease-out;
  mix-blend-mode: plus-lighter;
}
.bglow-edge::before {
  content: "";
  position: absolute;
  inset: var(--glow-pad);
  border-radius: inherit;
  box-shadow:
    inset 0 0 0 1px hsla(var(--h), var(--s), var(--l), 0.9),
    inset 0 0 3px 0 hsla(var(--h), var(--s), var(--l), 0.5),
    inset 0 0 8px 0 hsla(var(--h), var(--s), var(--l), 0.3),
    inset 0 0 20px 1px hsla(var(--h), var(--s), var(--l), 0.15),
    0 0 3px 0 hsla(var(--h), var(--s), var(--l), 0.5),
    0 0 8px 0 hsla(var(--h), var(--s), var(--l), 0.3),
    0 0 20px 1px hsla(var(--h), var(--s), var(--l), 0.15);
}

/* 内容层:保持胶囊原有内边距与文字风格 */
.bglow-inner {
  position: relative;
  z-index: 2;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 0 12px;
  border-radius: inherit;
  white-space: nowrap;
}
</style>
