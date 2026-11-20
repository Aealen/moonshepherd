<script setup lang="ts">
// 面板更新通知:顶栏胶囊(发现新版本时显示)→ 弹窗展示版本与 Release 说明,
// binary 部署支持一键自更新(下载→校验→换身,面板重启数秒),docker/手动部署
// 跳转 Release 页按各自方式升级。私有仓库检查/下载需 HERD_GH_TOKEN,转公开后免配置。
import { computed, onMounted, ref } from 'vue'
import { NButton, NModal, NSpin, useMessage } from 'naive-ui'
import { api } from '../api'
import BorderGlow from './BorderGlow.vue'

const message = useMessage()

type UpdateInfo = {
  current: string
  available: boolean
  latest?: { version: string; notes: string; url: string; published_at: number }
  mode: 'binary' | 'docker' | 'manual'
  arch: string
  reason?: string
}

type UpdateProg = {
  stage: 'idle' | 'download' | 'verify' | 'unpack' | 'replace' | 'exec' | 'error'
  detail?: string
  total: number
  done: number
  via?: string
}

const show = ref(false)
const info = ref<UpdateInfo | null>(null)
const checking = ref(false)
const updating = ref(false)
const waitingRestart = ref(false)
const everChecked = ref(false)
const prog = ref<UpdateProg | null>(null)
const lastError = ref('')
let lastSample = { at: 0, done: 0 }
const speedText = ref('')

const pct = computed(() =>
  prog.value && prog.value.total > 0 ? Math.min(100, Math.round((prog.value.done / prog.value.total) * 100)) : 0)

const stageText = computed(() => {
  const s = prog.value?.stage
  return ({ download: '下载面板包', verify: '校验 sha256', unpack: '解压面板包', replace: '替换面板文件', exec: '面板原地重启' } as Record<string, string>)[s ?? ''] ?? '准备更新'
})

function fmtMB(b: number): string {
  return (b / 1048576).toFixed(1)
}

async function check(force = false) {
  checking.value = true
  try {
    info.value = await api.get<UpdateInfo>(`/system/update-check${force ? '?force=1' : ''}`)
    everChecked.value = true
  } catch { /* 检查失败静默,顶栏不显示 */ } finally {
    checking.value = false
  }
}

async function openPanel() {
  show.value = true
  if (!everChecked.value || true) await check(true) // 打开面板即强制检查(绕缓存)
}

// 自更新流程:POST 触发 → 轮询 /system/update-progress 展示下载/校验/替换进度
// (服务端明确失败会以 HTTP 错误响应,直接展示真实原因,不再伪装「重启中」)
// → exec 换身时连接切断(轮询不可达)→ 轮询 /system/info 直到版本变化确认完成。
async function executeUpdate() {
  updating.value = true
  lastError.value = ''
  prog.value = null
  speedText.value = ''
  const target = info.value?.latest?.version
  try {
    await api.post('/system/update-execute', {})
    // 正常不会拿到 200:exec 换身时连接随旧进程消失;拿到即异常快速完成
  } catch (e: any) {
    if (e?.status) {
      // HTTP 错误响应 = 服务端明确失败(下载超时/校验失败/权限不足等),原文展示
      updating.value = false
      lastError.value = e.message || '更新失败'
      await check(true)
      return
    }
    /* 纯网络断 = POST 已发出、面板开始执行,继续走进度轮询 */
  }

  // 阶段一:轮询进度(原进程存活期间可达)
  const progDeadline = Date.now() + 15 * 60_000
  while (Date.now() < progDeadline) {
    await new Promise((r) => setTimeout(r, 1000))
    try {
      const p = await api.get<UpdateProg>('/system/update-progress')
      // 下载速率:两次采样差值
      if (p.stage === 'download' && lastSample.at > 0) {
        const dt = (Date.now() - lastSample.at) / 1000
        if (dt > 0.5) speedText.value = `${Math.max(0, (p.done - lastSample.done) / dt / 1024).toFixed(0)} KB/s`
      }
      if (p.stage === 'download') lastSample = { at: Date.now(), done: p.done }
      prog.value = p
      if (p.stage === 'error') {
        updating.value = false
        lastError.value = p.detail || '更新失败'
        await check(true)
        return
      }
      if (p.stage === 'idle') break // 换身完成,新进程进度已复位
    } catch {
      break // 进度接口不可达 = exec 换身发生,进入等待重启
    }
  }
  lastSample = { at: 0, done: 0 }

  // 阶段二:等面板以新版本回来
  waitingRestart.value = true
  const deadline = Date.now() + 90_000
  while (Date.now() < deadline) {
    await new Promise((r) => setTimeout(r, 2500))
    try {
      const info2 = await api.get<{ panel_version: string }>('/system/info')
      if (target && info2.panel_version === target) {
        message.success(`面板已更新至 ${target}`)
        waitingRestart.value = false
        updating.value = false
        prog.value = null
        await check(true)
        return
      }
    } catch { /* 重启中,继续轮询 */ }
  }
  waitingRestart.value = false
  updating.value = false
  lastError.value = '等待面板重启超时,请手动确认服务状态'
}

function gotoRelease() {
  const u = info.value?.latest?.url
  if (u) window.open(u, "_blank")
}

defineExpose({ check })
onMounted(() => check(false))
</script>

<template>
  <div>
    <!-- 新版本胶囊:边框光晕随指针方向点亮(纯 CSS,无库依赖) -->
    <BorderGlow v-if="info?.available" :hue="45" :sat="55" :light="72" :height="28" :glow-radius="16"
      :edge-sensitivity="14" class="update-chip" @click="openPanel">
      <span class="update-chip-label">新版本 {{ info.latest?.version }}</span>
    </BorderGlow>

  <n-modal v-model:show="show" preset="card" title="面板更新" style="width:560px">
    <div v-if="checking" style="display:flex; align-items:center; gap:10px; padding:12px 0">
      <n-spin size="small" /> <span class="muted">正在检查最新发布…</span>
    </div>

    <template v-else-if="info">
      <div v-if="info.reason === 'unreachable'" style="line-height:2">
        <div style="font-weight:600">无法检查更新</div>
        <div class="muted" style="font-size:12.5px; margin-top:6px">
          GitHub 不可达,或仓库未公开且未配置访问令牌。可在面板环境变量中配置
          <code>HERD_GH_TOKEN</code>(GitHub 令牌,需 contents:read)后重试;
          也可直接前往 Release 页查看最新版本。
        </div>
        <n-button size="small" style="margin-top:10px" @click="gotoRelease">
          前往 Release 页
        </n-button>
      </div>

      <template v-else-if="info.latest">
        <div style="display:flex; align-items:baseline; gap:12px; margin-bottom:10px">
          <span style="font-weight:600">
            {{ info.available ? `发现新版本 ${info.latest.version}` : `已是最新版本 ${info.current}` }}
          </span>
          <span class="muted" style="font-size:12px">当前 {{ info.current }} · {{ info.mode === 'docker' ? 'Docker 部署' : info.mode === 'binary' ? '二进制部署' : '手动部署' }}</span>
          <n-button size="tiny" quaternary style="margin-left:auto" :loading="checking" @click="check(true)">检查更新</n-button>
        </div>

        <template v-if="info.available">
          <!-- binary:一键自更新 -->
          <template v-if="info.mode === 'binary'">
            <div class="muted" style="font-size:12.5px; line-height:1.9; margin-bottom:10px">
              更新流程:下载 v{{ info.latest.version }} 面板包 → sha256 校验 → 原子替换
              (含 distro-bin 分发源,节点升级随面板同版本)→ 面板原地重启(约数秒)。
              失败自动回滚旧版;升级前建议确认已按 docs/08 备份数据目录。
              国内机器建议配置 HERD_GH_PROXY 本地代理,下载慢时自动切换。
            </div>
            <n-button type="primary" size="small" :loading="updating || waitingRestart" @click="executeUpdate">
              {{ waitingRestart ? '面板重启中…' : updating ? '更新中…' : '下载并更新' }}
            </n-button>
            <!-- 实时进度:阶段 + 下载百分比/字节/速率/线路 -->
            <div v-if="updating && prog" class="uprog">
              <div class="uprog-head">
                <span>{{ stageText }}</span>
                <span v-if="prog.stage === 'download' && prog.total > 0" class="uprog-num mono">
                  {{ pct }}% · {{ fmtMB(prog.done) }} / {{ fmtMB(prog.total) }} MB<template v-if="speedText"> · {{ speedText }}</template> · {{ prog.via === 'proxy' ? '代理' : '直连' }}
                </span>
                <span v-else-if="prog.detail" class="uprog-num">{{ prog.detail }}</span>
              </div>
              <div v-if="prog.stage === 'download' && prog.total > 0" class="uprog-bar">
                <div class="uprog-fill" :style="{ width: pct + '%' }" />
              </div>
            </div>
            <!-- 失败原因:原文展示,不再静默 -->
            <div v-if="lastError" class="uerr">{{ lastError }}</div>
          </template>

          <!-- docker:容器不可变,跳转 Release + pull 指引 -->
          <template v-else-if="info.mode === 'docker'">
            <div class="muted" style="font-size:12.5px; line-height:1.9; margin-bottom:10px">
              Docker 部署不支持面板内自更新。请在面板所在机器执行:
            </div>
            <pre class="term" style="margin:0 0 12px">docker pull {{ 'ghcr.io/aealen/moonshepherd:' + info.latest.version }}
# 然后按原容器配置重建(compose: docker compose up -d)</pre>
            <n-button size="small" @click="gotoRelease">前往 Release 页</n-button>
          </template>

          <!-- manual:非 linux/手动部署 -->
          <template v-else>
            <div class="muted" style="font-size:12.5px; margin-bottom:10px">
              当前运行环境不支持面板内自更新,请前往 Release 页手动下载对应平台的二进制替换。
            </div>
            <n-button size="small" @click="gotoRelease">前往 Release 页</n-button>
          </template>
        </template>

        <!-- Release 说明(折叠展示,超长截断) -->
        <div v-if="info.latest.notes" style="margin-top:16px">
          <div style="font-weight:600; font-size:13px; margin-bottom:6px">Release 说明</div>
          <pre class="term" style="padding:10px 12px; margin:0; max-height:220px; overflow:auto; font-size:12px; white-space:pre-wrap">{{ info.latest.notes }}</pre>
        </div>
      </template>
    </template>

    <div v-else style="padding:12px 0" class="muted">暂无更新信息</div>
  </n-modal>
  </div>
</template>

<style scoped>
.update-chip {
  font-variant-numeric: tabular-nums;
}
.update-chip-label {
  font-size: 12.5px;
  font-weight: 600;
  color: #e3c47f;
  letter-spacing: 0.2px;
}

/* 更新进度:夜空单色系,绿色填充=进行中 */
.uprog { margin-top: 12px; }
.uprog-head { display: flex; align-items: baseline; gap: 10px; font-size: 12.5px; }
.uprog-head > span:first-child { font-weight: 600; color: #eef1f5; }
.uprog-num { margin-left: auto; color: #a4abb8; font-size: 12px; font-variant-numeric: tabular-nums; }
.uprog-bar {
  margin-top: 7px; height: 6px; border-radius: 3px; overflow: hidden;
  background: rgba(238, 241, 245, 0.08);
}
.uprog-fill {
  height: 100%; border-radius: 3px; background: #63d3a6;
  box-shadow: 0 0 8px rgba(99, 211, 166, 0.5);
  transition: width 0.4s ease;
}
.uerr {
  margin-top: 12px; padding: 9px 12px; border-radius: 8px; font-size: 12.5px; line-height: 1.7;
  color: #ef9d93; background: rgba(239, 157, 147, 0.08);
  border: 1px solid rgba(239, 157, 147, 0.25);
  word-break: break-all;
}
.mono { font-family: "JetBrains Mono", Consolas, monospace; }
</style>
