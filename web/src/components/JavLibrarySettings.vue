<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { json, request } from '../api'

type AccountConfig = {
  username: string
  wanted_url: string
  favorites_url: string
  actors_url: string
  cookie_configured: boolean
  last_sync: string
  last_status: string
  counts: Record<string, number>
}
type SyncResult = {
  imported: Record<string, number>
  skipped_duplicates: Record<string, number>
  errors: string[]
  last_sync: string
  complete: boolean
}

const config = reactive({ username: '', wanted_url: '', favorites_url: '', actors_url: '', cookie: '', clear_cookie: false })
const cookieConfigured = ref(false)
const counts = ref<Record<string, number>>({ wanted: 0, favorites: 0, actors: 0 })
const lastSync = ref('')
const lastStatus = ref('')
const selected = reactive({ wanted: true, favorites: true, actors: true })
const loading = ref(false)
const saving = ref(false)
const syncing = ref(false)
const error = ref('')
const notice = ref('')
const syncResult = ref<SyncResult | null>(null)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const value = await request<AccountConfig>('/accounts/javlibrary')
    config.username = value.username || ''
    config.wanted_url = value.wanted_url || ''
    config.favorites_url = value.favorites_url || ''
    config.actors_url = value.actors_url || ''
    cookieConfigured.value = !!value.cookie_configured
    counts.value = value.counts || { wanted: 0, favorites: 0, actors: 0 }
    lastSync.value = value.last_sync || ''
    lastStatus.value = value.last_status || ''
  } catch (e: any) {
    error.value = e.message || String(e)
  } finally {
    loading.value = false
  }
}

async function save() {
  saving.value = true
  error.value = ''
  notice.value = ''
  try {
    const payload: Record<string, unknown> = {
      username: config.username,
      wanted_url: config.wanted_url,
      favorites_url: config.favorites_url,
      actors_url: config.actors_url,
      clear_cookie: config.clear_cookie,
    }
    if (config.cookie.trim()) payload.cookie = config.cookie
    const value = await request<AccountConfig>('/accounts/javlibrary', json('PUT', payload))
    cookieConfigured.value = !!value.cookie_configured
    config.cookie = ''
    config.clear_cookie = false
    notice.value = '账号导入设置已保存。Cookie 不会回显。'
    return true
  } catch (e: any) {
    error.value = e.message || String(e)
    return false
  } finally {
    saving.value = false
  }
}

async function sync() {
  const kinds = (Object.keys(selected) as Array<keyof typeof selected>).filter((kind) => selected[kind])
  if (!kinds.length) {
    error.value = '请至少选择一种导入内容。'
    return
  }
  syncing.value = true
  error.value = ''
  notice.value = ''
  syncResult.value = null
  try {
    if (!await save()) return
    const result = await request<SyncResult>('/accounts/javlibrary/sync', json('POST', { kinds }))
    syncResult.value = result
    counts.value = result.imported || counts.value
    lastSync.value = result.last_sync || ''
    lastStatus.value = result.complete ? 'complete' : 'partial'
    if (result.complete) notice.value = '导入完成；作品已进入候选列表待你审核。'
    else if (!result.imported?.wanted && !result.imported?.favorites && !result.imported?.actors) error.value = '没有导入内容，请查看下方错误信息。'
    emit('synced')
    await load()
  } catch (e: any) {
    error.value = e.message || String(e)
  } finally {
    syncing.value = false
  }
}

function formatDate(value: string) {
  if (!value) return '尚未同步'
  const date = new Date(value)
  return Number.isNaN(date.valueOf()) ? value : date.toLocaleString()
}

const emit = defineEmits<{ synced: [] }>()
onMounted(load)
</script>

<template>
  <section class="settings-card" aria-labelledby="javlibrary-settings-title">
    <header class="settings-heading">
      <div>
        <h2 id="javlibrary-settings-title" class="settings-title">JavLibrary 账号导入</h2>
        <p class="settings-description">读取你粘贴的清单页面，将作品作为待审核候选，并导入收藏演员标记。</p>
      </div>
      <span v-if="loading" class="muted" role="status">正在读取…</span>
    </header>

    <div class="field-grid">
      <label class="field"><span>用户名（备注）</span><input v-model.trim="config.username" autocomplete="username" :disabled="loading || saving || syncing" /></label>
      <label class="field"><span>想看清单 URL</span><input v-model.trim="config.wanted_url" type="url" placeholder="粘贴你账号中想看清单的完整地址" :disabled="loading || saving || syncing" /><small class="form-hint">站点用户清单地址由你从浏览器复制；不会根据用户名猜地址。</small></label>
      <label class="field"><span>收藏作品清单 URL</span><input v-model.trim="config.favorites_url" type="url" placeholder="粘贴你账号中收藏作品清单的完整地址" :disabled="loading || saving || syncing" /><small class="form-hint">把目标页面地址粘贴到这里。服务端只接受当前配置 JavLibrary 站点的同源 URL。</small></label>
      <label class="field"><span>收藏演员清单 URL</span><input v-model.trim="config.actors_url" type="url" placeholder="粘贴你账号中收藏演员页面的完整地址" :disabled="loading || saving || syncing" /><small class="form-hint">演员收藏页地址也请从登录后的浏览器复制。</small></label>
      <label class="field"><span>Cookie {{ cookieConfigured ? '（已保存）' : '' }}</span><input v-model="config.cookie" type="password" autocomplete="new-password" placeholder="仅在需要登录时填写；保存后不会再次显示" :disabled="loading || saving || syncing || config.clear_cookie" /></label>
    </div>
    <label v-if="cookieConfigured" class="settings-toggle"><input v-model="config.clear_cookie" type="checkbox" :disabled="loading || saving || syncing" />清除已保存的 Cookie</label>

    <div class="sync-kinds" aria-label="选择导入内容">
      <span class="sync-kinds-label">导入内容</span>
      <label><input v-model="selected.wanted" type="checkbox" :disabled="syncing" />想看作品</label>
      <label><input v-model="selected.favorites" type="checkbox" :disabled="syncing" />收藏作品</label>
      <label><input v-model="selected.actors" type="checkbox" :disabled="syncing" />收藏演员</label>
    </div>

    <div class="account-note">
      <p>这是只读导入：作品仅进入候选列表，不会下载或标记为已拥有。</p>
      <p>本地收藏演员反向写入 JavLibrary 尚未连接；目前不会向网站提交更改。</p>
      <p v-if="lastSync">上次同步：{{ formatDate(lastSync) }} · {{ lastStatus || '未知状态' }} · 新导入 {{ counts.wanted || 0 }} 个想看、{{ counts.favorites || 0 }} 个收藏作品、{{ counts.actors || 0 }} 位演员</p>
    </div>

    <p v-if="error" class="form-error" role="alert">{{ error }}</p>
    <p v-else-if="notice" class="settings-status" role="status">{{ notice }}</p>
    <div v-if="syncResult" class="sync-result" aria-live="polite">
      <strong>{{ syncResult.complete ? '导入完成' : '导入未完整完成' }}</strong>
      <span>新导入：想看 {{ syncResult.imported?.wanted || 0 }}、收藏作品 {{ syncResult.imported?.favorites || 0 }}、演员 {{ syncResult.imported?.actors || 0 }}</span>
      <span>跳过重复：本地作品 {{ syncResult.skipped_duplicates?.local_works || 0 }}、已有候选 {{ syncResult.skipped_duplicates?.existing_candidates || 0 }}、已收藏演员 {{ syncResult.skipped_duplicates?.already_favorite_actors || 0 }}</span>
      <ul v-if="syncResult.errors?.length"><li v-for="item in syncResult.errors" :key="item">{{ item }}</li></ul>
    </div>
    <div class="settings-actions">
      <button class="btn secondary" :disabled="loading || saving || syncing" @click="save">{{ saving ? '正在保存…' : '保存账号设置' }}</button>
      <button class="btn" :disabled="loading || saving || syncing" @click="sync">{{ syncing ? '正在读取清单…' : '手动同步所选内容' }}</button>
    </div>
  </section>
</template>

<style scoped>
.settings-card{display:grid;gap:16px;padding:20px;border:1px solid var(--line,#292a30);border-radius:10px;background:var(--panel,#17181c);color:var(--white,#f3f4f6)}
.settings-heading{display:flex;align-items:flex-start;justify-content:space-between;gap:12px}.settings-title{margin:0;font-size:17px}.settings-description{margin:6px 0 0;color:var(--muted,#858690);line-height:1.5}
.field-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:13px}.field{display:grid;align-content:start;gap:7px;min-width:0}.field>span{font-weight:600}.field input{width:100%;min-width:0;border:1px solid var(--line,#292a30);border-radius:7px;background:var(--bg,#101114);color:inherit;padding:10px 11px}.form-hint{color:var(--muted,#858690);line-height:1.5}
.settings-toggle,.sync-kinds,.sync-kinds label{display:flex;align-items:center;gap:9px}.settings-toggle input,.sync-kinds input{accent-color:var(--accent,#cfef72)}.sync-kinds{flex-wrap:wrap;gap:14px}.sync-kinds-label{font-weight:600}.account-note{border:1px solid var(--line,#292a30);border-radius:8px;padding:11px 13px;color:var(--muted,#858690);line-height:1.55}.account-note p{margin:0}.account-note p+p{margin-top:5px}
.settings-actions{display:flex;flex-wrap:wrap;gap:9px}.settings-status{margin:0;color:#94d6a3}.form-error{margin:0}.sync-result{display:grid;gap:5px;border-left:3px solid var(--accent,#cfef72);padding:2px 0 2px 12px}.sync-result ul{margin:3px 0 0;padding-left:20px;color:#ff9b9f}.sync-result span{color:var(--muted,#858690);line-height:1.5}
@media(max-width:700px){.field-grid{grid-template-columns:1fr}}
</style>
