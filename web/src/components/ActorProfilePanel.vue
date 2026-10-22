<script setup lang="ts">
import {ref,watch} from 'vue'
import {request,json} from '../api'
import ActorAvatar from './ActorAvatar.vue'
const props=defineProps<{name:string;privacy:boolean;avatar?:string}>()
const profile=ref<any>({}),loading=ref(false),error=ref('')
let generation=0
function linkLabel(label:string){return ({'Official website':'官方网站',Wikipedia:'维基百科',Wikidata:'维基数据'} as Record<string,string>)[label]||label}
function safeUrl(raw:string){try{const u=new URL(raw);return ['http:','https:'].includes(u.protocol)&&!u.username&&!u.password?u.href:''}catch{return ''}}
async function load(refresh=false){
 const id=++generation,name=props.name;loading.value=true;error.value=''
 try{const data=await request<any>(`/actors/${encodeURIComponent(name)}/profile`,refresh?json('POST',{}):undefined);if(id!==generation)return;profile.value=data;
  if(!refresh&&(!data.status||data.status==='not_fetched')){await load(true);return}
 }catch(e:any){if(id===generation)error.value=e.message||String(e)}finally{if(id===generation)loading.value=false}
}
watch(()=>props.name,()=>{profile.value={};void load()},{immediate:true})
</script>
<template><section class="actor-profile" aria-label="演员资料">
 <div class="actor-profile-heading"><div class="actor-profile-name"><ActorAvatar v-if="!privacy" :name="name" :src="avatar||profile.avatar_url"/><div><h3>{{name}}</h3><p>{{profile.description||'演员百科与公开账号'}}</p></div></div><button class="btn secondary" :disabled="loading" @click="load(true)">{{loading?'正在读取资料…':'更新演员资料'}}</button></div>
 <p v-if="loading" role="status">正在读取公开百科资料，已缓存资料可在设备离线时查看。</p>
 <p v-if="error" class="form-error" role="alert">{{error}}<span v-if="profile.biography"> · 保留上次缓存资料</span></p>
 <p v-if="profile.biography" class="actor-biography">{{profile.biography}}</p>
 <p v-else-if="!loading" class="muted">尚未找到可核实的百科简介。可以使用下方搜索入口查找，不会猜测社交账号。</p>
 <dl class="actor-facts"><template v-if="profile.birth_date"><dt>出生日期</dt><dd>{{profile.birth_date}}</dd></template><template v-if="profile.birth_place"><dt>出生地</dt><dd>{{profile.birth_place}}</dd></template><template v-if="profile.nationality"><dt>国籍</dt><dd>{{Array.isArray(profile.nationality)?profile.nationality.join('、'):profile.nationality}}</dd></template><template v-if="profile.aliases?.length"><dt>其他姓名</dt><dd>{{profile.aliases.join('、')}}</dd></template></dl>
 <h4>公开账号与网站</h4><div class="actor-profile-links"><template v-for="link in profile.links||[]" :key="link.url"><a v-if="safeUrl(link.url)" :href="safeUrl(link.url)" target="_blank" rel="noopener noreferrer">{{linkLabel(link.label)}} ↗</a></template></div><p v-if="!profile.links?.length" class="muted">百科来源暂未收录社交账号。</p>
 <h4>资料来源</h4><div class="actor-profile-links"><template v-for="source in profile.sources||[]" :key="source.url"><a v-if="safeUrl(source.url)" :href="safeUrl(source.url)" target="_blank" rel="noopener noreferrer">{{linkLabel(source.label)}} ↗</a></template></div>
 <p v-if="profile.updated_at" class="muted">资料缓存于 {{new Date(profile.updated_at).toLocaleString()}}</p>
 <h4>外部搜索</h4><p class="muted">以下为搜索入口，搜索结果未经过身份核实。</p><div class="actor-profile-links"><a :href="'https://ja.wikipedia.org/w/index.php?search='+encodeURIComponent(name)" target="_blank" rel="noopener noreferrer">Wikipedia 搜索 ↗</a><a :href="'https://www.wikidata.org/w/index.php?search='+encodeURIComponent(name)" target="_blank" rel="noopener noreferrer">Wikidata 搜索 ↗</a><a :href="'https://x.com/search?q='+encodeURIComponent(name)" target="_blank" rel="noopener noreferrer">X 搜索 ↗</a><a :href="'https://www.javbus.com/searchstar/'+encodeURIComponent(name)" target="_blank" rel="noopener noreferrer">JavBus 演员搜索 ↗</a></div>
</section></template>
