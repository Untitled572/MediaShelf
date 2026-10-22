<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { json, request } from '../api'

type ProxySettings = { proxy_enabled: boolean; proxy_url: string }
const settings = ref<ProxySettings>({ proxy_enabled: true, proxy_url: 'http://127.0.0.1:7890' })
const saved = ref<ProxySettings>({ ...settings.value })
const loading = ref(false)
const saving = ref(false)
const testing = ref(false)
const error = ref('')
const message = ref('')
const dirty = computed(() => settings.value.proxy_enabled !== saved.value.proxy_enabled || settings.value.proxy_url !== saved.value.proxy_url)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const value = await request<any>('/settings')
    settings.value = { proxy_enabled: value.proxy_enabled !== false, proxy_url: value.proxy_url || 'http://127.0.0.1:7890' }
    saved.value = { ...settings.value }
  } catch (e: any) {
    error.value = e.message || String(e)
  } finally {
    loading.value = false
  }
}

async function save() {
  saving.value = true
  error.value = ''
  message.value = ''
  try {
    const value = await request<any>('/settings', json('PATCH', settings.value))
    settings.value = { proxy_enabled: value.proxy_enabled, proxy_url: value.proxy_url }
    saved.value = { ...settings.value }
    message.value = '代理设置已保存。'
    return true
  } catch (e: any) {
    error.value = e.message || String(e)
    return false
  } finally {
    saving.value = false
  }
}

async function testProxy() {
  error.value = ''
  message.value = ''
  testing.value = true
  try {
    if (dirty.value) {
      if (!await save()) return
      message.value = '设置已先保存；正在测试代理…'
    }
    await request('/settings/proxy/test', json('POST', {}))
    message.value = '代理连接测试通过（Wikidata API）。'
  } catch (e: any) {
    error.value = e.message || String(e)
  } finally {
    testing.value = false
  }
}

onMounted(load)
</script>

<template>
  <section class="settings-card" aria-labelledby="network-settings-title">
    <header class="settings-heading">
      <div>
        <h2 id="network-settings-title" class="settings-title">网络代理</h2>
        <p class="settings-description">公开目录、演员资料、RSS 与在线图片使用此代理；设备和 qBittorrent 直连。</p>
      </div>
      <span v-if="loading" class="muted" role="status">正在读取…</span>
    </header>

    <label class="settings-toggle">
      <input v-model="settings.proxy_enabled" type="checkbox" :disabled="loading || saving || testing" />
      <span>启用全局代理</span>
    </label>
    <label class="field">
      <span>代理地址</span>
      <input v-model.trim="settings.proxy_url" type="url" placeholder="http://127.0.0.1:7890" autocomplete="off" :disabled="loading || saving || testing" />
      <small class="form-hint">填写本机可访问的 HTTP(S) 代理地址，不包含账号密码。</small>
    </label>

    <p v-if="error" class="form-error" role="alert">{{ error }}</p>
    <p v-else-if="message" class="settings-status" role="status">{{ message }}</p>
    <div class="settings-actions">
      <button class="btn" :disabled="loading || saving || testing || !dirty" @click="save">{{ saving ? '正在保存…' : '保存设置' }}</button>
      <button class="btn secondary" :disabled="loading || saving || testing || !settings.proxy_enabled" @click="testProxy">{{ testing ? '正在测试…' : '测试代理' }}</button>
    </div>
  </section>
</template>

<style scoped>
.settings-card{display:grid;gap:16px;padding:20px;border:1px solid var(--line,#292a30);border-radius:10px;background:var(--panel,#17181c);color:var(--white,#f3f4f6)}
.settings-heading{display:flex;align-items:flex-start;justify-content:space-between;gap:12px}.settings-title{margin:0;font-size:17px}.settings-description{margin:6px 0 0;color:var(--muted,#858690);line-height:1.5}
.field{display:grid;gap:7px}.field>span{font-weight:600}.field input{width:100%;min-width:0;border:1px solid var(--line,#292a30);border-radius:7px;background:var(--bg,#101114);color:inherit;padding:0 11px}
.form-hint{color:var(--muted,#858690);line-height:1.5}.settings-toggle{display:flex;align-items:center;gap:9px}.settings-toggle input{accent-color:var(--accent,#cfef72)}
.settings-actions{display:flex;flex-wrap:wrap;gap:9px}.settings-status{margin:0;color:#94d6a3}.form-error{margin:0}
</style>
