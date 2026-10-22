import {test} from 'node:test'
import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import vm from 'node:vm'
import ts from 'typescript'
import {ref,computed} from 'vue'

// Exercise the real SFC setup code with controlled API response timing.
const source=readFileSync(new URL('../src/App.vue',import.meta.url),'utf8').split('<script setup lang="ts">')[1].split('</script>')[0]
const icons=source.match(/import \{([^}]+)\} from 'lucide-vue-next'/)[1].split(',')
function app(request){
 let token='current-session'
 class ApiError extends Error{constructor(status,message){super(message);this.status=status}}
 const context=vm.createContext({URL,ref,computed,watch(){},onMounted(){},onUnmounted(){},request,ApiError,
  currentToken:()=>token,clearToken:()=>{token=''},rememberToken(){},json(){},
  localStorage:{getItem:()=>null},window:{},document:{},...Object.fromEntries(icons.map(n=>[n,()=>{}]))})
 const body=source.replace(/^import .*$/gm,'')+'\nglobalThis.harness={refresh,section,devices,libraries,devicesLoaded,error,selectedDownloaderId,loadDownloader,login,loginMode,password,loadRecommendations,visibleRecommendations,visibleActorRecs,nextRecommendationBatch,recommendationsLoaded,recommendationLoading,fileOnline,openListUrl,actorRecs};'
 vm.runInContext(ts.transpileModule(body,{compilerOptions:{target:ts.ScriptTarget.ES2022,module:ts.ModuleKind.None}}).outputText,context)
 return context.harness
}
const tick=()=>new Promise(resolve=>setImmediate(resolve))

test('device results appear while recommendations are still pending',async()=>{
 let release
 const pending=new Promise(resolve=>release=resolve)
 const ui=app(path=>path==='/recommendations'?pending:Promise.resolve(path==='/devices'?[{id:'pc',online:true}]:[]))
 ui.section.value='discover'
 const refreshing=ui.refresh()
 await tick()
 assert.equal(ui.devicesLoaded.value,true)
 assert.equal(ui.devices.value[0].id,'pc')
 assert.equal(ui.recommendationLoading.value,true)
 release([]);await refreshing
})

test('device refresh never contacts qBittorrent or recommendation endpoints',async()=>{
 const calls=[]
 const ui=app(async path=>{calls.push(path);return path==='/downloaders'?[{id:'remote-qbit'}]:[]})
 ui.section.value='devices'
 await ui.refresh()
 assert.equal(calls.some(p=>p.includes('/torrents')||p.includes('/categories')||p.includes('recommendations')),false)
 assert.equal(ui.error.value,'')
})

test('stale download responses cannot overwrite newer selection',async()=>{
 let rejectOld
 const pending=new Promise((_,reject)=>rejectOld=reject)
 const ui=app(path=>path.includes('/old/')?pending:Promise.resolve([]))
 const old=ui.loadDownloader('old')
 await ui.loadDownloader('new')
 rejectOld(new Error('old login failed'));await old
 assert.equal(ui.error.value,'')
})

test('recommendations survive background refresh and rotate five works and three actors',async()=>{
 let release
 const pending=new Promise(resolve=>release=resolve)
 const pool=Array.from({length:14},(_,i)=>({work:{id:`work${i}`}}))
 const actors=Array.from({length:8},(_,i)=>({name:`actor${i}`}))
 const ui=app(path=>path==='/recommendations'?pending:Promise.resolve(path==='/actor-recommendations'?actors:path==='/recommendation-status'?{}:[]))
 ui.section.value='discover'
 const loading=ui.loadRecommendations()
 await ui.refresh() // A background refresh must not invalidate the separate recommendation request.
 release(pool);await loading
 assert.equal(ui.recommendationsLoaded.value,true)
 assert.equal(ui.visibleRecommendations.value.length,5)
 assert.equal(ui.visibleActorRecs.value.length,3)
 const first=ui.visibleRecommendations.value[0].work.id
 const actor=ui.visibleActorRecs.value[0].name
 await ui.nextRecommendationBatch()
 assert.notEqual(ui.visibleRecommendations.value[0].work.id,first)
 assert.notEqual(ui.visibleActorRecs.value[0].name,actor)
 assert.equal(new Set(ui.visibleRecommendations.value.map(r=>r.work.id)).size,5)
})

test('offline files have no playback URL but remain in the database model',()=>{
 const ui=app(async()=>[])
 ui.devices.value=[{id:'pc',online:false}]
 ui.libraries.value=[{id:'media',device_id:'pc',openlist_base:'https://files.example/'}]
 const file={library_id:'media',device_id:'pc',relative_path:'film.mp4',missing:false}
 assert.equal(ui.fileOnline(file),false)
 assert.equal(ui.openListUrl(file),'')
 ui.devices.value[0].online=true
 assert.equal(ui.openListUrl(file),'https://files.example/film.mp4')
})