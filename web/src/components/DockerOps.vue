<script setup lang="ts">
// Docker 容器生命周期操作(docs/01 §3.4 白名单):启动/停止/重启/日志/拉取/更新镜像。
// 走既有指令链路(POST /nodes/{id}/commands),状态机/审计/重试自动继承;
// 日志结果经指令详情抽屉查看。
import { ref } from 'vue'
import { NButton, NDropdown, NModal, useMessage } from 'naive-ui'
import { api } from '../api'
import LogStreamModal from './LogStreamModal.vue'

const props = defineProps<{
  nodeID: string
  appName: string      // 容器名(探测器上报)
  status: string       // running / stopped / …
  hasDockerCap: boolean // 节点 capabilities 含 docker
}>()
const message = useMessage()

const sending = ref(false)
const showConfirm = ref(false)
const confirmOp = ref<{ op: string; label: string } | null>(null)
const showLog = ref(false)

const opLabel: Record<string, string> = {
  start: '启动', stop: '停止', restart: '重启', pull: '拉取镜像', update: '更新镜像',
}

function options() {
  const opts: { label: string; key: string; disabled?: boolean }[] = []
  if (props.status !== 'running') opts.push({ label: '启动', key: 'start' })
  if (props.status === 'running') {
    opts.push({ label: '重启', key: 'restart' })
    opts.push({ label: '停止', key: 'stop' })
  }
  opts.push({ label: '日志', key: 'logs' })
  opts.push({ label: '更新镜像(拉取并按原配置重建)', key: 'update' })
  opts.push({ label: '仅拉取镜像(不重建容器)', key: 'pull' })
  return opts
}

function onMenuSelect(key: string) {
  if (key === 'logs') {
    showLog.value = true
    return
  }
  confirmOp.value = { op: key, label: opLabel[key] ?? key }
  showConfirm.value = true
}

async function execute() {
  const conf = confirmOp.value
  if (!conf) return
  sending.value = true
  try {
    const data = await api.post<{ command_id: string }>(`/nodes/${props.nodeID}/commands`, {
      type: 'docker.op',
      timeout_s: conf.op === 'pull' || conf.op === 'update' ? 300 : 60,
      payload: { op: conf.op, container: props.appName },
    })
    message.success(`指令已下发(${data.command_id.slice(0, 10)}…),结果见「指令」列表`)
  } catch (e: any) {
    message.error(e.message)
  } finally {
    sending.value = false
    showConfirm.value = false
  }
}
</script>

<template>
  <n-dropdown v-if="hasDockerCap" trigger="click" :options="options()" @select="onMenuSelect">
    <n-button size="tiny" quaternary type="primary">操作 ▾</n-button>
  </n-dropdown>
  <span v-else class="muted" style="font-size:12px">—</span>

  <n-modal v-model:show="showConfirm" preset="dialog" type="warning"
    :title="`确认${confirmOp?.label ?? ''}:${appName}`"
    positive-text="执行" negative-text="取消" :loading="sending" @positive-click="execute">
    <p v-if="confirmOp">
      将在节点上对容器 <b>{{ appName }}</b> 执行「{{ confirmOp.label }}」。
      <template v-if="confirmOp.op === 'update'">
        流程:拉取最新镜像 → 停止旧容器 → <b>按原配置重建</b>(端口/挂载/环境变量等全部原样保留) →
        启动并删除旧容器。过程服务会短暂中断,任一步失败自动回滚旧容器,重建后即运行新镜像。
      </template>
      <template v-else-if="confirmOp.op === 'pull'">
        仅拉取最新镜像到本机,不改动运行中的容器;需执行「更新镜像」重建后新镜像才会生效。
      </template>
      <template v-else-if="confirmOp.op === 'stop'">容器内服务将中断,确定?</template>
      操作全程审计留痕。
    </p>
  </n-modal>

  <!-- 容器日志实时弹窗(tail -f 流式,docs/04 §9) -->
  <LogStreamModal v-model:show="showLog" :nodeID="nodeID" :container="appName" />
</template>
