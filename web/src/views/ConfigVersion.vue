<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import {
  NCard, NSpace, NButton, NTag, NTimeline, NTimelineItem, NModal,
  NDescriptions, NDescriptionsItem, useMessage,
} from 'naive-ui'
import CodeMirror from 'vue-codemirror'
import { api, fmtTime, type ConfigTarget, type ConfigVersion, type Node } from '../api'

const route = useRoute()
const message = useMessage()
const nodeId = route.params.id as string
const targetId = Number(route.params.targetId)

const target = ref<ConfigTarget | null>(null)
const node = ref<Node | null>(null)
const versions = ref<ConfigVersion[]>([])
const editorContent = ref('')

// 编辑状态：当前选中的版本 + 草稿内容
const selected = ref<ConfigVersion | null>(null)
const draftDirty = ref(false)

async function load() {
  try {
    const [nodeResp, targetsResp] = await Promise.all([
      api.get<{ node: Node }>(`/nodes/${nodeId}`),
      api.get<{ data: ConfigTarget[] }>(`/nodes/${nodeId}/configs`),
    ])
    node.value = nodeResp.node
    const t = targetsResp.data?.find((x) => x.id === targetId)
    if (!t) {
      message.error('配置目标不存在')
      return
    }
    target.value = t
    const vResp = await api.get<{ data: ConfigVersion[] }>(`/configs/versions?target_id=${targetId}`)
    versions.value = vResp.data ?? []
    const current = versions.value.find((v) => v.id === t.current_version_id) ?? versions.value[0]
    if (current) select(current)
  } catch (e: any) { message.error(e.message) }
}

function select(v: ConfigVersion) {
  selected.value = v
  editorContent.value = v.content
  draftDirty.value = false
}

const statusTagType = (s: string) => (s === 'applied' ? 'success' : s === 'rolled_back' ? 'info' : 'warning')

const editorLabel = computed(() => {
  if (!selected.value) return ''
  if (draftDirty.value) return `编辑中（基于 v${selected.value.id}）`
  return `v${selected.value.id} · ${selected.value.status === 'applied' ? '生效中' : selected.value.status === 'draft' ? '草稿' : '已回滚'}`
})

// 保存 = 产生新 draft 版本（不可变版本链，docs/06 §2.5）
async function saveDraft() {
  if (!selected.value || !draftDirty.value) return
  try {
    const resp = await api.put<{ version_id: number }>(`/configs/${selected.value.id}`, { content: editorContent.value })
    message.success(`已保存为新版本 v${resp.version_id}`)
    await reloadVersions()
    const created = versions.value.find((v) => v.id === resp.version_id)
    if (created) select(created)
  } catch (e: any) { message.error(e.message) }
}

async function reloadVersions() {
  const vResp = await api.get<{ data: ConfigVersion[] }>(`/configs/versions?target_id=${targetId}`)
  versions.value = vResp.data ?? []
}

// 下发确认：目标节点 + 路径 + 版本（docs/06 §4.2）
const showApply = ref(false)
const applying = ref(false)

async function doApply() {
  if (!selected.value) return
  applying.value = true
  try {
    const resp = await api.post<{ command_id: string }>(`/configs/${selected.value.id}/apply`)
    message.success(`已下发（指令 ${resp.command_id}），阶段结果见指令中心`)
    showApply.value = false
    reloadVersions()
  } catch (e: any) {
    message.error(e.message)
  } finally {
    applying.value = false
  }
}

async function doRollback(v: ConfigVersion) {
  try {
    const resp = await api.post<{ command_id: string }>(`/configs/${v.id}/rollback`)
    message.success(`回滚即下发：v${v.id} 内容已重新走完整下发流程（${resp.command_id}）`)
    reloadVersions()
  } catch (e: any) { message.error(e.message) }
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-title">
      📄 配置管理
      <span class="muted" style="font-size:13px; font-weight:400">
        {{ node?.name }} · <span class="mono">{{ target?.target_path }}</span>
      </span>
      <n-tag v-if="target" size="small" :bordered="false">{{ target.app_type }}</n-tag>
    </div>

    <div style="display:flex; gap:18px; align-items:flex-start;">
      <!-- 左：版本时间线 -->
      <n-card title="版本历史" size="small" style="width:300px; flex-shrink:0" :bordered="false">
        <n-timeline>
          <n-timeline-item v-for="v in versions" :key="v.id"
            :type="v.id === selected?.id ? 'info' : 'default'"
            :title="`v${v.id}`"
            :content="`${v.status} · ${v.created_by} · ${fmtTime(v.created_at)}`">
            <template #default>
              <div style="cursor:pointer; padding:2px 0" @click="select(v)">
                <span :style="{ fontWeight: v.id === selected?.id ? 700 : 400 }">v{{ v.id }}</span>
                <n-tag :type="statusTagType(v.status)" size="tiny" :bordered="false" style="margin-left:6px">{{ v.status }}</n-tag>
                <div class="muted" style="font-size:11.5px; margin-top:2px">{{ fmtTime(v.created_at) }} · {{ v.created_by }}</div>
                <n-space :size="2" style="margin-top:4px">
                  <n-button size="tiny" quaternary type="primary" @click.stop="select(v)">查看</n-button>
                  <n-button v-if="v.status !== 'draft' && v.id !== selected?.id" size="tiny" quaternary type="warning"
                    @click.stop="doRollback(v)">回滚到此</n-button>
                </n-space>
              </div>
            </template>
          </n-timeline-item>
        </n-timeline>
      </n-card>

      <!-- 右：编辑器 -->
      <n-card size="small" style="flex:1; min-width:0" :bordered="false">
        <template #header>
          {{ editorLabel }}
          <n-tag v-if="draftDirty" type="warning" size="tiny" :bordered="false" style="margin-left:8px">未保存</n-tag>
        </template>
        <template #header-extra>
          <n-space :size="8">
            <n-button size="small" :disabled="!draftDirty" @click="saveDraft">保存为新版本</n-button>
            <n-button size="small" type="primary" :disabled="draftDirty || !selected" @click="showApply = true">
              ⬇ 下发
            </n-button>
          </n-space>
        </template>

        <div style="border:1px solid #2a2a33; border-radius:6px; overflow:hidden">
          <code-mirror v-model="editorContent" :height="'520px'"
            :style="{ fontSize: '13px' }"
            @change="draftDirty = true"
            :extensions="[]" :basic-setup="{ lineNumbers: true, highlightActiveLine: true, tabSize: 4 }" />
        </div>

        <n-descriptions v-if="selected" :column="3" size="small" style="margin-top:12px">
          <n-descriptions-item label="校验和"><span class="mono muted" style="font-size:11px">{{ selected.checksum.slice(0, 16) }}…</span></n-descriptions-item>
          <n-descriptions-item label="创建人">{{ selected.created_by }}</n-descriptions-item>
          <n-descriptions-item label="下发指令"><span class="mono muted">{{ selected.command_id || '—' }}</span></n-descriptions-item>
        </n-descriptions>

        <div class="muted" style="font-size:12px; margin-top:8px">
          下发流程：备份 → 原子写入 → 本地校验（{{ target?.app_type === 'nginx' ? 'nginx -t' : 'caddy validate' }}）→ 重载 → 失败自动回滚（docs/01 §6）
        </div>
      </n-card>
    </div>

    <!-- 下发确认弹窗 -->
    <n-modal v-model:show="showApply" preset="dialog" type="warning" title="确认下发配置"
      positive-text="下发" negative-text="取消" :loading="applying" @positive-click="doApply">
      <n-descriptions :column="1" size="small" style="margin-top:8px">
        <n-descriptions-item label="目标节点"><span style="font-weight:600">{{ node?.name }}</span></n-descriptions-item>
        <n-descriptions-item label="目标路径"><span class="mono" style="color:#ffb84d">{{ target?.target_path }}</span></n-descriptions-item>
        <n-descriptions-item label="版本">v{{ selected?.id }}（{{ editorContent.length }} 字节）</n-descriptions-item>
        <n-descriptions-item label="保护机制">校验失败将自动恢复备份并重载旧配置</n-descriptions-item>
      </n-descriptions>
    </n-modal>
  </div>
</template>
