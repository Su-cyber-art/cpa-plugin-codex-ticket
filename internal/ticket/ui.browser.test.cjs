// DOM/browser interaction tests with synthetic credentials only.
// Run: npm ci && npm test
'use strict';
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const {JSDOM} = require('jsdom');
const html = fs.readFileSync(path.join(__dirname,'ui.html'),'utf8');
const tick = () => new Promise(resolve => setTimeout(resolve,0));
async function settle(){ for(let i=0;i<8;i++) await tick(); }
function make(url='https://panel.example/v0/resource/plugins/codex-ticket/settings') {
  const requests=[];
  const state={current:{mode:'core',ref:'core:global',configured:true,label:'socks5h://host:1080 (已配置认证)'},fail:null};
  const status={cached_count:1,harvest_active:true,inject_active:false,harvest_reason:'ready',inject_reason:'log_unsafe',worker_reason:'candidate_cached',inflight_count:0,tracked_requests:2,entries:[{auth_index:'<img src=x onerror=alert(1)>',model:'gpt-model',remaining_seconds:55,last_http:200,last_length:292,reason:'candidate_cached',injected_count:3}]};
  const dom=new JSDOM(html,{url,runScripts:'dangerously',beforeParse(w){
    w.fetch=async (target,init) => {
      assert.ok(/^\/v0\/management\/codex-ticket\/(status|proxy-options|proxy|proxy-test|refresh|pause|resume)$/.test(target));
      assert.equal(init.credentials,'omit'); assert.equal(init.redirect,'error'); assert.equal(init.cache,'no-store'); assert.equal(init.headers.Authorization,'Bearer test-management-key');
      requests.push({target,method:init.method,body:init.body?JSON.parse(init.body):undefined});
      if(state.fail){const code=state.fail;state.fail=null;return {ok:false,status:code,json:async()=>({reason:'raw-proxy-password-DO-NOT-DISPLAY',error:'test-management-key'})};}
      let data;
      if(target.endsWith('/status'))data=status;
      else if(target.endsWith('/proxy-options')) data={current:state.current,options:[{id:'core:global',label:'CPA 全局 · socks5h://host:1080 (已配置认证)',source:'global',scheme:'socks5h',host:'host',port:1080,has_auth:true}]};
      else if(target.endsWith('/proxy')){
        const body=JSON.parse(init.body);
        state.current={mode:body.mode,ref:body.ref||'',configured:true,label:'socks5h://host:1080 (已配置认证)'};data={current:state.current};
      } else if(target.endsWith('/proxy-test'))data={ok:true,http_status:401,latency_ms:123,reason:'reachable'};
      else data=status;
      return {ok:true,status:200,json:async()=>data};
    };
  }});
  return {dom,w:dom.window,d:dom.window.document,requests,state};
}
async function connect(t){t.d.getElementById('managementKey').value='test-management-key';t.d.getElementById('loginForm').dispatchEvent(new t.w.Event('submit',{bubbles:true,cancelable:true}));await settle();}
async function submit(t,id){t.d.getElementById(id).dispatchEvent(new t.w.Event('submit',{bubbles:true,cancelable:true}));await settle();}
function setMode(t,value){const radio=t.d.querySelector(`input[name=proxyMode][value=${value}]`);radio.checked=true;radio.dispatchEvent(new t.w.Event('change'));}
(async()=>{
  const t=make();assert.equal(t.requests.length,0,'Public shell must not request management data');
  await connect(t);
  assert.match(t.d.getElementById('connection').textContent,/已连接/);
  assert.equal(t.d.getElementById('managementKey').value,'');assert.equal(t.w.localStorage.length,0);assert.equal(t.w.sessionStorage.length,0);assert.equal(t.d.cookie,'');
  assert.equal(t.d.getElementById('cachedCount').textContent,'1');assert.equal(t.d.querySelectorAll('#entries img').length,0,'Account values must be text, not HTML');
  assert.match(t.d.getElementById('entries').textContent,/<img/);assert.equal(t.d.getElementById('coreProxy').value,'core:global');
  setMode(t,'custom');const before=t.requests.length;await submit(t,'proxyForm');assert.equal(t.requests.length,before,'Empty custom URL cannot replace core mode');
  t.d.getElementById('proxyURL').value='socks5h://user:proxy-secret@host:1080';t.d.getElementById('showProxy').click();assert.equal(t.d.getElementById('proxyURL').type,'text');
  await submit(t,'proxyForm');
  assert.deepEqual(t.requests.find(r=>r.target.endsWith('/proxy')).body,{mode:'custom',url:'socks5h://user:proxy-secret@host:1080'});
  assert.equal(t.d.getElementById('proxyURL').value,'');assert.equal(t.d.getElementById('proxyURL').type,'password');assert.ok(!t.d.body.textContent.includes('proxy-secret'));
  await submit(t,'proxyForm');assert.deepEqual(t.requests.filter(r=>r.target.endsWith('/proxy')).at(-1).body,{mode:'custom',url:''});
  setMode(t,'core');t.d.getElementById('coreProxy').value='core:global';await submit(t,'proxyForm');assert.deepEqual(t.requests.filter(r=>r.target.endsWith('/proxy')).at(-1).body,{mode:'core',ref:'core:global'});
  t.d.getElementById('testProxy').click();await settle();assert.match(t.d.getElementById('testResult').textContent,/HTTP 401/);assert.match(t.d.getElementById('connection').textContent,/已连接/);
  for(const name of ['refresh','pause','resume']){t.d.getElementById(name).click();await settle();assert.ok(t.requests.some(r=>r.target.endsWith('/'+name)&&r.method==='POST'));}
  t.state.fail=500;t.d.getElementById('reload').click();await settle();assert.ok(!t.d.body.textContent.includes('DO-NOT-DISPLAY'));
  t.state.fail=401;t.d.getElementById('reload').click();await settle();assert.equal(t.d.getElementById('connection').textContent,'未连接');assert.match(t.d.getElementById('message').textContent,/密钥无效/);assert.equal(t.d.getElementById('entries').textContent.includes('<img'),false);
  await connect(t);t.d.getElementById('proxyURL').value='secret';t.d.getElementById('disconnect').click();assert.equal(t.d.getElementById('proxyURL').value,'');assert.equal(t.d.getElementById('saveProxy').disabled,true);t.w.close();
  for(const url of ['http://remote.example/settings','file:///tmp/ui.html']){const u=make(url);assert.equal(u.d.getElementById('connect').disabled,true);await connect(u);assert.equal(u.requests.length,0);assert.equal(u.d.getElementById('transportWarning').hidden,false);u.w.close();}
  for(const host of ['localhost','127.0.0.1','[::1]']){const u=make('http://'+host+'/settings');await connect(u);assert.match(u.d.getElementById('connection').textContent,/已连接/);u.w.close();}
  assert.ok(!/innerHTML|localStorage|sessionStorage|document\.cookie|console\./.test(html.match(/<script>([\s\S]*?)<\/script>/)[1]));
  console.log('PASS: authenticated connect, memory-only credentials, text-only rows, custom/core save + readback, blank URL rules, masked clearing, saved proxy test (upstream 401), manual actions, safe errors, HTTP401 logout, disconnect, HTTPS and loopback transport guards.');
})().catch(error=>{console.error(error);process.exitCode=1;});
