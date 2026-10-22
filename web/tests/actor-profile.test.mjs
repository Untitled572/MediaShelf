import {test} from 'node:test'
import assert from 'node:assert/strict'
import vm from 'node:vm'
import {readFileSync} from 'node:fs'
import ts from 'typescript'
import {computed,ref} from 'vue'
const source=readFileSync(new URL('../src/components/ActorProfilePanel.vue',import.meta.url),'utf8').split('<script setup lang="ts">')[1].split('</script>')[0].replace(/^import .*$/gm,'')
function panel(request){
 const context=vm.createContext({computed,ref,watch(){},defineProps:()=>({name:'Example',privacy:true}),request,URL,location:{origin:'https://app.example'},currentToken:()=>'',json:(method,body)=>({method,body:JSON.stringify(body)})})
 vm.runInContext(ts.transpileModule(source+'\nglobalThis.panel={load,profile,showOriginal,biography,description,translationError,translationLoading};',{compilerOptions:{target:ts.ScriptTarget.ES2022,module:ts.ModuleKind.None}}).outputText,context)
 return context.panel
}
const tick=()=>new Promise(resolve=>setImmediate(resolve))
test('cached Chinese biography is shown by default without translating again',async()=>{
 const calls=[];const ui=panel(async path=>{calls.push(path);return {status:'found',biography_scope:'lead_and_career',biography:'Japanese original',biography_zh:'中文简介'}})
 await ui.load()
 assert.equal(ui.biography.value,'中文简介')
 assert.equal(calls.length,1)
 ui.showOriginal.value=true
 assert.equal(ui.biography.value,'Japanese original')
})
test('translation failure preserves the original profile and source links',async()=>{
 const ui=panel(async path=>{if(path.endsWith('/translate'))throw new Error('temporary timeout');return {status:'found',biography_scope:'lead_and_career',biography:'Original biography',sources:[{label:'Wikipedia',url:'https://ja.wikipedia.org/wiki/Example'}]}})
 await ui.load();await tick()
 assert.equal(ui.biography.value,'Original biography')
 assert.equal(ui.profile.value.sources.length,1)
 assert.equal(ui.translationError.value,'temporary timeout')
 assert.equal(ui.translationLoading.value,false)
})

test('legacy intro-only cache is expanded once while keeping the cached content visible',async()=>{const calls=[];const ui=panel(async(path,options)=>{calls.push(options?.method||'GET');return options?{status:'found',biography_scope:'lead_and_career',biography:'Long original',biography_zh:'更丰富的中文简介'}:{status:'found',biography:'Short original',biography_zh:'旧简介'}});await ui.load();assert.deepEqual(calls,['GET','POST']);assert.equal(ui.biography.value,'更丰富的中文简介')})
