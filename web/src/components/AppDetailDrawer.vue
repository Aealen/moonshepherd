<script setup lang="ts">
// 应用详情抽屉（共享组件）：应用中心 + 节点详情·应用 tab 复用。
import { computed, h, ref } from 'vue'
import {
  NDrawer, NDrawerContent, NDescriptions, NDescriptionsItem, NDataTable, NTag,
} from 'naive-ui'
import { fmtTime, type AppRow } from '../api'
import FileViewerModal from './FileViewerModal.vue'

const props = defineProps<{ show: boolean; app: AppRow | null }>()
const emit = defineEmits<{ (e: 'update:show', v: boolean): void }>()

interface PortEntry { ip: string; public: number; private: number; type: string }
interface MountEntry { source: string; dest: string; rw: boolean; mode?: string }
interface NetEntry { name: string; ip: string }

const ports = computed<PortEntry[]>(() => {
  const p = props.app?.detail?.ports
  return Array.isArray(p) ? p : []
})
const mounts = computed<MountEntry[]>(() => {
  const m = props.app?.detail?.mounts
  return Array.isArray(m) ? m : []
})
const nets = computed<NetEntry[]>(() => {
  const n = props.app?.detail?.networks
  return Array.isArray(n) ? n : []
})
// env "KEY=VAL" → 表格行；无 = 的整行当 key
const envRows = computed(() => {
  const e = props.app?.detail?.env
  if (!Array.isArray(e)) return []
  return e.map((s) => {
    const i = String(s).indexOf('=')
    return i > 0 ? { k: String(s).slice(0, i), v: String(s).slice(i + 1) } : { k: String(s), v: '' }
  })
})
// 兜底：未结构化展示的剩余字段
const restEntries = computed<Array<[string, any]>>(() => {
  if (!props.app) return []
  const skip = new Set(['image', 'id', 'ports', 'mounts', 'networks', 'env', 'user_env',
    'conf_files', 'conf_dir', 'config_candidates',
    'run_mode', 'entrypoint', 'cmd', 'restart',
    'name', 'hostname', 'user', 'workdir', 'healthcheck', 'privileged', 'devices',
    'cap_add', 'cap_drop', 'extra_hosts', 'dns', 'sysctls', 'ulimits',
    'memory', 'nano_cpus', 'network_mode', 'labels',
    'compose_project', 'compose_service', 'compose_working_dir',
    'compose_project_working_dir', 'compose_config_files', 'compose_project_config_files'])
  return Object.entries(props.app.detail ?? {}).filter(([k]) => !skip.has(k))
})

// ---------- Docker 启动方式(docs/06 应用详情) ----------

// compose 标签 = Docker Compose;run_mode 由 Agent 探测写入,老数据回退看 compose_project
const runMode = computed<'compose' | 'run' | undefined>(() => {
  const d = props.app?.detail ?? {}
  if (d.run_mode === 'compose' || d.compose_project) return 'compose'
  if (d.run_mode === 'run') return 'run'
  return undefined
})
const composeInfo = computed(() => {
  if (runMode.value !== 'compose') return null
  const d = props.app?.detail ?? {}
  return {
    project: d.compose_project ? String(d.compose_project) : '',
    service: d.compose_service ? String(d.compose_service) : '',
    workdir: d.compose_working_dir ?? d.compose_project_working_dir ?? '',
    files: d.compose_project_config_files ?? d.compose_config_files ?? '',
  }
})
// 启动命令:容器进程定义(Entrypoint/Cmd),如实展示不拼 docker run 串
const startCmd = computed(() => {
  const d = props.app?.detail ?? {}
  const ep = Array.isArray(d.entrypoint) ? d.entrypoint.map(String) : []
  const cmd = Array.isArray(d.cmd) ? d.cmd.map(String) : []
  if (!ep.length && !cmd.length) return null
  return { entrypoint: ep, cmd }
})
const restartPolicy = computed(() => {
  const r = props.app?.detail?.restart
  return r ? String(r) : ''
})
const cmdLines = computed<string[]>(() => {
  const lines: string[] = []
  if (startCmd.value) {
    lines.push('ENTRYPOINT  ' + (startCmd.value.entrypoint.join(' ') || '(空)'))
    lines.push('CMD         ' + (startCmd.value.cmd.join(' ') || '(空)'))
  }
  if (restartPolicy.value) lines.push('RESTART     ' + restartPolicy.value)
  return lines
})

// ---------- 重建参考:由 inspect 实测配置逆向合成等价 docker run ----------
// 非原始命令(Docker 引擎不存命令行);命名卷显示落地路径,env 取运行时注入部分(user_env)。
const runCmdLines = computed<string[] | null>(() => {
  if (runMode.value !== 'run') return null // compose 场景有自己的编排定义,不合成
  const d: any = props.app?.detail ?? {}
  const img = d.image ? String(d.image) : ''
  if (!img) return null
  const args: string[] = []
  if (d.name) args.push(`--name ${d.name}`)
  if (d.restart) args.push(`--restart ${d.restart}`)
  if (d.hostname) args.push(`--hostname ${d.hostname}`)
  if (d.user) args.push(`--user ${d.user}`)
  if (d.workdir) args.push(`--workdir ${d.workdir}`)
  if (d.network_mode) args.push(`--network ${d.network_mode}`)
  if (d.privileged) args.push('--privileged')
  for (const p of Array.isArray(d.ports) ? d.ports : []) {
    if (p?.public) {
      const ip = p.ip && p.ip !== '0.0.0.0' ? `${p.ip}:` : ''
      args.push(`-p ${ip}${p.public}:${p.private}/${p.type ?? 'tcp'}`)
    }
  }
  for (const m of Array.isArray(d.mounts) ? d.mounts : []) {
    if (m?.source && m?.dest) args.push(`-v ${m.source}:${m.dest}${m.rw === false ? ':ro' : ''}`)
  }
  // env:优先 user_env(运行时注入),老 Agent 无此字段回退全量 env 并剔除系统内置噪音
  const envs = Array.isArray(d.user_env) && d.user_env.length ? d.user_env : (Array.isArray(d.env) ? d.env : [])
  for (const e of envs) {
    const s = String(e)
    if (s && !s.startsWith('PATH=')) args.push(`-e ${s}`)
  }
  if (d.memory) args.push(`--memory ${fmtMem(Number(d.memory))}`)
  if (d.nano_cpus) args.push(`--cpus ${Number(d.nano_cpus) / 1e9}`)
  for (const dv of Array.isArray(d.devices) ? d.devices : []) {
    if (dv?.PathOnHost) args.push(`--device ${dv.PathOnHost}:${dv.PathInContainer ?? dv.PathOnHost}`)
  }
  for (const c of Array.isArray(d.cap_add) ? d.cap_add : []) args.push(`--cap-add ${c}`)
  for (const h of Array.isArray(d.extra_hosts) ? d.extra_hosts : []) args.push(`--add-host ${h}`)
  for (const s of Array.isArray(d.dns) ? d.dns : []) args.push(`--dns ${s}`)
  if (!args.length) return null

  // 按参数边界折行(Drawer 终端块约 64 字符/行,行尾续行符,免横向滚动)
  const lines: string[] = []
  let cur = 'docker run -d'
  for (const a of args) {
    if ((cur + ' ' + a).length > 64) {
      lines.push(cur + ' \\')
      cur = '    ' + a
    } else {
      cur += ' ' + a
    }
  }
  lines.push(cur + ' ' + img)
  return lines
})
function fmtMem(bytes: number): string {
  return bytes >= 1 << 30 ? `${Math.round(bytes / (1 << 30))}g` : `${Math.round(bytes / (1 << 20))}m`
}

// ---------- 中间件(nginx/caddy)专属:配置文件区 ----------

interface ConfCandidate { path: string; source: string; main?: boolean }

const isReverseProxy = computed(() => {
  const t = (props.app?.app_type ?? '').toLowerCase()
  const img = String(props.app?.detail?.image ?? props.app?.version ?? '').toLowerCase()
  return t === 'nginx' || t === 'caddy' || img.includes('nginx') || img.includes('caddy')
})
const middlewareKind = computed(() => {
  const t = (props.app?.app_type ?? '').toLowerCase()
  const img = String(props.app?.detail?.image ?? props.app?.version ?? '').toLowerCase()
  if (t === 'caddy' || img.includes('caddy')) return 'caddy'
  return 'nginx'
})

// 候选配置:config_candidates 优先,退回 conf_files + conf_dir 拼路径
const confCandidates = computed<ConfCandidate[]>(() => {
  const cands = props.app?.detail?.config_candidates
  if (Array.isArray(cands) && cands.length) {
    return cands.map((c: any) => ({ path: String(c.path), source: String(c.source ?? ''), main: !!c.main }))
  }
  const dir = String(props.app?.detail?.conf_dir ?? '')
  const files = props.app?.detail?.conf_files
  if (dir && Array.isArray(files)) {
    return files.slice(0, 8).map((f: any) => ({ path: dir + '/' + f, source: '目录扫描' }))
  }
  return []
})

const showFile = ref('') // 待查看的文件路径(空=关)
const showFileAsBool = computed({ get: () => showFile.value !== '', set: (v: boolean) => { if (!v) showFile.value = '' } })

const sourceLabel: Record<string, string> = {
  'nginx -T': '运行时加载(最准)',
  'caddy adapt': '已验证可解析',
  'docker-mount(nginx)': '容器挂载',
  'docker-mount(caddy)': '容器挂载',
  'dir-scan': '目录扫描',
}
function srcLabel(s: string) {
  for (const k of Object.keys(sourceLabel)) {
    if (s.startsWith(k)) return sourceLabel[k]
  }
  return s || '探测'
}

const portCols = [
  { title: '宿主地址', key: 'ip', width: 120, render: (p: PortEntry) => h('span', { class: 'mono' }, p.ip || '0.0.0.0') },
  { title: '宿主端口', key: 'public', width: 100, render: (p: PortEntry) => h('span', { class: 'mono' }, p.public ? String(p.public) : '—') },
  { title: '容器端口', key: 'private', width: 100, render: (p: PortEntry) => h('span', { class: 'mono' }, String(p.private)) },
  { title: '协议', key: 'type', width: 80, render: (p: PortEntry) => h(NTag, { size: 'tiny', bordered: false }, { default: () => p.type || 'tcp' }) },
]
const mountCols = [
  { title: '宿主路径', key: 'source', render: (m: MountEntry) => h('span', { class: 'mono', style: 'font-size:12px; word-break:break-all' }, m.source || m.mode || '—') },
  { title: '容器路径', key: 'dest', width: 200, render: (m: MountEntry) => h('span', { class: 'mono muted', style: 'font-size:12px; word-break:break-all' }, m.dest) },
  { title: '权限', key: 'rw', width: 70, render: (m: MountEntry) => h(NTag, { size: 'tiny', bordered: false, type: m.rw ? 'warning' : 'default' }, { default: () => m.rw ? '读写' : '只读' }) },
]
const envCols = [
  { title: '变量', key: 'k', width: 190, render: (e: { k: string }) => h('span', { class: 'mono', style: 'font-weight:600' }, e.k) },
  { title: '值', key: 'v', render: (e: { v: string }) => h('span', { class: 'mono muted', style: 'font-size:12px; word-break:break-all' }, e.v || '（空）') },
]
</script>

<template>
  <n-drawer :show="show" :width="640" placement="right" @update:show="emit('update:show', $event)">
    <n-drawer-content v-if="app" closable>
      <template #header>
        <span style="font-weight:600">{{ app.app_name }}</span>
        <n-tag size="tiny" round :bordered="false" style="margin-left:10px"
          :type="app.status === 'running' ? 'success' : app.status === 'stopped' ? 'warning' : 'default'">
          {{ app.status === 'running' ? '运行中' : app.status === 'stopped' ? '已停止' : app.status }}
        </n-tag>
        <n-tag size="tiny" :bordered="false" style="margin-left:6px">{{ app.app_type }}</n-tag>
        <n-tag v-if="isReverseProxy" size="tiny" :bordered="false" type="info" style="margin-left:6px">
          {{ middlewareKind === 'caddy' ? 'Caddy 反向代理' : 'Nginx 反向代理' }}
        </n-tag>
      </template>

      <n-descriptions :column="1" size="small" bordered label-placement="left" style="margin-bottom:18px">
        <n-descriptions-item label="镜像"><span class="mono">{{ app.detail?.image || app.version || '—' }}</span></n-descriptions-item>
        <n-descriptions-item v-if="app.detail?.id" label="容器 ID">
          <span class="mono muted">{{ String(app.detail.id).slice(0, 12) }}</span>
        </n-descriptions-item>
        <n-descriptions-item label="启动方式" v-if="runMode">
          <n-tag size="tiny" :bordered="false" :type="runMode === 'compose' ? 'info' : 'default'">
            {{ runMode === 'compose' ? 'Docker Compose' : 'Docker Run' }}
          </n-tag>
        </n-descriptions-item>
        <template v-if="composeInfo">
          <n-descriptions-item label="Compose 项目"><span class="mono">{{ composeInfo.project || '—' }}</span></n-descriptions-item>
          <n-descriptions-item v-if="composeInfo.service" label="Compose 服务"><span class="mono">{{ composeInfo.service }}</span></n-descriptions-item>
          <n-descriptions-item v-if="composeInfo.workdir" label="工作目录">
            <span class="mono muted" style="font-size:12px; word-break:break-all">{{ composeInfo.workdir }}</span>
          </n-descriptions-item>
          <n-descriptions-item v-if="composeInfo.files" label="Compose 配置">
            <span class="mono muted" style="font-size:12px; word-break:break-all">{{ composeInfo.files }}</span>
          </n-descriptions-item>
        </template>
        <n-descriptions-item v-if="nets.length" label="网络">
          <span class="mono" style="font-size:12px">{{ nets.map((n) => `${n.name}${n.ip ? ' (' + n.ip + ')' : ''}`).join('，') }}</span>
        </n-descriptions-item>
        <n-descriptions-item v-if="app.node_name" label="所属节点">{{ app.node_name }}（{{ fmtTime(app.detected_at) }} 探测）</n-descriptions-item>
      </n-descriptions>

      <!-- 重建参考:逆向合成的等价 docker run 命令 -->
      <template v-if="runCmdLines">
        <div style="display:flex; align-items:baseline; margin:0 0 8px">
          <span style="font-weight:600">重建参考</span>
          <span class="muted" style="font-size:12px; margin-left:8px">
            等价 docker run · 由容器实测配置逆向合成(非原始命令;命名卷显示落地路径)
          </span>
        </div>
        <pre class="term" style="padding:10px 14px; margin:0 0 18px; user-select:all; word-break:normal; white-space:pre; overflow-x:auto">{{ runCmdLines.join('\n') }}</pre>
      </template>

      <!-- Docker 启动命令/重启策略 -->
      <template v-if="cmdLines.length">
        <div style="display:flex; align-items:center; margin:0 0 8px">
          <span style="font-weight:600">启动命令</span>
          <span class="muted" style="font-size:12px; margin-left:8px">容器进程定义(inspect 实测)</span>
        </div>
        <pre class="term" style="padding:10px 14px; margin:0 0 18px">{{ cmdLines.join('\n') }}</pre>
      </template>

      <!-- 中间件专属:配置文件区(发现→纳管工作流入口) -->
      <template v-if="isReverseProxy && confCandidates.length">
        <div style="display:flex; align-items:center; margin:0 0 8px">
          <span style="font-weight:600">配置文件</span>
          <span class="muted" style="font-size:12px; margin-left:8px">探测自动发现,可纳管后在线编辑/下发</span>
        </div>
        <div v-for="c in confCandidates" :key="c.path" class="conf-item" :class="{ main: c.main }">
          <n-tag size="tiny" :bordered="false" :type="c.main ? 'success' : 'default'" style="flex-shrink:0">
            {{ c.main ? '主配置' : '候选' }}
          </n-tag>
          <span class="mono conf-path" style="font-size:12.5px; flex:1; word-break:break-all" title="点击查看文件内容(只读)" @click="showFile = c.path">{{ c.path }}</span>
          <span class="muted" style="font-size:11.5px; flex-shrink:0">{{ srcLabel(c.source) }}</span>
        </div>
        <div class="muted" style="font-size:11.5px; margin:6px 2px 18px">
          节点详情 → 配置 页可一键纳管以上路径
        </div>
      </template>

      <template v-if="ports.length">
        <div style="font-weight:600; margin:0 0 8px">端口映射</div>
        <n-data-table size="small" :bordered="false" :single-line="false" style="margin-bottom:18px"
          :columns="portCols" :data="ports" :row-key="(p: any) => `${p.ip}:${p.public}:${p.private}`" />
      </template>

      <template v-if="mounts.length">
        <div style="font-weight:600; margin:0 0 8px">目录挂载</div>
        <n-data-table size="small" :bordered="false" :single-line="false" style="margin-bottom:18px"
          :columns="mountCols" :data="mounts" :row-key="(m: any) => m.source + m.dest" />
      </template>

      <template v-if="envRows.length">
        <div style="font-weight:600; margin:0 0 8px">环境变量（{{ envRows.length }}）</div>
        <n-data-table size="small" :bordered="false" :single-line="false"
          :columns="envCols" :data="envRows" :row-key="(e: any) => e.k" />
      </template>

      <template v-if="restEntries.length">
        <div style="font-weight:600; margin:0 0 8px">其他</div>
        <div class="term" style="padding:8px 12px"><template v-for="[k, v] in restEntries" :key="k">
{{ k }}: {{ typeof v === 'object' ? JSON.stringify(v) : v }}
</template></div>
      </template>
    </n-drawer-content>
  </n-drawer>
  <FileViewerModal v-model:show="showFileAsBool" :node-i-d="app?.node_id ?? ''" :path="showFile" />
</template>

<style scoped>
.conf-path { cursor: pointer; }
.conf-path:hover { color: #eef1f5; text-decoration: underline; }

/* 中间件配置候选条目:主配置带青色左边条 */
.conf-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 12px;
  border: 1px solid rgba(255, 255, 255, 0.05);
  border-radius: 8px;
  margin-bottom: 5px;
  background: rgba(255, 255, 255, 0.02);
}
.conf-item.main {
  border-color: rgba(238, 241, 245, 0.25);
  background: rgba(238, 241, 245, 0.045);
  box-shadow: inset 3px 0 0 rgba(238, 241, 245, 0.55);
}
</style>
