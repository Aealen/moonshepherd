<script setup lang="ts">
// 面板更新通知:顶栏胶囊(发现新版本时显示)→ 弹窗展示版本与 Release 说明,
// binary 部署支持一键自更新(下载→校验→换身,面板重启数秒),docker/手动部署
// 跳转 Release 页按各自方式升级。私有仓库检查/下载需 HERD_GH_TOKEN,转公开后免配置。
import { onMounted, ref } from 'vue'
import { NButton, NModal, NSpin, useMessage } from 'naive-ui'
import { api } from '../api'

const message = useMessage()

type UpdateInfo = {
  current: string
  available: boolean
  latest?: { version: string; notes: string; url: string; published_at: number }
  mode: 'binary' | 'docker' | 'manual'
  arch: string
  reason?: string
}

const show = ref(false)
const info = ref<UpdateInfo | null>(null)
const checking = ref(false)
const updating = ref(false)
const waitingRestart = ref(false)
const everChecked = ref(false)

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

// 自更新:exec 换身时原连接消失属预期——轮询 /system/info 直到版本变化或超时
async function executeUpdate() {
  updating.value = true
  const target = info.value?.latest?.version
  try {
    await api.post('/system/update-execute', {})
  } catch { /* exec 换身时连接被切断,属预期 */ }
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
        await check(true)
        return
      }
    } catch { /* 重启中,继续轮询 */ }
  }
  waitingRestart.value = false
  updating.value = false
  message.error('等待面板重启超时,请手动确认服务状态')
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
    <n-button v-if="info?.available" size="small" type="warning" secondary class="update-chip"
      @click="openPanel">
      新版本 {{ info.latest?.version }}
    </n-button>

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
            </div>
            <n-button type="primary" size="small" :loading="updating || waitingRestart" @click="executeUpdate">
              {{ waitingRestart ? '面板重启中…' : updating ? '更新中…' : '下载并更新' }}
            </n-button>
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
</style>
