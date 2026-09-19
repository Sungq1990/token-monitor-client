/* Wails 注入 window.go.main.App.* 与 window.runtime.* */
const API = () => window.go?.main?.App;
const AGENT_LABEL = {claude:"Claude Code", opencode:"OpenCode", zcode:"ZCode", codex:"Codex"};
const APATH_FIELDS = {claude:["projects_dir","file_glob"],codex:["projects_dir","file_glob"],opencode:["db_file"],zcode:["db_file"]};
const APATH_LABEL = {projects_dir:"会话目录",file_glob:"文件匹配",db_file:"数据库文件"};
const $ = id => document.getElementById(id);
const esc = s => (s??"").toString().replace(/[&<>"']/g, m=>({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"}[m]));
const shortModel = m => m ? m.split("/").pop() : "—";

let info = null, cfg = null, agentDrafts = null, lastStatus = null;

function setErr(msg){ $("err").textContent = msg || ""; }
function msg(id, text, cls){ const el=$(id); el.textContent=text; el.className="hint"+(cls?" "+cls:""); }
/* 按钮 loading：setBusy(btn, true, "文案") / setBusy(btn, false) 恢复 */
function setBusy(btn, on, text){
  if(on){ btn.dataset.orig=btn.textContent; btn.disabled=true; btn.textContent=text||"处理中…"; }
  else { btn.disabled=false; if(btn.dataset.orig){ btn.textContent=btn.dataset.orig; delete btn.dataset.orig; } }
}
/* 顶部悬浮通知：setNet("ok"|"bad", 文案)；ok 自动消失 */
function setNet(cls, text){
  const b=$("netBanner"); if(!b) return;
  b.className="netbanner show "+cls; b.textContent=text;
  clearTimeout(b._t);
  if(cls==="ok") b._t=setTimeout(()=>{ b.className="netbanner"; }, 4000);
}

/* ---- Tabs ---- */
document.querySelectorAll(".tab").forEach(t=>t.onclick=()=>{
  document.querySelectorAll(".tab").forEach(x=>{const on=x===t;x.classList.toggle("active",on);x.setAttribute("aria-selected",String(on));});
  document.querySelectorAll(".pane").forEach(p=>{const on=p.dataset.pane===t.dataset.tab;p.classList.toggle("active",on);p.hidden=!on;});
  if(t.dataset.tab==="pricing") loadPricing();
  if(t.dataset.tab==="agents" && agentDrafts===null) loadAgents();
});

/* ---- 服务端与设备 ---- */
function fillServer(){
  $("serverUrl").value = cfg.server_url || "";
  $("deviceId").value = cfg.device_id || "";
  $("deviceName").value = cfg.device_name || "";
  $("interval").value = cfg.interval_minutes ?? 5;
  $("rescan").value = cfg.rescan_window_seconds ?? 7200;
  $("startMinimized").checked = !!cfg.start_minimized;
}
function collectServer(){
  const c = {...cfg};
  c.server_url = $("serverUrl").value.trim();
  c.device_id = $("deviceId").value.trim();
  c.device_name = $("deviceName").value.trim();
  c.interval_minutes = parseInt($("interval").value,10);
  c.rescan_window_seconds = parseInt($("rescan").value,10);
  c.start_minimized = $("startMinimized").checked;
  return c;
}
$("btnTest").onclick = async () => {
  const b=$("btnTest"); b.disabled=true; msg("testMsg","正在连接…");
  try{
    const h = await API().TestServer($("serverUrl").value);
    msg("testMsg",`连接成功：服务端 v${h.version||"?"}，已有 ${h.devices??0} 台设备（${h.online??0} 台在线）`,"ok");
  }catch(e){ msg("testMsg","连接失败："+(e?.message||e),"bad"); }
  finally{ b.disabled=false; }
};
$("btnNewId").onclick = async () => {
  if(!confirm("重新生成设备标识后，服务端会把本机当成一台新设备，之前的数据留在旧标识下。确定？")) return;
  $("deviceId").value = await API().NewDeviceID();
};
$("btnSave").onclick = async () => {
  const b=$("btnSave"); setBusy(b,true,"正在保存并同步…"); msg("saveMsg","正在保存…");
  const c = collectServer();
  const oldId = cfg.device_id;
  if(c.device_id!==oldId && !confirm("设备标识已修改。保存后会清空本地游标并全量重传到新标识下，确定？")){ setBusy(b,false); msg("saveMsg",""); return; }
  try{
    await API().SaveConfig(c);
    cfg = await API().GetConfig(); fillServer();
    msg("saveMsg","已保存并同步到服务端，设备已注册，正在同步…","ok");
    setNet("ok","设置已保存并同步到服务端");
  }catch(e){
    // 保存成功但注册失败也会抛错：重新拉配置确认
    cfg = await API().GetConfig(); fillServer();
    msg("saveMsg",(e?.message||String(e)),"bad");
    setNet("bad","保存失败："+(e?.message||e));
  }finally{ setBusy(b,false); }
};

/* ---- Agent 路径 ---- */
function pathInput(a, v, i){
  return `<div class="path-entry"><input data-path="${i}" value="${esc(v)}" placeholder="例如 ~/.claude 或 C:\\Users\\你\\.claude" spellcheck="false"><span class="st" data-st="${i}"></span><button type="button" class="btn small" data-action="pick" data-index="${i}" title="选择目录">…</button><button type="button" class="btn small danger" data-action="remove-path" data-index="${i}">移除</button></div>`;
}
function renderAgents(){
  const specs = info?.collectors || [];
  $("agentList").innerHTML = agentDrafts.map((a,i)=>{
    const spec = specs.find(s=>s.key===a.agent);
    const fields = APATH_FIELDS[a.agent] || ["projects_dir","file_glob","db_file"];
    return `<article class="acard" data-card="${i}">
      <div class="acard-head"><strong>${esc(AGENT_LABEL[a.agent]||a.agent||"新 Agent 配置")}</strong>
        <span class="pill ${spec?"":"warn"}">${spec?"已支持解析":"待适配 · 暂不采集"}</span>
        ${a.fresh?'<button class="btn small" data-action="remove-draft">取消新增</button>':'<button class="btn small" data-action="probe">检测路径</button><button class="btn small danger" data-action="remove-agent" title="从配置中删除该 Agent">删除</button>'}
        <label><input type="checkbox" data-field="enabled" ${a.enabled?"checked":""}>启用采集</label></div>
      <div class="grid"><label class="f">Agent 名称<input data-field="agent" maxlength="32" value="${esc(a.agent)}" ${a.fresh?"":"readonly"} placeholder="claude / codex / opencode / zcode 或自定义" list="agentKinds"></label>
        <label class="f">默认位置<input readonly value="${esc((spec?.default_paths||[]).join("  ;  ")||"—")}" title="本机该 Agent 的默认数据目录"></label></div>
      <div class="path-heading"><b>数据根目录</b><button class="btn small" data-action="add-path">＋ 添加路径</button></div>
      <div class="path-entries">${a.paths.map((p,j)=>pathInput(a,p,j)).join("")||'<div class="hint">尚未添加路径；启用前至少添加一条。</div>'}</div>
      <div class="hint" data-probe="${i}"></div>
      <details class="adv"><summary>高级路径选项（相对于根目录，留空用默认）</summary><div class="grid">${fields.map(f=>`<label class="f">${APATH_LABEL[f]}<input data-field="${f}" value="${esc(a[f]||"")}" placeholder="${esc(spec?.defaults?.[f]||"")}"></label>`).join("")}</div></details>
    </article>`;
  }).join("") || '<div class="hint">还没有 Agent 配置，点击上方按钮添加。</div>';
  if(!$("agentKinds")){ const dl=document.createElement("datalist"); dl.id="agentKinds"; dl.innerHTML=specs.map(s=>`<option value="${esc(s.key)}">${esc(s.label)}</option>`).join(""); document.body.appendChild(dl); }
}
function loadAgents(){
  agentDrafts = (cfg.agents||[]).map(a=>({...a, paths:[...(a.paths||[])], fresh:false}));
  renderAgents();
  agentDrafts.forEach((_,i)=>probe(i));
}
async function probe(i, btn){
  const a = agentDrafts[i]; if(!a || a.fresh) return;
  const label = AGENT_LABEL[a.agent]||a.agent;
  if(btn) setBusy(btn, true, "检测中…");
  let box = document.querySelector(`[data-probe="${i}"]`);
  if(box) box.textContent = "正在检测路径与数据源…";
  try{
    const r = await API().ProbePaths(a.agent, {agent:a.agent, enabled:a.enabled, paths:a.paths, projects_dir:a.projects_dir||"", file_glob:a.file_glob||"", db_file:a.db_file||""});
    const card = document.querySelector(`[data-card="${i}"]`); if(!card) return;
    a.paths.forEach((p,j)=>{ const st=card.querySelector(`[data-st="${j}"]`); if(!st) return; const ok=r.exists?.[p]; st.textContent = ok?"存在":"不存在"; st.className="st "+(ok?"ok":"bad"); });
    box = document.querySelector(`[data-probe="${i}"]`);
    if(!r.supported){
      if(box) box.textContent = "该名称没有解析器，保存后不会采集。";
      setNet("bad", label+"：没有解析器，保存后不会采集");
      return;
    }
    const n = r.sources?.length || 0;
    if(box) box.textContent = n ? `发现 ${n} 个数据源：${r.sources.slice(0,3).join("、")}${n>3?" …":""}` : "未发现数据源：请检查路径或该 Agent 尚未产生记录。";
    if(n) setNet("ok", label+`：检测完成，发现 ${n} 个数据源 ✓`);
    else setNet("bad", label+"：未发现数据源，请检查路径是否正确");
  }catch(e){
    if(box) box.textContent = "";
    setNet("bad", "检测失败："+(e?.message||e));
  }finally{ if(btn) setBusy(btn, false); }
}
$("btnAddAgent").onclick = () => {
  if(agentDrafts===null) loadAgents();
  agentDrafts.push({agent:"",enabled:true,paths:[""],projects_dir:"",file_glob:"",db_file:"",fresh:true});
  renderAgents();
  document.querySelector(`[data-card="${agentDrafts.length-1}"] [data-field="agent"]`)?.focus();
};
const alist = $("agentList");
alist.addEventListener("input", e=>{
  const card=e.target.closest("[data-card]"); if(!card) return;
  const a=agentDrafts[Number(card.dataset.card)], f=e.target.dataset.field;
  if(e.target.hasAttribute("data-path")) a.paths[Number(e.target.dataset.path)] = e.target.value;
  else if(f) a[f] = f==="enabled" ? e.target.checked : e.target.value;
  msg("agentsMsg","有未保存的修改");
});
alist.addEventListener("change", e=>{
  const card=e.target.closest("[data-card]"); if(!card) return;
  const a=agentDrafts[Number(card.dataset.card)];
  if(e.target.dataset.field==="agent" && a.fresh){
    a.agent = e.target.value.trim();
    const spec = (info?.collectors||[]).find(s=>s.key===a.agent);
    if(spec && a.paths.every(p=>!p.trim())) a.paths = [...spec.default_paths];
    renderAgents();
  }
});
alist.addEventListener("click", async e=>{
  const btn=e.target.closest("[data-action]"); if(!btn) return;
  const i=Number(btn.closest("[data-card]").dataset.card), a=agentDrafts[i];
  const act=btn.dataset.action;
  if(act==="add-path") a.paths.push("");
  if(act==="remove-path") a.paths.splice(Number(btn.dataset.index),1);
  if(act==="remove-draft" && a.fresh) agentDrafts.splice(i,1);
  if(act==="remove-agent" && !a.fresh){
    if(!confirm(`删除「${AGENT_LABEL[a.agent]||a.agent}」的采集配置？保存后服务端也会同步删除，已上报的数据不受影响。`)) return;
    agentDrafts.splice(i,1);
  }
  if(act==="probe"){ await probe(i, btn); return; }
  if(act==="pick"){
    try{ const dir = await API().PickDirectory("选择 "+(AGENT_LABEL[a.agent]||a.agent)+" 的数据根目录"); if(dir){ a.paths[Number(btn.dataset.index)] = dir; } else return; }catch(_){ return; }
  }
  renderAgents(); msg("agentsMsg","有未保存的修改");
});
$("btnSaveAgents").onclick = async () => {
  if(agentDrafts===null) return;
  const b=$("btnSaveAgents"); setBusy(b,true,"正在保存并同步…");
  const agents = agentDrafts.map(({fresh,...a})=>({agent:a.agent.trim(), enabled:!!a.enabled, paths:a.paths.map(p=>p.trim()).filter(Boolean), projects_dir:(a.projects_dir||"").trim(), file_glob:(a.file_glob||"").trim(), db_file:(a.db_file||"").trim()}));
  try{
    const c = collectServer(); c.agents = agents;
    await API().SaveConfig(c);
    cfg = await API().GetConfig(); loadAgents();
    const unsupported = agents.filter(a=>a.enabled && !(info?.collectors||[]).some(s=>s.key===a.agent)).map(a=>a.agent);
    const m = unsupported.length ? "已保存；以下 Agent 待适配，暂不采集："+unsupported.join("、") : "已保存，正在按新配置采集。";
    msg("agentsMsg", m, unsupported.length?"":"ok");
    setNet("ok","Agent 配置已保存并同步到服务端");
  }catch(e){ msg("agentsMsg", e?.message||String(e), "bad"); setNet("bad","保存失败："+(e?.message||e)); }
  finally{ setBusy(b,false); }
};

/* ---- 模型单价 ---- */
async function loadPricing(){
  const box=$("pricingList"); msg("pricingMsg","");
  try{
    const list = await API().GetPricing() || [];
    if(!list.length){ box.innerHTML='<div class="hint">服务端暂无模型数据，等采集上报后再来设置。</div>'; return; }
    const byAgent = new Map();
    for(const p of list){ const a=byAgent.get(p.agent)||new Map(); const prov=a.get(p.provider||"other")||[]; prov.push(p); a.set(p.provider||"other",prov); byAgent.set(p.agent,a); }
    const inp=(p,f,label)=>`<label>${label}</label><input type="number" step="0.01" min="0" data-agent="${esc(p.agent)}" data-model="${esc(p.model)}" data-f="${f}" value="${p[f]??0}">`;
    let html="";
    for(const [agentName,provs] of byAgent){
      const n=[...provs.values()].reduce((s,x)=>s+x.length,0);
      html+=`<div class="phead" data-toggle="1"><span class="caret">▶</span>${esc(AGENT_LABEL[agentName]||agentName)} <span class="pcnt">${provs.size} 个服务商 / ${n} 个模型</span></div><div hidden>`;
      for(const [prov,models] of provs){
        html+=`<div class="phead2" data-toggle="1"><span class="caret">▶</span>${esc(prov)} <span class="pcnt">${models.length} 个模型</span></div><div hidden>${models.map(p=>`<div class="prow"><span class="pname" title="${esc(p.model)}">${esc(shortModel(p.model))}${p.configured?"":' <span class="pcnt">(未配置)</span>'}</span>${inp(p,"input_per_m","输入")}${inp(p,"output_per_m","输出")}${inp(p,"cache_read_per_m","缓存读")}${inp(p,"cache_write_per_m","缓存写")}</div>`).join("")}</div>`;
      }
      html+=`</div>`;
    }
    box.innerHTML=html;
  }catch(e){ box.innerHTML=""; msg("pricingMsg","加载失败："+(e?.message||e)+"（请先确认服务端地址正确并已保存）","bad"); }
}
$("pricingList").addEventListener("click", e=>{
  const head=e.target.closest("[data-toggle]"); if(!head) return;
  const body=head.nextElementSibling; if(!body) return;
  const show=body.hidden; body.hidden=!show; head.querySelector(".caret")?.classList.toggle("open",show);
});
$("btnReloadPricing").onclick = loadPricing;
$("btnSavePricing").onclick = async () => {
  const items = Object.values([...document.querySelectorAll("#pricingList input")].reduce((acc,inp)=>{
    const k=inp.dataset.agent+"\u0000"+inp.dataset.model;
    acc[k] = Object.assign(acc[k]||{agent:inp.dataset.agent, model:inp.dataset.model, input_per_m:0, output_per_m:0, cache_read_per_m:0, cache_write_per_m:0}, {[inp.dataset.f]: Number(inp.value)||0});
    return acc;
  },{}));
  const b=$("btnSavePricing"); b.disabled=true;
  try{ await API().SavePricing(items); msg("pricingMsg","已保存到服务端，面板费用已按新单价重算","ok"); }
  catch(e){ msg("pricingMsg","保存失败："+(e?.message||e),"bad"); }
  finally{ b.disabled=false; }
};

/* ---- 运行状态 ---- */
function renderStatus(s){
  lastStatus = s;
  const cs=$("connStatus"); cs.classList.remove("ok","error","busy");
  if(s.syncing){ cs.classList.add("busy"); $("connText").textContent="正在采集上报…"; }
  else if(s.connected){ cs.classList.add("ok"); $("connText").textContent = "已连接服务端"+(s.server_version?" v"+s.server_version:"")+(s.last_success_at?" · 上次成功 "+s.last_success_at:""); }
  else { cs.classList.add("error"); $("connText").textContent = "服务端不可达"+(s.server_error?"："+s.server_error:""); }
  $("stLastRun").textContent = s.last_run_at||"—"; $("stLastOk").textContent = s.last_success_at||"—"; $("stNext").textContent = s.next_run_at||"—"; $("stTotal").textContent = (s.total_uploaded||0).toLocaleString();
  const rows = Object.values(s.agents||{}).sort((a,b)=>a.agent<b.agent?-1:1);
  document.querySelector("#agentStatus tbody").innerHTML = rows.map(a=>`<tr><td>${esc(AGENT_LABEL[a.agent]||a.agent)}</td><td>${!a.enabled?'<span class="muted">已停用</span>':!a.supported?'<span class="muted">待适配</span>':a.error?`<span class="bad" title="${esc(a.error)}">错误</span>`:'<span class="ok">正常</span>'}</td><td title="${esc((a.sources||[]).join("\n"))}">${(a.sources||[]).length}</td><td>${a.usage_rows||0}</td><td>${a.last_sync_at||"—"}</td><td><button class="btn small" data-reset="${esc(a.agent)}" title="清空游标，下轮全量重扫（服务端去重，不会重复累计）">重扫</button></td></tr>`).join("") || '<tr><td colspan="6" class="muted">尚未运行</td></tr>';
  $("logBox").textContent = (s.log||[]).slice(-200).join("\n") || "（暂无）";
}
document.querySelector("#agentStatus").addEventListener("click", async e=>{
  const b=e.target.closest("[data-reset]"); if(!b) return;
  if(!confirm(`清空「${AGENT_LABEL[b.dataset.reset]||b.dataset.reset}」的本地游标并全量重扫？`)) return;
  setBusy(b,true,"排队中…");
  try{
    await API().ResetCursors(b.dataset.reset);
    b.textContent="已排队";
    setNet("ok","已清空游标并触发重扫，服务端去重不会重复累计");
    setTimeout(()=>setBusy(b,false), 3000);
  }catch(err){ setBusy(b,false); setNet("bad","重扫失败："+(err?.message||err)); }
});
$("btnSync").onclick = async () => {
  const b=$("btnSync"); setBusy(b,true,"同步中…");
  try{
    await API().SyncNow();
    const t0=Date.now(); let s;
    do{ await new Promise(r=>setTimeout(r,800)); s=await API().GetStatus(); }while(s.syncing && Date.now()-t0<90000);
    renderStatus(s);
    if(!s.connected) setNet("bad","服务端不可达："+(s.server_error||"未知错误"));
    else{
      const errs=Object.values(s.agents||{}).filter(a=>a.error);
      const n=Object.values(s.agents||{}).reduce((x,a)=>x+(a.usage_rows||0),0);
      if(errs.length) setNet("bad","同步完成但有失败："+errs.map(a=>(AGENT_LABEL[a.agent]||a.agent)+"："+a.error).join("；"));
      else setNet("ok",`同步完成：本轮上报 ${n} 条用量`);
    }
  }catch(e){ setNet("bad","同步失败："+(e?.message||e)); }
  finally{ setBusy(b,false); }
};
$("btnOpenPanel").onclick = () => { const u=(cfg?.server_url||"").replace(/\/$/,""); if(u) API().OpenURL(u+"/"); };

/* ---- 启动 ---- */
function hideSplash(){
  const s=document.getElementById("splash");
  if(!s || s.classList.contains("done")) return;
  s.classList.add("done");
  setTimeout(()=>s.remove(), 700);
}
async function init(){
  // 开屏动画至少展示 900ms，避免一闪而过；异常也要能退出开屏
  const t0=Date.now();
  const later=()=>setTimeout(hideSplash, Math.max(0, 900-(Date.now()-t0)));
  setTimeout(hideSplash, 8000); // 兜底
  if(!API()){ later(); setErr("未检测到 Wails 运行时。请通过桌面客户端打开此页面。"); return; }
  try{
    info = await API().GetInfo();
    cfg = await API().GetConfig();
    fillServer();
    renderStatus(await API().GetStatus());
    later();
    window.runtime?.EventsOn("status", renderStatus);
    // 服务端下发的配置被采纳后（如新装客户端首次连上服务端），刷新设置页
    window.runtime?.EventsOn("config", async (c)=>{
      if(!c) return;
      cfg = c;
      fillServer();
      if(agentDrafts!==null) loadAgents();
      msg("saveMsg","已采纳服务端下发的配置","ok");
    });
    // 心跳探测结果：断线/恢复的页面提示（断线弹窗由 Go 侧原生对话框负责）
    window.runtime?.EventsOn("serverdown", (e)=>setNet("bad","服务端连接断开："+e+"（每分钟自动重试）"));
    window.runtime?.EventsOn("serverup", ()=>setNet("ok","服务端连接已恢复 ✓"));
    setInterval(async()=>{ try{ renderStatus(await API().GetStatus()); }catch(_){} }, 5000);
  }catch(e){ setErr("初始化失败："+(e?.message||e)); }
}
init();
