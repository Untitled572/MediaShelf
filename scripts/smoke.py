"""Run a non-destructive API + device-agent acceptance smoke test against local dev server.
All fixture files and credentials stay in ignored data/smoke/. Existing media is untouched.
"""
import json, pathlib, subprocess, urllib.request, urllib.error, time
ROOT=pathlib.Path(__file__).resolve().parents[1]
DATA=ROOT/'data'/'smoke'; DATA.mkdir(parents=True,exist_ok=True)
BASE='http://127.0.0.1:8765'
access=json.loads((ROOT/'data'/'local-access.json').read_text(encoding='utf-8-sig'))
def api(method,path,body=None,token=None):
    headers={'Content-Type':'application/json'}
    if token:headers['Authorization']='Bearer '+token
    req=urllib.request.Request(BASE+'/api/v1'+path,data=json.dumps(body).encode() if body is not None else None,headers=headers,method=method)
    with urllib.request.urlopen(req,timeout=90) as r:return json.load(r)
token=api('POST','/login',{'password':access['password']})['token']
def admin(method,path,body=None):return api(method,path,body,token)
dev=admin('POST','/devices',{'name':'验收设备（合成样例）'})
fixture=DATA/'library'/'Test'/'TEST-001';fixture.mkdir(parents=True,exist_ok=True)
(fixture/'TEST-001_001.mp4').write_bytes(b'SYNTHETIC SCANNER FIXTURE - NOT A PLAYABLE VIDEO')
(fixture/'TEST-001_002.mp4').write_bytes(b'SYNTHETIC SECOND SEGMENT')
(fixture/'movie.nfo').write_text('<movie><title>验收样例：分段影片</title><premiered>2024-06-01</premiered><uniqueid type="num">TEST-001</uniqueid><uniqueid type="cid">test00001</uniqueid><actor><name>验收演员</name></actor><genre>测试</genre><rating>8.5</rating></movie>',encoding='utf-8')
lib=admin('POST','/libraries',{'id':'smoke-library','name':'验收样例库','device_id':dev['id'],'path':str(DATA/'library'),'openlist_base':'http://127.0.0.1:294','openlist_prefix':'MediaShelf-test'})
conf={'server_url':BASE,'token':dev['token'],'libraries':{lib['id']:lib['path']},'tools':{},'state_dir':str(DATA/'agent-state')}
cp=DATA/'agent.json';cp.write_text(json.dumps(conf,ensure_ascii=False),encoding='utf-8')
subprocess.run([str(ROOT/'dist'/'mediashelf-agent.exe'),'--config',str(cp),'--once','--scan-library'],check=True,cwd=ROOT)
works=admin('GET','/works');w=next(x for x in works if x['code']=='TEST-001')
current_files=[f for f in w['files'] if f['device_id']==dev['id']]
assert len(current_files)==2,(w['code'],len(current_files))
assert w['year']==2024,w['year']
assert w['actors']==['验收演员'],w['actors']
assert w['source_rating'] is not None
admin('PATCH','/works/'+urllib.parse.quote(w['id'],safe=''),{'rating':4.5,'title':'验收样例：手动修改已保存'})
progress={'compression':'done','cutting':'done','translation':'not_needed','verified':True}
admin('PATCH','/works/'+urllib.parse.quote(w['id'],safe='')+'/files/'+current_files[0]['id'],{'progress':progress})
w=admin('GET','/works/'+urllib.parse.quote(w['id'],safe=''));assert not w['progress']['verified']
assert not next(f for f in w['files'] if f['id']==current_files[1]['id'])['progress']['verified']
subprocess.run([str(ROOT/'dist'/'mediashelf-agent.exe'),'--config',str(cp),'--once','--scan-library'],check=True,cwd=ROOT)
w=admin('GET','/works/'+urllib.parse.quote(w['id'],safe=''));assert w['rating']==4.5 and w['title']=='验收样例：手动修改已保存'
admin('PATCH','/actors/'+urllib.parse.quote('独立关注演员',safe=''),{'policy':'selective','target':2})
assert any(a['name']=='独立关注演员' for a in admin('GET','/actors'))
backup=admin('GET','/backup');assert backup['schema_version']==1
assert 'password' not in json.dumps(backup)
print(json.dumps({'api_smoke':'passed','works_in_fixture':1,'segments':2,'checks':['device scan','typed NFO IDs','premiered year','actor import','half rating','segment isolation','rescan preservation','independent actor','metadata backup']},ensure_ascii=False))

