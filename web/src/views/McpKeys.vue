<script setup lang="ts">
// MCP Keys 配置页(docs/07 §5 + 两级授权细化):
//   节点→应用 树形多选(cascade:勾节点=全部应用,可下钻勾部分应用)。
//   Key 支持创建 + 编辑(改名/scope/范围/启停;key 值不可改)。
import { computed, h, onMounted, ref } from 'vue'
import {
  NDataTable, NButton, NTag, NModal, NInput, NCheckbox, NCheckboxGroup, NSpace,
  NPopconfirm, NTree, useMessage,
  type DataTableColumns, type TreeOption,
} from 'naive-ui'
import { api, fmtAgo, type Node, type AppRow, type Page } from '../api'

const message = useMessage()

interface McpKey {
  id: number; name: string; scopes: string[]; node_scope: string;
  enabled: boolean; last_used: number; created_at: number
}
const keys = ref<McpKey[]>([])
const nodes = ref<Node[]>([])
const apps = ref<AppRow[]>([])

const scopeMeta: Record<string, { label: string; type: string; desc: string }> = {
  read: { label: '只读', type: 'success', desc: '查看节点列表、在线状态、CPU/内存等指标、应用清单、面板信息——纯查询,无任何变更。' },
  logs: { label: '日志', type: 'info', desc: '读取 Docker 容器日志尾部(默认 100 行,上限 500)。注意:日志中可能包含应用打印的密钥等敏感内容。' },
  'container-ops': { label: '容器操作', type: 'warning', desc: '对 Docker 容器执行 启动/停止/重启/拉取镜像(白名单,无删除)。影响容器可用性,但不会丢配置。' },
  files: { label: '文件', type: 'error', desc: '列出目录、读取与改写节点文件(文本配置白名单,≤512KB,虚拟系统目录保护)。等同授权 AI 触达节点文件系统。' },
  exec: { label: '命令执行', type: 'error', desc: '在节点上执行任意 shell 命令并获取输出——等同把节点 shell 交给 AI,是最高危能力,强烈建议仅绑定测试节点。' },
}
const scopeOptions = Object.entries(scopeMeta).map(([k, v]) => ({ label: v.label, value: k }))

async function load() {
  const data = await api.get<{ data: McpKey[] }>('/mcp/keys')
  keys.value = data.data ?? []
}
async function loadNodes() {
  const d = await api.get<Page<Node>>('/nodes?page_size=100')
  nodes.value = d.data ?? []
}
async function loadApps() {
  const d = await api.get<Page<AppRow>>('/apps?page_size=500')
  apps.value = d.data ?? []
}

// ---------- 两级范围:树选项(节点→docker 应用) ----------

const scopeTree = computed<TreeOption[]>(() => {
  const byNode = new Map<string, TreeOption[]>()
  for (const a of apps.value) {
    if (a.probe_name !== 'docker') continue
    const list = byNode.get(a.node_id) ?? []
    list.push({ key: a.node_id + '::' + a.app_name, label: a.app_name, isLeaf: true })
    byNode.set(a.node_id, list)
  }
  return nodes.value.map((n) => ({
    key: n.id, label: n.name, children: byNode.get(n.id) ?? [],
  }))
})

// 选中的 key 集合 → {mode, nodes[]} 序列化。
// 节点自身 key 选中 = 全部应用(apps 省略);仅应用选中 = 列出应用。
const checkedKeys = ref<string[]>([])
function serializeScope(): string {
  const nodesMap = new Map<string, { id: string; apps: string[]; all: boolean }>()
  for (const k of checkedKeys.value) {
    if (!k.includes('::')) {
      nodesMap.set(k, { id: k, apps: [], all: true })
    }
  }
  for (const k of checkedKeys.value) {
    if (k.includes('::')) {
      const [nid, app] = k.split('::')
      let e = nodesMap.get(nid)
      if (!e) { e = { id: nid, apps: [], all: false }; nodesMap.set(nid, e) }
      if (!e.all) e.apps.push(app)
    }
  }
  return JSON.stringify({ mode: 'custom', nodes: [...nodesMap.values()].map((e) => ({ id: e.id, ...(e.all ? {} : { apps: e.apps }) })) })
}

// 反序列化(node_scope JSON → checked keys;兼容旧格式)。
function deserializeScope(raw: string): string[] {
  if (raw === 'all' || raw === '') return []
  try {
    const parsed = JSON.parse(raw)
    if (Array.isArray(parsed)) return parsed // 旧格式:节点全选
    const out: string[] = []
    for (const n of parsed.nodes ?? []) {
      out.push(n.id)
      for (const a of n.apps ?? []) out.push(n.id + '::' + a)
    }
    return out
  } catch { return [] }
}

function scopeLabel(k: McpKey): string {
  if (k.node_scope === 'all') return '全部节点'
  try {
    const p = JSON.parse(k.node_scope)
    if (Array.isArray(p)) return p.length + ' 个节点'
    const total = p.nodes?.length ?? 0
    const appNodes = (p.nodes ?? []).filter((n: any) => n.apps?.length).length
    return total + ' 个节点' + (appNodes ? '(含应用级 ' + appNodes + ')' : '')
  } catch { return '—' }
}

// ---------- 表单(创建/编辑共用) ----------

const showForm = ref(false)
const formMode = ref<'create' | 'edit'>('create')
const editingId = ref<number | null>(null)
const fName = ref('')
const fScopes = ref<string[]>([])
const fScopeAll = ref('all')
const showExecConfirm = ref(false)
const execConfirmText = ref('')
const showPlaintext = ref(false)
const plaintext = ref('')

function wantSubmit() {
  if (fScopes.value.includes('exec') && formMode.value === 'create') { showExecConfirm.value = true; return }
  doSubmit()
}
async function doSubmit() {
  showExecConfirm.value = false
  const ns = fScopeAll.value === 'all' ? 'all' : serializeScope()
  try {
    if (formMode.value === 'create') {
      const resp = await api.post<{ key: string }>('/mcp/keys', { name: fName.value, scopes: fScopes.value, node_scope: ns })
      plaintext.value = resp.key
      showPlaintext.value = true
    } else {
      await api.patch(`/mcp/keys/${editingId.value}`, { name: fName.value, scopes: fScopes.value, node_scope: ns })
      message.success('Key 权限已更新(即时生效)')
    }
    showForm.value = false
    load()
  } catch (e: any) { message.error(e.message) }
}

function openCreate() {
  formMode.value = 'create'
  editingId.value = null
  fName.value = ''
  fScopes.value = []
  fScopeAll.value = 'all'
  checkedKeys.value = []
  showForm.value = true
}

function openEdit(k: McpKey) {
  formMode.value = 'edit'
  editingId.value = k.id
  fName.value = k.name
  fScopes.value = [...k.scopes]
  if (k.node_scope === 'all') { fScopeAll.value = 'all'; checkedKeys.value = [] }
  else { fScopeAll.value = 'sel'; checkedKeys.value = deserializeScope(k.node_scope) }
  showForm.value = true
}

async function toggleKey(k: McpKey) {
  try { await api.patch(`/mcp/keys/${k.id}`, { enabled: !k.enabled }); load() }
  catch (e: any) { message.error(e.message) }
}
async function delKey(k: McpKey) {
  try {
    await api.delete(`/mcp/keys/${k.id}`)
    message.success('已吊销(即时生效)')
    load()
  } catch (e: any) { message.error(e.message) }
}

function scopeTags(scopes: string[]) {
  return scopes.map((s) => h(NTag, { size: 'tiny', bordered: false, type: (scopeMeta[s]?.type ?? 'default') as any },
    { default: () => scopeMeta[s]?.label ?? s }))
}

const columns: DataTableColumns<McpKey> = [
  { title: '名称', key: 'name', width: 140, render: (k) => h('span', { style: 'font-weight:600' }, k.name) },
  {
    title: 'Scope', key: 'scopes', width: 220,
    render: (k) => h(NSpace, { size: 4 }, { default: () => scopeTags(k.scopes) }),
  },
  { title: '节点范围', key: 'node_scope', width: 170, render: (k) => h('span', { class: 'muted', style: 'font-size:12px' }, scopeLabel(k)) },
  { title: '最后使用', key: 'last_used', width: 110, render: (k) => h('span', { class: 'muted' }, k.last_used ? fmtAgo(k.last_used) : '从未') },
  {
    title: '状态', key: 'enabled', width: 75,
    render: (k) => h(NTag, { size: 'tiny', round: true, bordered: false, type: k.enabled ? 'success' : 'default' },
      { default: () => k.enabled ? '启用' : '停用' }),
  },
  {
    title: '', key: 'op', width: 150,
    render: (k) => h(NSpace, { size: 4 }, { default: () => [
      h(NButton, { size: 'tiny', quaternary: true, type: 'primary', onClick: () => openEdit(k) }, { default: () => '编辑' }),
      h(NButton, { size: 'tiny', quaternary: true, onClick: () => toggleKey(k) }, { default: () => (k.enabled ? '停用' : '启用') }),
      h(NPopconfirm, { onPositiveClick: () => delKey(k) }, {
        trigger: () => h(NButton, { size: 'tiny', quaternary: true, type: 'error' }, { default: () => '吊销' }),
        default: () => `吊销后使用该 Key 的 AI 客户端立即失联(审计保留)。确定吊销「${k.name}」?`,
      }),
    ] }),
  },
]

function copyPlaintext() {
  navigator.clipboard.writeText(plaintext.value).then(() => message.success('已复制'))
}

// 接入信息卡:端点一键复制
const mcpEndpoint = `${location.origin}/mcp`
function copyEndpoint() {
  navigator.clipboard.writeText(mcpEndpoint).then(() => message.success('端点地址已复制'))
}

onMounted(() => { load(); loadNodes(); loadApps() })
</script>

<template>
  <div class="page">
    <div style="display:flex; align-items:flex-start; justify-content:space-between">
      <div>
        <div class="page-title">MCP Keys</div>
        <div class="page-sub">让 AI 客户端安全地操控牧云 · 能力(scope)决定"能做什么",授权范围决定"对哪些节点/应用做" · 随时可编辑</div>
      </div>
      <n-button type="primary" size="small" class="glow-hover" @click="openCreate">＋ 创建 Key</n-button>
    </div>

    <!-- 接入信息:常驻描述区,置于列表上方(放底部会被长列表挤压) -->
    <div class="usage-card">
      <div class="usage-endpoint">
        <div class="usage-label">Streamable HTTP 端点</div>
        <div class="usage-url mono" :title="mcpEndpoint">{{ mcpEndpoint }}</div>
        <n-button size="tiny" quaternary type="primary" @click="copyEndpoint">复制</n-button>
      </div>
      <div class="usage-steps">
        <div class="usage-label">接入三步</div>
        <div class="usage-step"><b>1</b> 创建 Key 并复制明文(仅显示一次)</div>
        <div class="usage-step"><b>2</b><span>客户端配置 HTTP MCP,<span class="mono">Header: Authorization: Bearer mcp_xxx…</span></span></div>
        <div class="usage-step"><b>3</b> 对 AI 说"列一下节点"——范围外的请求会被拒绝并记录审计</div>
      </div>
    </div>

    <n-data-table :columns="columns" :data="keys" :bordered="false" size="small" />

    <!-- 创建/编辑表单 -->
    <n-modal v-model:show="showForm" preset="card" :title="formMode === 'create' ? '创建 MCP Key' : '编辑 Key 权限'" style="width:620px">
      <n-space vertical :size="12">
        <div>
          <div class="muted" style="font-size:12px; margin-bottom:6px">名称(审计中显示)</div>
          <n-input v-model:value="fName" placeholder="my-ai-assistant" :maxlength="32" />
        </div>
        <div>
          <div class="muted" style="font-size:12px; margin-bottom:6px">能力(可执行哪类操作)</div>
          <n-checkbox-group v-model:value="fScopes">
            <n-space>
              <n-checkbox v-for="o in scopeOptions" :key="o.value" :value="o.value" :label="o.label" />
            </n-space>
          </n-checkbox-group>
          <div v-if="fScopes.length" class="scope-desc">
            <div v-for="sc in fScopes" :key="sc" class="scope-desc-item">
              <n-tag size="tiny" :bordered="false" :type="(scopeMeta[sc]?.type ?? 'default') as any">{{ scopeMeta[sc]?.label }}</n-tag>
              <span>{{ scopeMeta[sc]?.desc }}</span>
            </div>
          </div>
          <div v-else class="muted" style="font-size:12px; margin-top:8px">未勾选任何能力时该 Key 无法调用任何工具</div>
          <div v-if="fScopes.includes('exec')" style="color:#ef9d93; font-size:12px; margin-top:6px">
            ⚠ 命令执行 = 把节点 shell 交给 AI,建议仅绑测试节点
          </div>
          <div v-if="fScopes.includes('files')" style="color:#e3c47f; font-size:12px; margin-top:4px">
            ⚠ 文件读写可触达节点文件系统
          </div>
        </div>
        <div>
          <div class="muted" style="font-size:12px; margin-bottom:6px">授权范围(节点 → 应用,两级可下钻)</div>
          <n-radio-group v-model:value="fScopeAll" size="small">
            <n-radio-button value="all" label="全部节点" />
            <n-radio-button value="sel" label="自定义" />
          </n-radio-group>
          <div v-if="fScopeAll === 'sel'" style="margin-top:8px">
            <div class="scope-tree inset-panel">
              <n-tree block-line checkable cascade default-expand-all
                :data="scopeTree" v-model:checked-keys="checkedKeys"
                key-field="key" label-field="label" children-field="children" />
            </div>
            <div class="muted" style="font-size:11.5px; margin-top:6px">
              勾节点 = 该节点全部应用;展开后仅勾应用 = 只授权这些应用(容器操作/日志按应用校验;命令执行/文件按节点校验)
            </div>
          </div>
        </div>
      </n-space>
      <template #footer>
        <n-space justify="end">
          <n-button size="small" @click="showForm = false">取消</n-button>
          <n-button size="small" type="primary" :disabled="!fName || fScopes.length === 0 || (fScopeAll === 'sel' && checkedKeys.length === 0)" @click="wantSubmit">
            {{ formMode === 'create' ? '创建' : '保存修改' }}
          </n-button>
        </n-space>
      </template>
    </n-modal>

    <!-- exec 二次确认 -->
    <n-modal v-model:show="showExecConfirm" preset="dialog" type="error" title="高危授权确认">
      <p>你正在授予 <b>命令执行</b> 权限——等同把节点 shell 交给 AI。</p>
      <p class="muted" style="font-size:12px">输入 Key 名称 <b>{{ fName }}</b> 以确认:</p>
      <n-input v-model:value="execConfirmText" />
      <template #action>
        <n-button size="small" @click="showExecConfirm = false">取消</n-button>
        <n-button size="small" type="error" :disabled="execConfirmText !== fName" @click="doSubmit">我已知晓风险</n-button>
      </template>
    </n-modal>

    <!-- 一次性明文 -->
    <n-modal v-model:show="showPlaintext" preset="dialog" title="Key 已创建(仅此一次显示)">
      <div class="muted" style="font-size:12px; margin-bottom:8px">关闭后无法再查看,请立即复制到你的 MCP 客户端:</div>
      <pre class="term" style="margin:0; user-select:all">{{ plaintext }}</pre>
      <template #action>
        <n-button size="small" type="primary" @click="copyPlaintext">复制</n-button>
        <n-button size="small" quaternary @click="showPlaintext = false">我已保存,关闭</n-button>
      </template>
    </n-modal>
  </div>
</template>

<style scoped>
.scope-desc {
  margin-top: 8px;
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 10px 12px;
  background: #0b0c11;
  border: 1px solid #1c2130;
  border-radius: 8px;
  box-shadow: inset 0 1px 6px rgba(0, 0, 0, 0.35);
}
.scope-desc-item {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  font-size: 12.5px;
  color: #a4abb8;
  line-height: 1.6;
}
.scope-desc-item .n-tag { flex-shrink: 0; margin-top: 1px; }
.scope-tree {
  max-height: 300px;
  overflow: auto;
  padding: 8px 6px;
  background: #0b0c11;                 /* 比卡片底更深:下凹第一步 */
  border-radius: 8px;
  border: 1px solid #1c2130;
  box-shadow: inset 0 2px 8px rgba(0, 0, 0, 0.45);  /* 内阴影=凹陷 */
}
.crumb-select { min-width: 100%; }

/* 接入信息卡:列表上方常驻,横向双栏 */
.usage-card {
  display: flex;
  gap: 28px;
  align-items: stretch;
  margin-bottom: 16px;
  padding: 14px 18px;
  background: #15181d;
  border: 1px solid #23272e;
  border-radius: 12px;
}
.usage-endpoint {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-shrink: 0;
  padding-right: 28px;
  border-right: 1px solid #23272e;
}
.usage-label {
  font-size: 11px;
  color: #676e7a;
  letter-spacing: 0.5px;
  margin-bottom: 4px;
}
.usage-endpoint .usage-label { margin-bottom: 0; flex-shrink: 0; }
.usage-url {
  font-size: 12.5px;
  color: #eef1f5;
  background: #0a0b0d;
  border: 1px solid #23272e;
  border-radius: 8px;
  padding: 5px 12px;
  max-width: 380px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.usage-steps { flex: 1; min-width: 0; }
.usage-step {
  font-size: 12.5px;
  color: #a4abb8;
  line-height: 1.9;
  display: flex;
  align-items: baseline;
  gap: 7px;
}
.usage-step b {
  color: #eef1f5;
  font-size: 11px;
  width: 16px; height: 16px;
  border-radius: 50%;
  background: #24282f;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  transform: translateY(1px);
}
.usage-step .mono { font-size: 11.5px; color: #676e7a; }
</style>
