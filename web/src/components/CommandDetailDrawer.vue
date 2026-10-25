<script setup lang="ts">
// 指令详情抽屉（共享组件）：指令中心 + 节点详情·指令 tab 复用。
// 打开时自行拉取完整指令（列表接口不含输出正文）。
import { ref, watch } from 'vue'
import {
  NDrawer, NDrawerContent, NDescriptions, NDescriptionsItem, NTag, NTimeline, NTimelineItem,
} from 'naive-ui'
import { api, fmtTime, type Command } from '../api'

const props = defineProps<{ show: boolean; commandId: string | null }>()
const emit = defineEmits<{ (e: 'update:show', v: boolean): void }>()

const detail = ref<Command | null>(null)
const loading = ref(false)

watch(() => props.show, async (open) => {
  if (open && props.commandId) {
    loading.value = true
    detail.value = null
    try {
      detail.value = await api.get<Command>(`/commands/${props.commandId}`)
    } finally {
      loading.value = false
    }
  }
})
</script>

<template>
  <n-drawer :show="show" :width="620" placement="right" @update:show="emit('update:show', $event)">
    <n-drawer-content :title="`指令 ${detail?.type ?? ''}`" closable>
      <div v-if="loading" class="muted">加载中…</div>
      <template v-else-if="detail">
        <n-descriptions :column="2" size="small" style="margin-bottom:14px">
          <n-descriptions-item label="状态">
            <n-tag size="tiny" round :bordered="false">{{ detail.status }}</n-tag>
          </n-descriptions-item>
          <n-descriptions-item label="退出码">{{ detail.exit_code ?? '—' }}</n-descriptions-item>
          <n-descriptions-item label="发起人">{{ detail.created_by }}</n-descriptions-item>
          <n-descriptions-item label="创建时间">{{ fmtTime(detail.created_at) }}</n-descriptions-item>
          <n-descriptions-item v-if="detail.payload" label="内容" :span="2">
            <span class="mono" style="font-size:12px; word-break:break-all">
              {{ detail.type === 'exec' ? (detail.payload?.cmd ?? '') : detail.type === 'config.apply' ? (detail.payload?.target_path ?? '') : JSON.stringify(detail.payload) }}
            </span>
          </n-descriptions-item>
        </n-descriptions>

        <template v-if="detail.stages?.length">
          <div style="font-weight:600; margin:10px 0">下发阶段</div>
          <n-timeline>
            <n-timeline-item v-for="s in detail.stages" :key="s.name"
              :type="s.status === 'success' ? 'success' : s.status === 'failed' ? 'error' : 'default'"
              :title="s.name" :content="s.detail" />
          </n-timeline>
        </template>

        <div v-if="detail.error" style="color:#ff8f8f; margin:12px 0 6px; font-weight:600">错误</div>
        <pre v-if="detail.error" class="term out-err">{{ detail.error }}</pre>

        <div style="font-weight:600; margin:12px 0 6px">stdout
          <span v-if="detail.truncated" class="muted" style="font-weight:400">（已截断）</span>
        </div>
        <pre class="term">{{ detail.stdout || '（空）' }}</pre>

        <div v-if="detail.stderr" style="font-weight:600; margin:12px 0 6px">stderr</div>
        <pre v-if="detail.stderr" class="term out-err">{{ detail.stderr }}</pre>
      </template>
    </n-drawer-content>
  </n-drawer>
</template>
