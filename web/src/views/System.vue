<script setup lang="ts">
import { onMounted, ref } from 'vue'
import {
  NCard, NSpace, NInput, NButton, NModal, NForm, NFormItem,
  useMessage,
} from 'naive-ui'
import { api } from '../api'

const message = useMessage()
const info = ref<{ panel_version: string; node_total: number; node_online: number; command_total: number } | null>(null)

// 修改密码
const showPwd = ref(false)
const oldPwd = ref('')
const newPwd = ref('')
const newPwd2 = ref('')

async function load() {
  try {
    info.value = await api.get('/system/info')
  } catch (e: any) { message.error(e.message) }
}

async function changePassword() {
  if (newPwd.value.length < 10) {
    message.error('新密码最短 10 位')
    return
  }
  if (newPwd.value !== newPwd2.value) {
    message.error('两次输入不一致')
    return
  }
  try {
    await api.put('/auth/password', { old_password: oldPwd.value, new_password: newPwd.value })
    message.success('密码已修改；其他设备的会话已全部失效')
    showPwd.value = false
    oldPwd.value = newPwd.value = newPwd2.value = ''
  } catch (e: any) { message.error(e.message) }
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div style="display:flex; align-items:flex-start; justify-content:space-between">
      <div>
        <div class="page-title">系统</div>
        <div class="page-sub">面板信息 · 账户安全 · <span style="color:#63d3a6">审计已独立成菜单</span></div>
      </div>
      <n-button size="small" @click="showPwd = true">修改密码</n-button>
    </div>

    <n-space :size="16" style="margin-bottom:18px">
      <n-card title="面板" size="small" style="width:240px">
        <div class="mono" style="font-size:18px; font-weight:700">{{ info?.panel_version ?? '…' }}</div>
        <div class="muted" style="font-size:12px; margin-top:4px">单二进制 · SQLite · 前端内嵌</div>
      </n-card>
      <n-card title="节点" size="small" style="width:240px">
        <div style="font-size:18px; font-weight:700">
          {{ info?.node_online ?? 0 }} <span class="muted" style="font-size:13px; font-weight:400">/ {{ info?.node_total ?? 0 }} 在线</span>
        </div>
      </n-card>
      <n-card title="累计指令" size="small" style="width:240px">
        <div style="font-size:18px; font-weight:700">{{ info?.command_total ?? 0 }}</div>
        <div class="muted" style="font-size:12px">审计不可删除（docs/05 §3.3）</div>
      </n-card>
    </n-space>


    <n-modal v-model:show="showPwd" preset="dialog" title="修改密码" positive-text="修改" negative-text="取消" @positive-click="changePassword">
      <n-form label-placement="top" style="margin-top:10px">
        <n-form-item label="原密码"><n-input v-model:value="oldPwd" type="password" show-password-on="click" /></n-form-item>
        <n-form-item label="新密码（≥10 位）"><n-input v-model:value="newPwd" type="password" show-password-on="click" /></n-form-item>
        <n-form-item label="重复新密码"><n-input v-model:value="newPwd2" type="password" show-password-on="click" /></n-form-item>
      </n-form>
      <div class="muted" style="font-size:12px">修改成功后其他设备的会话将全部失效</div>
    </n-modal>

  </div>
</template>
