const $=s=>document.querySelector(s), form=$('#task-form');let csrf='',tasks=[],filter='all',selected=new Set(),timezone='Asia/Shanghai';
const esc=s=>String(s??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const fmt=n=>n?new Intl.DateTimeFormat('zh-CN',{timeZone:timezone,year:'numeric',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',second:'2-digit',hour12:false}).format(new Date(n*1000)):'—';
function toast(s){$('#toast').textContent=s;$('#toast').hidden=false;clearTimeout(toast.timer);toast.timer=setTimeout(()=>$('#toast').hidden=true,4000)}
async function api(action,data){const r=await fetch('api.php?action='+action,{method:data?'POST':'GET',headers:data?{'Content-Type':'application/json','X-CSRF-Token':csrf}:{},body:data?JSON.stringify(data):undefined});const v=await r.json();if(!r.ok)throw Error(v.error||'请求失败');return v}
async function safe(fn){try{await fn()}catch(e){toast(e.message)}}
async function init(){const s=await api('session');csrf=s.csrf;$('#install').hidden=s.configured;$('#login').hidden=!s.configured||s.authenticated;$('#workspace').hidden=!s.authenticated;$('#logout').hidden=!s.authenticated;if(s.authenticated)await refresh()}
async function refresh(){const v=await api('list');tasks=v.tasks;timezone=v.timezone;selected=new Set([...selected].filter(id=>tasks.some(t=>t.id===id)));$('#total').textContent=tasks.length;$('#enabled').textContent=tasks.filter(t=>t.enabled).length;$('#failed').textContent=tasks.filter(t=>['failed','interrupted'].includes(t.last_status)).length;$('#runner-state').textContent=v.heartbeat?(Date.now()/1000-v.heartbeat<180?'● 执行器已连接':'○ 执行器未更新，请检查 cron')+' · '+fmt(v.heartbeat):'○ 执行器等待连接';render()}
function period(t){const minute=Number(t.at_time.split(':')[1]);return {daily:'每天 '+t.at_time,days:'每 '+t.interval_n+' 天 · '+t.at_time,hourly:'每小时 · 第 '+minute+' 分钟',hours:'每 '+t.interval_n+' 小时 · 第 '+minute+' 分钟',minutes:'每 '+t.interval_n+' 分钟',weekly:'每周'+['','一','二','三','四','五','六','日'][t.weekday]+' · '+t.at_time,monthly:'每月 '+t.monthday+' 日 · '+t.at_time,seconds:'每 '+t.interval_n+' 秒'}[t.period]+(t.schedule_version===1?'（原规则）':'')}
function visible(){return tasks.filter(t=>(filter==='all'||filter===t.type)&&t.name.toLowerCase().includes($('#search').value.toLowerCase()))}
function render(){const list=visible();$('#rows').innerHTML=list.map(t=>`<tr><td><input type="checkbox" data-check="${t.id}" ${selected.has(t.id)?'checked':''} aria-label="选择 ${esc(t.name)}"></td><td><strong title="${esc(t.name)}">${esc(t.name)}</strong><small>${t.type==='shell'?'⌘ SHELL 脚本':'↗ HTTP · '+t.method}</small></td><td>${period(t)}</td><td><button class="switch ${t.enabled?'':'off'}" data-action="toggle" data-id="${t.id}">${t.enabled?'已启用':'已暂停'}</button></td><td>${t.enabled?fmt(t.next_run):'—'}</td><td><span class="pill ${['failed','interrupted'].includes(t.last_status)?'failed':!t.last_status?'muted':''}">${t.requested?'等待执行':({success:'执行成功',failed:'执行失败',running:'执行中',interrupted:'执行中断'}[t.last_status]||'尚未执行')}</span><small>${fmt(t.last_run)}</small></td><td><div class="row-actions"><button data-action="run" data-id="${t.id}">执行</button><button data-action="edit" data-id="${t.id}">编辑</button><button data-action="logs" data-id="${t.id}">日志</button><button data-action="delete" data-id="${t.id}">删除</button></div></td></tr>`).join('');$('#empty').hidden=!!list.length;$('#count').textContent=`显示 ${list.length} / 共 ${tasks.length} 个任务`;$('#selection').textContent=`已选择 ${selected.size} 项`;$('#delete-selected').disabled=!selected.size;$('#check-all').checked=list.length>0&&list.every(t=>selected.has(t.id));$('#check-all').indeterminate=list.some(t=>selected.has(t.id))&&!$('#check-all').checked}
let previewTimer, previewSequence=0;
function scheduleData(){
 const data=Object.fromEntries(new FormData(form));
 const h=form.elements.schedule_hour.disabled?'00':String(Number(form.elements.schedule_hour.value)).padStart(2,'0');
 const m=form.elements.schedule_minute.disabled?'00':String(Number(form.elements.schedule_minute.value)).padStart(2,'0');
 if(data.type==='http' && data.method==='POST' && data.body_type==='form')data.body=JSON.stringify([...document.querySelectorAll('.param-row')].map(row=>({key:row.querySelector('.param-key').value,value:row.querySelector('.param-value').value})).filter(p=>p.key!==''||p.value!==''));
 return {...data,at_time:h+':'+m};
}
function requestPreview(){
 clearTimeout(previewTimer);const sequence=++previewSequence;
 $('#preview-next').textContent='正在计算…';$('#preview-following').textContent='';$('#preview-note').textContent='';
 previewTimer=setTimeout(async()=>{
  try {
   for(const name of ['interval_n','weekday','monthday','schedule_hour','schedule_minute']){const input=form.elements.namedItem(name);if(!input.disabled&&!input.checkValidity())throw Error('请填写有效的周期数值');}
   const data=scheduleData();const v=await api('preview',Object.fromEntries(['period','interval_n','at_time','weekday','monthday'].filter(k=>data[k]!==undefined).map(k=>[k,data[k]])));if(sequence!==previewSequence)return;
   const format=n=>new Intl.DateTimeFormat('zh-CN',{timeZone:v.timezone,year:'numeric',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',second:'2-digit',hour12:false}).format(new Date(n*1000));
   $('#preview-next').textContent=format(v.runs[0]);
   $('#preview-following').textContent='后续：'+v.runs.slice(1).map(format).join(' / ');
  }catch(e){if(sequence===previewSequence){$('#preview-next').textContent=e.message;$('#preview-note').textContent='调整参数后自动更新预览。';}}
 },200);
}
function addParam(key='',value=''){
 const row=document.createElement('div');row.className='param-row';
 row.innerHTML='<input class="param-key" aria-label="参数名称" placeholder="Key"><input class="param-value" aria-label="参数值" placeholder="Value"><button type="button" aria-label="删除参数">×</button>';
 row.querySelector('.param-key').value=key;row.querySelector('.param-value').value=value;
 row.querySelector('button').onclick=()=>row.remove();$('#param-rows').append(row);
}
function bodyFields(){
 const post=form.elements.type.value==='http'&&form.elements.method.value==='POST';
 $('#post-fields').hidden=!post;$('#form-body').hidden=!post||form.elements.body_type.value!=='form';$('#json-body').hidden=!post||form.elements.body_type.value!=='json';
}
$('#add-param').onclick=()=>addParam();form.elements.method.onchange=form.elements.body_type.onchange=bodyFields;
function fields(){
 bodyFields();
 const type=form.elements.type.value,p=form.elements.period.value;
 $('#shell-fields').hidden=type!=='shell';$('#http-fields').hidden=type!=='http';
 form.elements.script.required=type==='shell';form.elements.url.required=type==='http';
 const show={'interval-field':['days','hours','minutes','seconds'].includes(p),'weekday-field':p==='weekly','monthday-field':p==='monthly','hour-field':['daily','days','weekly','monthly'].includes(p),'minute-field':!['minutes','seconds'].includes(p)};
 for(const [id,visible] of Object.entries(show)){const el=$('#'+id);el.hidden=!visible;el.querySelectorAll('input,select').forEach(input=>input.disabled=!visible)}
 const max={days:31,hours:23,minutes:59,seconds:59}[p]||1;form.elements.interval_n.max=max;
 $('#interval-unit').textContent={days:'天',hours:'小时',minutes:'分钟',seconds:'秒'}[p]||'';
 $('#period-hint').textContent={daily:'每天在指定的小时、分钟执行。',days:'按每月日期步进：N=3 时在每月 1、4、7…日执行；月初重新计数。',hourly:'在每个小时的指定分钟执行。',hours:'从每天 0 点开始按 N 小时步进，在指定分钟执行。例如 3 小时 15 分钟：00:15、03:15、06:15…',minutes:'从每小时第 0 分钟开始按 N 分钟步进。例如 N=10：00、10、20、30、40、50 分。',weekly:'每星期的指定星期、小时、分钟执行。',monthly:'每月指定日期执行；当月没有该日期时跳过该月。',seconds:'每 N 秒执行。服务器需使用秒级执行器（部署说明中已提供），任务耗时会影响实际间隔。'}[p];
 requestPreview();
}
function edit(t){form.reset();$('#param-rows').replaceChildren();if(t?.body_type==='form'){for(const p of JSON.parse(t.body||'[]'))addParam(p.key,p.value)}else addParam();form.elements.id.value='';if(t)for(const [k,v]of Object.entries(t))if(form.elements.namedItem(k))form.elements.namedItem(k).value=v;const [h,m]=(t?.at_time||'02:00').split(':');form.elements.schedule_hour.value=Number(h);form.elements.schedule_minute.value=Number(m);$('#legacy-hint').hidden=t?.schedule_version!==1;$('#editor-title').textContent=t?'编辑任务':'添加任务';fields();$('#editor').showModal()}
$('#add').onclick=$('#empty-add').onclick=()=>edit();document.querySelectorAll('[data-close]').forEach(b=>b.onclick=()=>$('#'+b.dataset.close).close());form.elements.type.onchange=form.elements.period.onchange=fields;
form.addEventListener('input',e=>{if(['interval_n','weekday','monthday','schedule_hour','schedule_minute'].includes(e.target.name))requestPreview()});
form.onsubmit=e=>{e.preventDefault();safe(async()=>{await api('save',scheduleData());$('#editor').close();await refresh();toast('任务已保存')})};
$('#install-form').onsubmit=e=>{e.preventDefault();safe(async()=>{const data=Object.fromEntries(new FormData(e.target));if(data.password!==data.password_confirm)throw Error('两次输入的密码不一致');const button=e.target.querySelector('button');button.disabled=true;try{await api('install',data);e.target.reset();await init();toast('管理员设置完成')}finally{button.disabled=false}})};
$('#login-form').onsubmit=e=>{e.preventDefault();safe(async()=>{await api('login',Object.fromEntries(new FormData(e.target)));e.target.elements.password.value='';await init()})};$('#logout').onclick=()=>safe(async()=>{await api('logout',{});await init()});
$('#clear-logs').onclick=()=>safe(async()=>{if(!confirm('确定清空所有任务的执行日志？此操作无法撤销，任务和执行计划会保留；正在运行的任务完成后会产生新日志。'))return;const button=$('#clear-logs');button.disabled=true;try{const v=await api('clear_logs',{});$('#log-content').textContent='暂无执行记录。';toast('已清空 '+v.deleted+' 条执行日志')}finally{button.disabled=false}});
$('#search').oninput=render;document.querySelectorAll('[data-filter]').forEach(b=>b.onclick=()=>{filter=b.dataset.filter;document.querySelectorAll('[data-filter]').forEach(a=>a.classList.toggle('selected',a===b));render()});$('#refresh').onclick=()=>safe(refresh);
$('#check-all').onchange=e=>{visible().forEach(t=>e.target.checked?selected.add(t.id):selected.delete(t.id));render()};$('#rows').onchange=e=>{if(e.target.dataset.check){const id=Number(e.target.dataset.check);e.target.checked?selected.add(id):selected.delete(id);render()}};
async function remove(ids){if(!confirm(`确定删除 ${ids.length} 个任务及其日志？已启动的进程将继续运行。`))return;await api('delete',{ids});ids.forEach(id=>selected.delete(id));await refresh();toast('任务已删除')}
$('#delete-selected').onclick=()=>safe(()=>remove([...selected]));$('#rows').onclick=e=>safe(async()=>{const b=e.target.closest('[data-action]');if(!b)return;const id=Number(b.dataset.id),t=tasks.find(t=>t.id===id);switch(b.dataset.action){case 'edit':edit(t);return;case 'delete':await remove([id]);return;case 'logs':{const v=await api('logs&id='+id);$('#log-title').textContent=t.name+' · 执行日志';$('#log-content').innerHTML=v.logs.length?v.logs.map((l,i)=>`<details ${i===0?'open':''}><summary>${fmt(l.started_at)} · ${l.status==='success'?'成功':'失败'} · ${l.duration}s</summary><pre>${esc(l.output)}</pre></details>`).join(''):'<p>暂无执行记录。任务执行后，日志会显示在这里。</p>';$('#logs').showModal();return}case 'toggle':await api('toggle',{ids:[id],enabled:!t.enabled});break;case 'run':await api('run',{ids:[id]});toast('已加入执行队列，cron 下次检查时执行');break}await refresh()});
safe(init);setInterval(()=>{if(!$('#workspace').hidden&&!document.hidden)safe(refresh)},15000);

let logPage=1, logLoading=false;
const logStatus=s=>({success:'成功',failed:'失败',interrupted:'执行中断',running:'执行中'}[s]||s);
async function loadAllLogs(page=1){
 if(logLoading)return;
 logLoading=true;$('#log-prev').disabled=$('#log-next').disabled=$('#log-refresh').disabled=true;
 try{
  const v=await api('all_logs&page='+page);logPage=v.page;
  $('#all-log-rows').innerHTML=v.logs.map(l=>`<tr data-log-id="${l.id}"><td><button class="log-name" data-log-id="${l.id}" title="${esc(l.name)}">${esc(l.name)}</button></td><td>${fmt(l.started_at)}</td><td>${Number(l.duration).toFixed(3)}s</td><td><span class="pill ${l.status==='success'?'':'failed'}">${esc(logStatus(l.status))}</span></td></tr>`).join('');
  $('#all-log-empty').hidden=v.logs.length>0;$('#log-page').textContent='第 '+logPage+' 页';
  $('#log-prev').disabled=logPage<=1;$('#log-next').disabled=!v.has_more;
 }finally{logLoading=false;$('#log-refresh').disabled=false}
}
$('#all-logs').onclick=()=>safe(async()=>{await loadAllLogs();$('#all-log-list').showModal()});
$('#log-prev').onclick=()=>safe(()=>loadAllLogs(logPage-1));$('#log-next').onclick=()=>safe(()=>loadAllLogs(logPage+1));$('#log-refresh').onclick=()=>safe(()=>loadAllLogs());
$('#all-log-rows').onclick=e=>safe(async()=>{
 const row=e.target.closest('[data-log-id]');if(!row)return;
 const l=await api('log_detail&id='+row.dataset.logId);
 $('#detail-title').textContent=l.name;$('#detail-meta').textContent=fmt(l.started_at)+' · '+Number(l.duration).toFixed(3)+'s · '+logStatus(l.status);$('#detail-output').textContent=l.output||'（无输出）';$('#log-detail').showModal();
});
