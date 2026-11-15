import { deriveAuthFileIdentity, resolveAntigravityGroupLabelKey, resolveAntigravityBucketLabelKey } from './account-helpers.js';
'use strict';
const $ = id => document.getElementById(id);
const api = '/v0/management/plugins/antigravity-acp';
let managementKey = '', modelIDs = [], busy = false, embedded = false;
let statusRunning = false, catalogFingerprint = '', requestFingerprint = '';
const channel = 'antigravity-acp-management-v1';
const pending = new Map();
let sequence = 0;
const accountActions={'/settings':'save-settings','/accounts':'accounts','/accounts/refresh':'refresh-account','/accounts/select':'select-account','/accounts/rename':'rename-account','/accounts/delete':'delete-account','/oauth/start':'oauth-start','/oauth/status':'oauth-status','/oauth/complete':'oauth-complete','/oauth/cancel':'oauth-cancel'};
function relay(path,body) {
 const action = path === '/prompt-rules' ? (body===undefined?'prompt-rules':'save-prompt-rules') : path === '/status' ? 'status' : path === '/models/refresh' ? 'refresh-models' : accountActions[path];
 if (!action) return Promise.reject(new Error('不支持的管理操作。'));
 return new Promise((resolve,reject)=>{
  const id=String(++sequence);
  const timer=setTimeout(()=>{pending.delete(id);reject(new Error('主页面请求超时，请刷新 CPA 后重试。'));},46000);
  pending.set(id,{resolve,reject,timer});
  window.parent.postMessage({channel,type:'request',id,action,body},location.origin);
 });
}
window.addEventListener('message',event=>{
 if(event.source!==window.parent || event.origin!==location.origin || !event.data || event.data.channel!==channel)return;
 if(event.data.theme==='light'||event.data.theme==='dark'||event.data.theme==='white')document.documentElement.dataset.theme=event.data.theme;
 if(event.data.type==='connected' && !embedded){embedded=true;$('login-panel').hidden=true;action(()=>status());}
 if(event.data.type==='response') {const item=pending.get(event.data.id);if(!item)return;clearTimeout(item.timer);pending.delete(event.data.id);event.data.ok?item.resolve(event.data.data):item.reject(new Error(event.data.error||'管理请求失败。'));}
});
const names = {'gemini-4-argon':'Gemini 4 Argon','gemini-4-hydrogen':'Gemini 4 Hydrogen','gemini-pro-orchestrator-g1-pro':'Gemini Pro Orchestrator','gemini-3.8-flash':'Gemini 3.8 Flash'};
function notice(text, error = false) { $('notice').textContent=text; $('notice').hidden=!text; $('notice').className='notice'+(error?' error':''); }
function controls() { promptControls(); $('native-web-toggle').disabled=(!managementKey&&!embedded)||busy||!nativeWebOnline; for (const id of ['refresh-status','refresh-route','refresh-models','refresh-accounts','add-account']) $(id).disabled=(!managementKey&&!embedded)||busy; for(const button of document.querySelectorAll('.account-actions button,.account-edit button'))button.disabled=busy||button.dataset.locked==='true'; $('connect').disabled=busy; $('disconnect').disabled=busy; $('model-search').disabled=!managementKey&&!embedded; }
function clock(value) { return value ? new Date(value).toLocaleString('zh-CN',{hour12:false}) : '尚未获取'; }
function renderCatalog(data) {
 const fingerprint=JSON.stringify(data);if(fingerprint===catalogFingerprint)return;catalogFingerprint=fingerprint;
 modelIDs=Array.isArray(data.models)?data.models:[];
 $('model-count').textContent=data.checked_at?modelIDs.length:'—';
 $('catalog-time').textContent=data.checked_at?'目录更新于 '+clock(data.checked_at):'尚未获取目录';
 $('catalog-error').hidden=!data.error; $('catalog-error').textContent=data.error||'';
 renderModels();
}
function renderModels() {
 const query=$('model-search').value.toLowerCase().trim(); const shown=modelIDs.filter(id=>id.toLowerCase().includes(query));$('models').replaceChildren();
 if(!shown.length) {const p=document.createElement('p');p.className='empty';p.textContent=modelIDs.length?'没有匹配的模型，请调整搜索内容。':'暂无模型。点击“更新模型目录”读取当前账号可用的 Gemini 模型。';$('models').append(p);return;}
 for(const id of shown) {const row=document.createElement('div');row.className='model-row';const info=document.createElement('div');info.className='model-info';const title=document.createElement('span');title.className='model-name';title.textContent=names[id.replace('antigravity-acp/','')]||id.replace('antigravity-acp/','');const code=document.createElement('code');code.textContent=id;info.append(title,code);const button=document.createElement('button');button.textContent='复制名称';button.setAttribute('aria-label','复制 '+id);button.addEventListener('click',()=>copy(id,button));row.append(info,button);$('models').append(row);}
}
async function copy(text,button) {try {if(navigator.clipboard&&window.isSecureContext){await navigator.clipboard.writeText(text);}else{const el=document.createElement('textarea');el.value=text;el.setAttribute('aria-label','待复制内容');document.body.append(el);el.select();const ok=document.execCommand('copy');el.remove();if(!ok)throw new Error();}const previous=button.textContent;button.textContent='已复制';setTimeout(()=>button.textContent=previous,1500);}catch{notice('复制失败，请手动选择并复制文本。',true);}}
async function request(path,method='GET',body) {
 if(embedded)return relay(path,body);
 const controller=new AbortController();const timer=setTimeout(()=>controller.abort(),method==='POST'?45000:12000);
 try {const response=await fetch(api+path,{method,headers:{Authorization:'Bearer '+managementKey,'Content-Type':'application/json'},body:body===undefined?undefined:JSON.stringify(body),cache:'no-store',credentials:'omit',signal:controller.signal});
  if(response.status===401||response.status===403){disconnect();throw new Error('管理密钥无效或未获准远程访问，请检查后重新连接。');}
  const data=await response.json();if(!response.ok){if(data.catalog)renderCatalog(data.catalog);throw new Error(typeof data.error==='string'?data.error:'请求失败（HTTP '+response.status+'）');}return data;
 }catch(error){if(error.name==='AbortError')throw new Error('请求超时，请检查服务状态后重试。');throw error;}finally{clearTimeout(timer);}
}
function renderRoute(data) {
 renderPromptStatus(data?.prompt);
 renderNativeWeb(data?.bridge);
 const bridge=data?.bridge, online=bridge?.online, cache=online?bridge.session_cache:null;
 $('route-state').textContent=!data?'等待状态':online?(bridge.active_requests>0?'请求处理中':'通道可达'):'桥接不可达';
 $('route-state').className='badge'+(online?' connected':'');
 const concurrency=online?bridge.concurrency:null;
 $('route-active').textContent=concurrency?concurrency.active+' / '+concurrency.limit:online?bridge.active_requests:'—';
 $('route-queued').textContent=concurrency?concurrency.queued+' / '+concurrency.queue_limit:'—';
 $('route-queue-detail').textContent=concurrency?'最长等待 '+concurrency.queue_timeout_ms/1000+' 秒':'排队状态待获取';
 $('route-sessions').textContent=cache?.sessions??'—';
 $('route-idle').textContent=cache?cache.idle+' 个空闲会话 · 进程复用 '+(cache.process_hits||0)+' 次':'空闲数量待获取';
 const totals=online?bridge.request_stats:null;
 $('route-total').textContent=totals?.total==null?'—':Number(totals.total).toLocaleString('zh-CN');
 $('route-hits').textContent=cache?.hits==null?'—':Number(cache.hits).toLocaleString('zh-CN');
 $('route-hit-rate').textContent=totals?.total>0&&cache?((cache.hits/totals.total)*100).toFixed(1)+'%':'—';
 $('route-stats-since').textContent=totals?.since?'累计自 '+clock(totals.since)+' · Bridge 重启后重新计数 · 包含排队、失败与取消请求':'统计自 Bridge 本次启动起累计；不含状态查询与模型目录请求。';
 $('route-misses').textContent=cache?cache.misses+' / '+cache.bypassed:'—';
 $('route-checked').textContent=data?'状态检查于 '+clock(data.checked_at):'尚未读取状态';
 if(online||!data)renderRecentRequests(bridge?.recent_requests);
 $('route-refresh-state').textContent=online?($('route-auto').checked?'每 5 秒自动刷新 · 仅读取运行状态':'自动刷新已暂停'):data?'桥接不可达，保留上次请求记录':'连接后自动刷新';
}
function renderRouteAccount(data) {
 const account=data?.accounts?.find(item=>item.active);
 $('route-account').textContent=account?(account.email||account.label):data?'未选择账号':'待获取';
 $('route-tier').textContent=account?.details?.tier?.name||'';
}

let nativeWebOnline=false,nativeWebValue=false;
function renderNativeWeb(bridge){
 nativeWebOnline=!!bridge?.online;nativeWebValue=!!bridge?.native_web;
 $('native-web-toggle').checked=nativeWebValue;
 $('native-web-state').textContent=!nativeWebOnline?'待连接':nativeWebValue?'已开启':'已关闭';
 controls();
}
$('native-web-toggle').addEventListener('change',()=>{
 const enabled=$('native-web-toggle').checked;
 action(async()=>{
  try{
   const data=await request('/settings','POST',{native_web:enabled});
   nativeWebValue=data.native_web;
   $('native-web-state').textContent=nativeWebValue?'已开启':'已关闭';
   notice(nativeWebValue?'原生联网搜索已开启，对后续请求生效。':'原生联网搜索已关闭，对后续请求生效。');
  }finally{$('native-web-toggle').checked=nativeWebValue;}
 });
});

$('refresh-route').addEventListener('click',()=>action(status));
async function status(refreshAccounts=true) {
 if(statusRunning)return;
 statusRunning=true;
 try {
 const data=await request('/status');if(!managementKey&&!embedded)return;
 $('login-panel').hidden=true;$('disconnect').hidden=embedded;$('session-state').textContent=embedded?'已连接 CPA 主页面':'管理会话已连接';$('session-state').className='badge connected';
 const online=data.bridge.online;$('bridge-state').textContent=online?'运行中':'无法连接';$('bridge-state').className=online?'good':'bad';$('bridge-detail').textContent=online?'内部连接正常':data.bridge.message;
 $('active-count').textContent=online?data.bridge.active_requests:'—';$('plugin-version').textContent=data.plugin_version;$('bridge-version').textContent=online?'桥接版本 '+data.bridge.version:'桥接版本不可用';$('updated-at').textContent='状态检查于 '+clock(data.checked_at);renderCatalog(data.catalog);renderRoute(data);if(refreshAccounts){await loadAccounts();if(!rulesLoaded)await loadRules();}return data;
 } finally {statusRunning=false;}
}
async function pollRoute() {
 if(document.hidden||!$('route-auto').checked||busy||statusRunning||(!managementKey&&!embedded))return;
 try {await status(false);} catch {
  $('route-state').textContent='状态待更新';$('route-state').className='badge';
  $('route-refresh-state').textContent='自动刷新失败，保留上次结果；稍后重试。';
 }
}
setInterval(pollRoute,5000);
document.addEventListener('visibilitychange',()=>{if(!document.hidden)pollRoute();});
$('route-auto').addEventListener('change',()=>{
 $('route-refresh-state').textContent=$('route-auto').checked?'每 5 秒自动刷新 · 仅读取运行状态':'自动刷新已暂停';
 if($('route-auto').checked)pollRoute();
});
let recentItems=[], selectedRequest='', followRequests=true;
const requestRows=new Map(), requestDots=new Map();
const outcomes={queued:'等待执行',running:'执行中',completed:'已完成',tool_call:'工具交接',failed:'失败',cancelled:'已取消'};
const cacheNames={hit:'缓存命中',miss:'缓存未命中',bypass:'绕过缓存',not_checked:'未检查缓存'};
const processNames={cold:'冷启动',reused:'复用进程 · 新会话',session:'沿用会话进程'};
const requestCacheLabel=item=>item.process_cache==='reused'?'新会话 · 进程复用':(cacheNames[item.session_cache]||'待匹配');
const reasons={process_reassigned:'对应旧会话已关闭，其进程已供新会话使用',no_cached_session:'当前没有可匹配的缓存会话',scope_mismatch:'现有会话配置不匹配',instructions_mismatch:'历史中的系统提示词与现有会话不同',history_mismatch:'请求历史不是现有会话历史的完整延续',continuation_mismatch:'新增消息不符合续接规则：需对应的单条工具结果，或新的用户消息',matching_scope_busy:'同配置会话正在使用中，且没有可续接的空闲会话',process_exited:'对应的 ACP 进程已退出',idle_expired:'对应会话空闲超过 5 分钟，已释放',capacity_evicted:'对应会话此前因容量限制被回收',previous_request_ended:'对应会话因先前请求失败或取消而关闭',account_switched:'对应会话在账号切换时被清理',session_capacity:'会话池已满，且没有可回收的空闲会话',reuse_disabled:'服务端已关闭会话复用',missing_caller_scope:'请求缺少 CPA 调用方标识',not_admitted:'请求尚未获准执行，未进入会话匹配',not_matched:'请求尚未进入会话匹配',history_prefix_match:'配置与历史匹配，继续使用同一 ACP 会话'};
const differenceNames={endpoint:'接口',account:'账号',model:'模型',instructions:'系统提示词',tools:'工具定义',tool_names:'工具名称或命名空间',tool_policy:'工具策略'};
const isLive=item=>item.status==='running'||item.status==='queued';
const shortModel=item=>{const id=item.model||item.requested_model||'';return names[id.replace('antigravity-acp/','')]||id.replace('antigravity-acp/','')||'模型待解析';};
const duration=item=>Number.isFinite(item.duration_ms)?(item.duration_ms/1000).toFixed(2)+' 秒':'—';
function reasonText(item){let text=reasons[item.session_reason]||'此请求未记录具体匹配原因';if(item.session_differences?.length)text+='：'+item.session_differences.map(x=>differenceNames[x]||x).join('、')+'不同';if(item.process_cache==='reused')text+='；已复用空闲 ACP 进程，关闭旧会话并建立独立新会话';return text;}
function pickRequest(id){selectedRequest=id;followRequests=false;paintRequestSelection();}
$('request-follow').addEventListener('click',()=>{followRequests=true;selectedRequest=recentItems[0]?.id||'';paintRequestSelection();});
function renderRecentRequests(items) {
 const fingerprint=JSON.stringify(items??null);if(fingerprint===requestFingerprint)return;requestFingerprint=fingerprint;
 recentItems=Array.isArray(items)?items:[];
 const live=recentItems.filter(isLive).length;
 $('recent-count').textContent=Array.isArray(items)?live+' 进行中 · '+(recentItems.length-live)+' 条已结束':'尚未读取';
 if(followRequests)selectedRequest=recentItems[0]?.id||'';
 if(!recentItems.length){$('request-topology').hidden=true;inspectorFingerprint='';requestRows.clear();requestDots.clear();$('request-dots').replaceChildren();$('recent-requests').replaceChildren(element('p','request-empty',Array.isArray(items)?'暂无请求。聊天或 Agent 请求到达后会自动显示。':'连接后显示最近请求。'));$('request-detail').replaceChildren(element('p','request-empty','选择一个请求，查看它实际经过的模型、账号与会话。'));return;}
 $('recent-requests').querySelector('.request-empty')?.remove();
 const ids=new Set(recentItems.map(item=>item.id));
 for(const [id,row] of requestRows)if(!ids.has(id)){row.remove();requestRows.delete(id);requestDots.get(id)?.remove();requestDots.delete(id);}
 for(const [i,item] of recentItems.entries()){
  let row=requestRows.get(item.id), dot=requestDots.get(item.id);
  if(!row){row=element('button','request-row');row.type='button';row.addEventListener('click',()=>pickRequest(item.id));for(const cls of ['request-row-time','request-row-model','request-row-duration','request-row-account','request-row-state','request-row-source'])row.append(element('span',cls));requestRows.set(item.id,row);dot=element('button','request-dot');dot.type='button';dot.addEventListener('click',()=>pickRequest(item.id));requestDots.set(item.id,dot);}
  row.dataset.outcome=item.status;row.dataset.cache=item.session_cache;dot.dataset.outcome=item.status;dot.dataset.cache=item.session_cache;
  const values=[new Date(item.started_at).toLocaleTimeString('zh-CN',{hour12:false}),shortModel(item),duration(item),item.account_email||item.account_label||'账号待分配',(outcomes[item.status]||item.status)+' · '+requestCacheLabel(item),item.source_ip||'来源 IP 未提供'];
  values.forEach((value,index)=>{if(row.children[index].textContent!==value)row.children[index].textContent=value;});
  row.title=reasonText(item);dot.title=values[0]+' · '+values[1]+' · '+values[4];dot.setAttribute('aria-label','查看请求 '+item.id.slice(-8)+'：'+dot.title);row.setAttribute('aria-label',values.join('，'));
  if($('recent-requests').children[i]!==row)$('recent-requests').insertBefore(row,$('recent-requests').children[i]||null);
  if($('request-dots').children[i]!==dot)$('request-dots').insertBefore(dot,$('request-dots').children[i]||null);
 }
 paintRequestSelection();
}
let inspectorFingerprint='';
function paintRequestSelection(){
 $('request-follow').setAttribute('aria-pressed',String(followRequests));
 for(const [id,row] of requestRows){row.setAttribute('aria-pressed',String(id===selectedRequest));requestDots.get(id).setAttribute('aria-pressed',String(id===selectedRequest));}
 const item=recentItems.find(x=>x.id===selectedRequest);
 if(!item){$('request-topology').hidden=true;$('request-detail').replaceChildren(element('p','request-empty','所选请求已移出最近 20 条记录。点击“跟随最新”继续查看。'));inspectorFingerprint='';return;}
 const fingerprint=JSON.stringify({...item,followRequests,duration_ms:0,queue_ms:0});
 if(fingerprint!==inspectorFingerprint){
  inspectorFingerprint=fingerprint;const root=$('request-detail');root.replaceChildren();root.dataset.live=String(item.status==='running');
  const topology=$('request-topology');topology.hidden=false;topology.replaceChildren();topology.dataset.live=String(item.status==='running');topology.dataset.outcome=item.status;
  const heading=element('div','inspector-heading');heading.append(element('span','eyebrow',followRequests?'当前请求流向':'所选请求流向'),element('span','badge',outcomes[item.status]||item.status));topology.append(heading);
  const flow=element('div','request-flow');
  const icon=kind=>{const svg=document.createElementNS('http://www.w3.org/2000/svg','svg');svg.setAttribute('viewBox','0 0 32 32');svg.setAttribute('aria-hidden','true');svg.classList.add('flow-icon');const path=document.createElementNS(svg.namespaceURI,'path');path.setAttribute('d',kind==='client'?'M6 6h20v20H6z M11 12l4 4-4 4 M18 20h4':'M16 2C18 11 21 14 30 16C21 18 18 21 16 30C14 21 11 18 2 16C11 14 14 11 16 2Z');if(kind==='client'){path.setAttribute('fill','none');path.setAttribute('stroke','currentColor');path.setAttribute('stroke-width','2');path.setAttribute('stroke-linecap','round');path.setAttribute('stroke-linejoin','round');}else path.setAttribute('fill','currentColor');svg.append(path);return svg;};
  const source=element('div','flow-node flow-source');source.append(icon('client'),element('strong','flow-title','聊天 / Agent'),element('span','flow-subtitle',item.source_ip||'来源 IP 未提供'));
  const hub=element('div','flow-node flow-hub');const mark=element('img','flow-logo');mark.src='./logo.svg';mark.alt='';hub.append(mark,element('strong','flow-title','Antigravity ACP'),element('span','flow-subtitle','CPA · 官方 CLI'));
  const target=element('div','flow-node flow-target');const title=element('div','flow-target-title');title.append(element('span','flow-status-dot'),icon('gemini'),element('strong','flow-title','Gemini'),element('code','flow-model',item.model||item.requested_model||'模型待解析'));target.append(title);
  target.append(element('p','flow-target-status',(outcomes[item.status]||item.status)+' · '+requestCacheLabel(item)),element('p','flow-target-account',item.account_email||item.account_label||'账号尚未分配'));
  const wire=()=>{const el=element('span','flow-wire');el.setAttribute('aria-hidden','true');return el;};
  flow.append(source,wire(),hub,wire(),target);topology.append(flow);
  const verdict=element('div','request-verdict');verdict.dataset.cache=item.session_cache;verdict.append(element('strong','',item.session_cache==='hit'?'为什么复用会话':item.session_cache==='not_checked'?'匹配进度':'为什么未复用会话'),element('p','',reasonText(item)));root.append(verdict);
  const facts=element('dl','request-facts');for(const [label,value] of [['开始时间',clock(item.started_at)],['总耗时',''],['排队耗时',''],['接口',item.endpoint],['ACP 会话',item.session_id?item.session_id.slice(-12):'尚未建立'],['请求 ID',item.id.slice(-12)],['缓存状态',cacheNames[item.session_cache]||'未知'],['ACP 进程',processNames[item.process_cache]||'未记录'],['联网搜索',item.native_web?'开启 · 搜索 '+(item.web_searches||0)+' 次 / 读取 '+(item.web_reads||0)+' 次':'关闭'],['会话准备',item.process_cache?(Number(item.setup_ms||0)/1000).toFixed(3)+' 秒':'未记录']]){const cell=element('div','');cell.append(element('dt','',label),element('dd','',value));facts.append(cell);}root.append(facts);
 }
 const facts=$('request-detail').querySelectorAll('dd');facts[1].textContent=duration(item);facts[2].textContent=(Number(item.queue_ms||0)/1000).toFixed(2)+' 秒';
}

function disconnect(){rulesLoaded=false;rulesEditable=false;rulesDirty=false;rulesRevisionValue='';$('rule-words').value='';$('rule-replacements').replaceChildren();$('rule-patterns').replaceChildren();$('rules-state').textContent='待连接';$('rules-save-state').textContent='连接后加载共享规则';renderRoute();renderRouteAccount();clearTimeout(oauthTimer);oauthJob=null;$('accounts').replaceChildren();$('oauth-panel').hidden=true;$('oauth-code').value='';managementKey='';$('management-key').value='';$('login-panel').hidden=false;$('disconnect').hidden=true;$('session-state').textContent='未连接管理会话';$('session-state').className='badge';$('bridge-state').textContent='待连接';$('bridge-state').className='';$('bridge-detail').textContent='连接管理会话后查看';$('active-count').textContent='—';$('bridge-version').textContent='桥接版本待获取';$('updated-at').textContent='尚未检查服务状态';renderCatalog({models:[]});controls();}
async function action(fn){if(busy)return;busy=true;controls();notice('');try{await fn();}catch(e){notice(e.message||'请求失败，请稍后重试。',true);}finally{busy=false;controls();}}
$('login-form').addEventListener('submit',e=>{e.preventDefault();action(async()=>{managementKey=$('management-key').value.trim();if(!managementKey)throw new Error('请输入 CPA 管理密钥。');try{await status();$('management-key').value='';}catch(e){disconnect();throw e;}});});
$('disconnect').addEventListener('click',()=>{disconnect();notice('管理会话已断开。');});
$('refresh-status').addEventListener('click',()=>action(async()=>{await status();notice('服务状态已刷新。');}));
$('refresh-models').addEventListener('click',()=>action(async()=>{const button=$('refresh-models');button.textContent='正在获取…';try{const data=await request('/models/refresh','POST');renderCatalog(data.catalog);notice('模型目录已更新，共 '+modelIDs.length+' 个 Gemini 模型。');}finally{button.textContent='更新模型目录';}}));
$('model-search').addEventListener('input',renderModels);

if(window.parent!==window){document.body.classList.add('embedded');$('login-panel').hidden=true;$('session-state').textContent='正在连接 CPA 主页面';window.parent.postMessage({channel,type:'ready'},location.origin);setTimeout(()=>{if(!embedded){$('login-panel').hidden=false;$('session-state').textContent='请连接管理会话';}},2500);}

let oauthJob=null, oauthTimer;
const quotaLabels={group_gemini_models:'Gemini 模型',weekly_limit:'每周额度',daily_limit:'每日额度',five_hour_limit:'5 小时额度',monthly_limit:'每月额度'};
function element(tag,className,text){const el=document.createElement(tag);el.className=className;if(text!==undefined)el.textContent=text;return el;}
function resetText(value){
 const ms=Date.parse(value);if(!Number.isFinite(ms))return '重置时间未提供';
 const minutes=Math.ceil((ms-Date.now())/60000);if(minutes<=0)return '已到重置时间，可刷新确认';
 const days=Math.floor(minutes/1440),hours=Math.floor(minutes%1440/60),rest=minutes%60;
 return (days?days+' 天 '+hours+' 小时':hours?hours+' 小时 '+rest+' 分钟':minutes+' 分钟')+'后重置';
}
function renderAccountQuota(account){
 const details=account.details||{};const section=element('div','account-quota');
 const heading=element('div','quota-heading');heading.append(element('strong','','Gemini 配额'),element('span','quota-source','官方 CLI'));section.append(heading);
 if(details.error)section.append(element('p','quota-error',details.error+(details.checked_at?' 以下为上次成功结果。':'')));
 if(!details.checked_at)section.append(element('p','quota-message',account.refreshing?'正在读取账号信息与配额…':'点击下方“刷新配额”查看剩余额度。'));
 else if(!details.groups?.length)section.append(element('p','quota-message','官方 CLI 暂未提供 Gemini 配额。'));
 for(const group of details.groups||[]){
  const block=element('div','quota-group');block.append(element('h4','quota-group-title',quotaLabels[resolveAntigravityGroupLabelKey(group.name)]||group.name));
  for(const bucket of group.buckets||[]){
   const row=element('div','quota-row');const header=element('div','quota-row-header');
   const percent=typeof bucket.remaining_fraction==='number'&&Number.isFinite(bucket.remaining_fraction)?Math.min(100,Math.max(0,bucket.remaining_fraction*100)):null;
   const disabled=bucket.disabled===true;
   const label=quotaLabels[resolveAntigravityBucketLabelKey(bucket.name,bucket.window)]||bucket.name;
   header.append(element('span','quota-model',label),element('span','quota-value',disabled?'暂不可用':percent===null?'未提供':percent.toFixed(1)+'% 剩余'));row.append(header);
   if(!disabled){const meter=element('progress','quota-meter');meter.max=100;meter.value=percent??0;meter.dataset.level=percent===null?'unknown':percent>=70?'high':percent>=30?'medium':'low';meter.setAttribute('aria-label',group.name+' '+label+' 剩余额度');if(percent===null)meter.setAttribute('aria-valuetext','未提供');row.append(meter);}
   const reset=element('p','quota-reset',resetText(bucket.reset_time));if(bucket.reset_time){reset.dataset.reset=bucket.reset_time;reset.title=clock(bucket.reset_time);}row.append(reset);block.append(row);
  }
  section.append(block);
 }
 if(details.checked_at)section.append(element('p','quota-updated','更新于 '+clock(details.checked_at)));
 return section;
}
function renderAccounts(data) {
 renderRouteAccount(data);
 $('accounts').replaceChildren();
 if(!data.accounts?.length){$('accounts').append(element('p','empty','尚未添加账号。'));return;}
 for(const account of data.accounts) {
  const details=account.details||{};
  const card=element('article','account-card'+(account.active?' account-current':''));
  const identity=deriveAuthFileIdentity({name:account.label+'.json',email:account.email});
  const head=element('div','account-head');const title=element('h3','account-identity');title.append(element('span','provider-badge','Antigravity'),element('span','account-name',identity.primary));head.append(title);card.append(head);
  if(identity.secondary)card.append(element('p','account-secondary',identity.secondary));
  const chips=element('div','account-chips');chips.append(element('span','badge'+(account.active?' connected':''),account.active?'当前使用':'未启用'));
  if(details.tier?.name){const tier=element('span','plan-chip',details.tier.name);tier.title='套餐来自 ACP · '+clock(details.tier_checked_at);chips.append(tier);}
  card.append(chips);
  if(!account.email)card.append(element('p','account-hint','本地登录资料未提供邮箱，当前显示账号备注。'));
  if(details.tier_error)card.append(element('p','quota-error',details.tier_error));
  card.append(renderAccountQuota(account));
  const edit=element('details','account-edit');edit.append(element('summary','','编辑备注'));
  const form=element('form','account-edit-form');const label=element('input','');label.value=account.label;label.maxLength=80;label.required=true;label.setAttribute('aria-label','账号备注 '+identity.primary);const save=element('button','','保存备注');save.type='submit';form.append(label,save);form.addEventListener('submit',e=>{e.preventDefault();action(async()=>{renderAccounts(await request('/accounts/rename','POST',{id:account.id,label:label.value}));notice('账号备注已保存。');});});edit.append(form);card.append(edit);
  const footer=element('div','account-footer');footer.append(element('span','account-origin',account.legacy?'原有登录':'添加于 '+clock(account.created_at)));
  const buttons=element('div','account-actions');
  const make=(text,fn,locked=false)=>{const b=element('button','',text);b.type='button';b.dataset.locked=String(locked);b.disabled=locked;b.addEventListener('click',()=>action(fn));buttons.append(b);return b;};
  make(account.refreshing?'读取中…':'刷新配额',async()=>{const result=await request('/accounts/refresh','POST',{id:account.id});renderAccounts(result);const detail=result.accounts?.find(item=>item.id===account.id)?.details;notice(detail?.error||detail?.tier_error||'账号信息已更新；5 分钟内重复查询使用缓存。',Boolean(detail?.error||detail?.tier_error));},account.refreshing);
  make(account.active?'使用中':'切换使用',async()=>{renderAccounts(await request('/accounts/select','POST',{id:account.id}));renderCatalog({models:[]});notice('账号已切换。请更新模型目录，再在 CPA 插件列表重新加载插件以同步下游目录。');},account.active);
  if(!account.legacy){make('删除',async()=>{if(!confirm('删除“'+account.label+'”在本插件保存的登录凭证？此操作不可撤销。'))return;renderAccounts(await request('/accounts/delete','POST',{id:account.id}));notice('登录凭证已删除。');},account.active).className='danger';}
  footer.append(buttons);card.append(footer);$('accounts').append(card);
 }
 controls();
}
setInterval(()=>{for(const el of document.querySelectorAll('[data-reset]'))el.textContent=resetText(el.dataset.reset);},60000);
async function loadAccounts(){renderAccounts(await request('/accounts'));renderOAuth(await request('/oauth/status','POST',{}));}
function renderOAuth(data){
 oauthJob=data;clearTimeout(oauthTimer);
 const pending=['starting','waiting','exchanging'].includes(data.status);
 const messages={idle:'填写账号备注后生成授权链接。',starting:'正在生成官方授权链接…',waiting:'等待 Google 授权，链接有效至 '+clock(data.expires_at),exchanging:'官方 CLI 正在完成登录…',completed:'登录已保存。新账号未自动启用，可在账号列表中手动切换。',cancelled:'本次登录已取消。',expired:'授权已过期，请重新生成链接。',failed:'登录未完成，请重新生成链接并授权。'};
 $('oauth-status').textContent=messages[data.status]||'未知登录状态';
 $('oauth-start-form').hidden=pending;$('oauth-authorize').hidden=data.status!=='waiting';$('oauth-cancel').hidden=!pending;
 $('oauth-link').removeAttribute('href');
 if(data.status==='waiting') {const url=new URL(data.authorization_url);if(url.protocol!=='https:'||url.host!=='accounts.google.com')throw new Error('授权地址无效');$('oauth-link').href=url.href;}
 if(pending){$('oauth-panel').hidden=false;oauthTimer=setTimeout(pollOAuth,2500);}
}
async function pollOAuth(){if(!managementKey&&!embedded)return;try{const data=await request('/oauth/status','POST',{id:oauthJob.id});renderOAuth(data);if(data.status==='completed')renderAccounts(await request('/accounts'));}catch(e){notice(e.message,true);}}
$('refresh-accounts').addEventListener('click',()=>action(loadAccounts));
$('add-account').addEventListener('click',()=>{$('oauth-panel').hidden=false;$('account-label').focus();});
$('oauth-close').addEventListener('click',()=>{$('oauth-panel').hidden=true;});
$('oauth-start-form').addEventListener('submit',e=>{e.preventDefault();action(async()=>{renderOAuth(await request('/oauth/start','POST',{label:$('account-label').value}));});});
$('oauth-complete-form').addEventListener('submit',e=>{e.preventDefault();action(async()=>{const code=$('oauth-code').value;$('oauth-code').value='';renderOAuth(await request('/oauth/complete','POST',{id:oauthJob.id,code}));});});
$('oauth-cancel').addEventListener('click',()=>action(async()=>{renderOAuth(await request('/oauth/cancel','POST',{id:oauthJob.id}));$('oauth-code').value='';}));
$('oauth-copy').addEventListener('click',()=>copy($('oauth-link').href,$('oauth-copy')));

let rulesLoaded=false, rulesEditable=false, rulesDirty=false, rulesRevisionValue='';
function promptControls(){
 const connected=!!managementKey||embedded;
 $('rules-editor').disabled=!connected||busy||!rulesEditable;
 $('reload-rules').disabled=!connected||busy;
 $('save-rules').disabled=!connected||busy||!rulesEditable||!rulesDirty;
}
const updateLabels={checking:'正在检查',current:'已是最新版',updated:'升级成功',busy:'请求繁忙，等待空闲',rolled_back:'升级失败，已回滚',failed:'检查或升级失败',disabled:'插件已停用',manual_release:'新版需要手动升级'};
function renderPromptStatus(data){
 $('prompt-cpa-version').textContent=data?.cpa_version||'—';
 $('prompt-cpa-commit').textContent=data?.cpa_commit?data.cpa_commit.slice(0,12):'算法版本待获取';
 const wrapper=data?.updater, current=wrapper?.snapshot, last=current?.last_update;
 $('update-state').textContent=!current?'暂无记录':current.enabled===false?'定时检查未启用':current.enabled===true?'自动检查已启用':'已接入更新器';
 $('update-state').className='badge'+(current?.enabled?' connected':'');
 $('update-checked').textContent=current?.checked_at?clock(current.checked_at):'尚未检查';
 $('update-result').textContent=current?(updateLabels[current.status]||current.status):'—';
 $('update-result').className=['failed','rolled_back'].includes(current?.status)?'bad':'';
 $('update-last-time').textContent=last?.checked_at?clock(last.checked_at):'尚无记录';
 $('update-last-result').textContent=last?last.status==='rolled_back'?'回滚至 '+last.version:(last.previous_version?last.previous_version+' → ':'')+last.version+' · 升级成功':'尚无升级记录';
 $('update-message').textContent=current?.error||wrapper?.message||(current?.available?'发现版本 '+current.available+'。':'上游每 6 小时检查；服务器约每 30 分钟检查，空闲 30 秒后热升级。')+(current?' 启用状态来自最近一次检查。':'');
}
function markRulesDirty(){
 rulesDirty=true;$('rules-save-state').textContent='有未保存的更改';
 $('words-count').textContent=$('rule-words').value.split(/\r?\n/).filter(x=>x.trim()).length+' 条';
 promptControls();
}
function addRuleRow(kind,first='',second=''){
 const root=$(kind==='pattern'?'rule-patterns':'rule-replacements');
 root.querySelector('.rule-empty')?.remove();
 const row=element('div','rule-row'), head=element('div','rule-row-head');
 head.append(element('span','',kind==='pattern'?'正则规则':'文本替换'));
 const actions=element('div','rule-row-actions');
 if(kind==='pattern')for(const [name,direction] of [['上移',-1],['下移',1]]){
  const button=element('button','quiet',name);button.type='button';
  button.addEventListener('click',()=>{const sibling=direction<0?row.previousElementSibling:row.nextElementSibling;if(!sibling)return;root.insertBefore(direction<0?row:sibling,direction<0?sibling:row);markRulesDirty();});
  actions.append(button);
 }
 const remove=element('button','quiet','删除');remove.type='button';remove.addEventListener('click',()=>{row.remove();markRulesDirty();});actions.append(remove);head.append(actions);
 const fields=element('div','rule-row-fields');
 for(const [index,labelText,value] of [[0,kind==='pattern'?'匹配正则':'原始文本',first],[1,'替换为（留空删除）',second]]){
  const label=element('label','',labelText),input=element('textarea','');input.rows=2;input.spellcheck=false;input.value=value;input.dataset.ruleField=String(index);input.setAttribute('aria-label',labelText);input.addEventListener('input',markRulesDirty);label.append(input);fields.append(label);
 }
 row.append(head,fields);root.append(row);
}
function renderRules(data){
 rulesLoaded=true;rulesEditable=!!data.configured;rulesRevisionValue=data.revision||'';rulesDirty=false;
 $('rule-words').value=(data.rules?.words||[]).join('\n');
 $('words-count').textContent=(data.rules?.words||[]).length+' 条';
 $('rule-replacements').replaceChildren();$('rule-patterns').replaceChildren();
 for(const [key,value] of Object.entries(data.rules?.replacements||{}))addRuleRow('replacement',key,value);
 for(const rule of data.rules?.patterns||[])addRuleRow('pattern',rule.pattern,rule.replacement);
 $('rules-state').textContent=rulesEditable?'共用 CPA 配置':'未配置规则文件';
 $('rules-state').className='badge'+(rulesEditable?' connected':'');
 $('rules-save-state').textContent=rulesEditable?'已加载共享规则':'请先配置 cpa_config_file';
 promptControls();
}
async function loadRules(){renderRules(await request('/prompt-rules'));}
$('rule-words').addEventListener('input',markRulesDirty);
$('add-replacement').addEventListener('click',()=>{addRuleRow('replacement');markRulesDirty();});
$('add-pattern').addEventListener('click',()=>{addRuleRow('pattern');markRulesDirty();});
$('reload-rules').addEventListener('click',()=>action(loadRules));
$('prompt-rules-form').addEventListener('submit',event=>{event.preventDefault();action(async()=>{
 const rules={words:$('rule-words').value.split(/\r?\n/).map(x=>x.trim()).filter(Boolean),replacements:Object.create(null),patterns:[]};
 for(const row of $('rule-replacements').children){
  const fields=row.querySelectorAll('textarea'),key=fields[0].value;
  if(!key.trim())throw new Error('字面替换的原始文本不能为空。');
  if(Object.hasOwn(rules.replacements,key))throw new Error('字面替换存在重复的原始文本。');
  rules.replacements[key]=fields[1].value;
 }
 for(const row of $('rule-patterns').children){
  const fields=row.querySelectorAll('textarea');
  if(!fields[0].value.trim())throw new Error('匹配正则不能为空。');
  rules.patterns.push({pattern:fields[0].value,replacement:fields[1].value});
 }
 renderRules(await request('/prompt-rules','POST',{rules,revision:rulesRevisionValue}));
 $('rules-save-state').textContent='已保存到 CPA · 后续请求生效';notice('混淆规则已保存到 CPA 共享配置。');
});});
