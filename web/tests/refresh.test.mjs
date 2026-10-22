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
 const context=vm.createContext({ref,computed,watch(){},onMounted(){},onUnmounted(){},request,ApiError,
  currentToken:()=>token,clearToken:()=>{token=''},rememberToken(){},json(){},
  localStorage:{getItem:()=>null},window:{},document:{},...Object.fromEntries(icons.map(n=>[n,()=>{}]))})
 const body=source.replace(/^import .*$/gm,'')+'\nglobalThis.harness={refresh,section,devices,libraries,devicesLoaded,error,selectedDownloaderId,loadDownloader,login,loginMode,password};'
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
