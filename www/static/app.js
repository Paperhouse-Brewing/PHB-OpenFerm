const i18n = {
  en: { 
	  Units:"Units", 
	  Language:"Language", 
	  Fermenters:"Fermenters", 
	  Beer:"Beer", 
	  Target:"Target", 
	  Valve:"Valve", 
	  SetTarget:"Set Target", 
	  Alarms:"Alarms", 
	  Apply:"Apply",
	  Profiles:"Profiles", 
	  SavedProfiles:"Saved Profiles", 
	  CreateProfile:"Create Profile",
	  Name:"Name", 
	  Steps:"Steps", 
	  Type:"Type", 
	  Duration:"Duration", 
	  Rate:"Rate (°/h)",
	  AddStep:"Add step", 
	  SaveProfile:"Save profile", 
	  AssignToFV:"Assign to selected FV",
	  CancelActive:"Cancel active", 
	  ActiveProfile:"Active Profile:", 
	  None:"None" 
  },
  es: { 
	  Units:"Unidades", 
	  Language:"Idioma", 
	  Fermenters:"Fermentadores", 
	  Beer:"Cerveza", 
	  Target:"Objetivo", 
	  Valve:"Válvula", 
	  SetTarget:"Ajustar objetivo", 
	  Alarms:"Alarmas", 
	  Apply:"Aplicar",
	  Profiles:"Perfiles", 
	  SavedProfiles:"Perfiles guardados", 
	  CreateProfile:"Crear perfil",
	  Name:"Nombre", 
	  Steps:"Pasos", 
	  Type:"Tipo", 
	  Duration:"Duración", 
	  Rate:"Tasa (°/h)",
	  AddStep:"Añadir paso", 
	  SaveProfile:"Guardar perfil", 
	  AssignToFV:"Asignar al FV seleccionado",
	  CancelActive:"Cancelar activo", 
	  ActiveProfile:"Perfil activo:", 
	  None:"Ninguno"
  }
};

let FV_LIST = [];
let CHART_INIT = false;
let HISTORY_WIRED = false;
let HISTORY_INFLIGHT = false;
let HISTORY_LAST_FETCH = 0;
const HISTORY_MIN_PERIOD_MS = 15000; // fetch history at most every 15s


let PREFS = (() => {
  try {
    return Object.assign(
      { units:"C", locale:"en", labelC:"°C", labelF:"°F" },
      JSON.parse(localStorage.getItem("phb_prefs") || "{}")
    );
  } catch { return { units:"C", locale:"en", labelC:"°C", labelF:"°F" }; }
})();

function durToSec(s){
  if(s.endsWith('h')) return parseInt(s)*3600;
  if(s.endsWith('m')) return parseInt(s)*60;
  if(s.endsWith('s')) return parseInt(s);
  return 6*3600; // default 6h
}

function niceStep(secondsRange){
  // choose ~300 buckets max
  const target = 300;
  const raw = secondsRange/target;
  const candidates = [10,15,30,60,120,300,600,900,1800,3600];
  for(const c of candidates){ if(raw <= c) return c; }
  return 3600;
}

function getCurrentFV(){
  return (document.getElementById('selFVMain')?.value)
      || (document.getElementById('selFV')?.value)
      || (FV_LIST[0] || 'fv1');
}

function updateFVSelectors(st){
  const sels = ['selFV','selFVMain']
    .map(id => document.getElementById(id))
    .filter(Boolean);
  if (sels.length === 0) return false;

  const ids = st.fv.map(f => f.id);
  let changed = false;

  for (const sel of sels) {
    const prev = sel.value;
    sel.innerHTML = '';
    for (const f of st.fv) {
      const opt = document.createElement('option');
      opt.value = f.id;
      opt.textContent = (f.name || f.id) + ' (' + f.id + ')';
      sel.appendChild(opt);
    }
    if (ids.includes(prev)) sel.value = prev;
    else if (ids.length) sel.value = ids[0];
    changed = changed || (sel.value !== prev);
  }

  // keep selects in sync when user changes either
  for (const sel of sels) {
    sel.onchange = () => {
      for (const other of sels) if (other !== sel) other.value = sel.value;
      loadHistory();
      refreshActiveProfile();
      if (typeof updateActiveLabel === 'function') updateActiveLabel();
    };
  }

  FV_LIST = ids;
  return changed;
}


// very small canvas line plotter
function drawSeries(canvas, series, unitsLabel, events){
    const ctx = canvas.getContext('2d');
    const W = canvas.width, H = canvas.height;
    ctx.clearRect(0,0,W,H);

    // Basic guard
    if(!series || series.length === 0){
        ctx.fillStyle = '#555'; ctx.font = '12px system-ui';
        ctx.fillText('No data', 10, 20);
        return;
    }

    // Time & Y ranges
    const tMin = series[0].t, tMax = series[series.length-1].t;
    let yMin = Infinity, yMax = -Infinity;
    for(const p of series){
        yMin = Math.min(yMin, p.beer_c, p.target_c);
        yMax = Math.max(yMax, p.beer_c, p.target_c);
    }
    if(yMin === yMax){ yMin -= 0.5; yMax += 0.5; }

    const L=55, R=10, T=10, B=30;
    const X = t => L + ((t - tMin) / Math.max(1,(tMax - tMin))) * (W - L - R);
    const Y = v => H - B - ((v - yMin) / Math.max(1e-6,(yMax - yMin))) * (H - T - B);

    // ---------- helpers for events ----------
    function parseIntSafe(s){ const n = parseInt(s,10); return Number.isFinite(n) ? n : 0; }

    // Build override bands from events (override_set / override_clear)
    function buildOverrideBands(evts){
        const bands = [];
        // We’ll consider each "override_set" as start and use its data (unix end).
        // If there’s no end in data, fall back to the next override_clear.
        const clears = evts.filter(e => e.type === 'override_clear').map(e => e.t).sort((a,b)=>a-b);

        for(const e of evts){
            if(e.type !== 'override_set') continue;
            const start = e.t;
            let end = parseIntSafe(e.data);
            if (end <= 0) {
                // find the first clear after start
                const c = clears.find(c => c >= start);
                end = c || start; // if missing, draw a hairline only
            }
            if (end > start) bands.push({start, end});
        }
        return bands;
    }

    // Separate point events we want to mark (mode_change, etc.)
    function pickPointMarks(evts){
        return evts.filter(e => e.type === 'mode_change' || e.type === 'target_set');
    }

    // ---------- draw axes ----------
    ctx.strokeStyle = '#bbb'; ctx.beginPath();
    ctx.moveTo(L,T); ctx.lineTo(L,H-B); ctx.lineTo(W-R,H-B); ctx.stroke();

    ctx.fillStyle = '#666'; ctx.font = '12px system-ui';
    ctx.fillText(unitsLabel, 8, 12);

    // horizontal grid + value labels
    ctx.strokeStyle = '#eee';
    ctx.fillStyle = '#666';
    for(let i=0;i<=5;i++){
        const y = T + i*(H-T-B)/5;
        ctx.beginPath(); ctx.moveTo(L,y); ctx.lineTo(W-R,y); ctx.stroke();
        const v = yMax - i*(yMax-yMin)/5;
        ctx.fillText(v.toFixed(1), 4, y+4);
    }

    // x labels
    for(let i=0;i<=5;i++){
        const t = tMin + i*(tMax-tMin)/5;
        const d = new Date(t*1000);
        const label = d.toLocaleTimeString([], {hour:'2-digit', minute:'2-digit'});
        const x = X(t);
        ctx.fillText(label, x-18, H-10);
    }

    // ---------- overlays: override bands (behind lines) ----------
    const ev = Array.isArray(events) ? events : [];
    const bands = buildOverrideBands(ev);
    if (bands.length){
        ctx.fillStyle = 'rgba(200,200,255,0.25)'; // soft blue-ish band
        for(const b of bands){
            const x1 = X(b.start), x2 = X(b.end);
            ctx.fillRect(Math.min(x1,x2), T, Math.abs(x2-x1), H-T-B);
        }
    }

    // ---------- data lines ----------
    function line(color, getY){
        ctx.beginPath();
        let first=true;
        ctx.strokeStyle = color;
        for(const p of series){
            const x = X(p.t), y = getY(p);
            if(first){ ctx.moveTo(x,y); first=false; } else { ctx.lineTo(x,y); }
        }
        ctx.stroke();
    }

    line('#2d7', p => Y(p.beer_c));   // beer
    line('#36c', p => Y(p.target_c)); // target

    // ---- Event overlays ----
    // simple palette by type
    const colorFor = (typ) => {
        if (!typ) return '#888';
        if (typ.startsWith('alarm')) return '#d33';
        switch(typ){
            case 'mode_change':     return '#a3f';
            case 'target_set':      return '#08a';
            case 'profile_step':    return '#7a0';
            case 'override_set':    return '#e80';
            case 'override_clear':  return '#555';
            default:                return '#888';
        }
    };

    if (Array.isArray(events) && events.length) {
        ctx.save();
        ctx.globalAlpha = 0.85;
        for (const ev of events) {
            const t = ev.t|0;
            if (t < tMin || t > tMax) continue;
            const x = Math.round(X(t)) + 0.5;

            // vertical line
            ctx.strokeStyle = colorFor(ev.type);
            ctx.beginPath(); ctx.moveTo(x, T); ctx.lineTo(x, H-B); ctx.stroke();

            // tiny tick at bottom
            ctx.beginPath(); ctx.moveTo(x, H-B); ctx.lineTo(x, H-B+4); ctx.stroke();
        }
        ctx.restore();

        // legend (compact)
        const legend = [
            ['Beer', '#2d7'],
            ['Target', '#36c'],
            //['Alarm', colorFor('alarm')],
            ['Mode',  colorFor('mode_change')],
            ['Target set', colorFor('target_set')],
            ['Profile step', colorFor('profile_step')],
            ['Override', colorFor('override_set')],
        ];
        let lx = W - R - 140, ly = T + 6;
        ctx.font = '11px system-ui';
        for (const [name,col] of legend) {
            ctx.fillStyle = col; ctx.fillRect(lx, ly-8, 10, 3);
            ctx.fillStyle = '#333'; ctx.fillText(name, lx+14, ly-2);
            ly += 14;
        }
    }

    // ---------- legend ----------
    const legend = [
        {c:'#2d7', label:'Beer'},
        {c:'#36c', label:'Target'},
        {c:'rgba(200,200,255,0.25)', label:'Override active'},
    ];
    let lx=L+4, ly=T+16;
    for(const it of legend){
        // color box / swatch
        ctx.fillStyle = it.c;
        ctx.fillRect(lx, ly, 10, 10);
        ctx.fillStyle = '#333';
        ctx.fillText(it.label, lx+14, ly+10);
        lx += 110;
    }
}


function cToF(c){ return c*9/5+32; }
function fToC(f){ return (f-32)*5/9; }
function fmtTemp(c){
  return (PREFS.units==="F" ? cToF(c).toFixed(2)+" "+PREFS.labelF : c.toFixed(2)+" "+PREFS.labelC);
}
function applyLocale(){
  const t = i18n[PREFS.locale] || i18n.en;
  document.getElementById('labelUnits').textContent = t.Units;
  document.getElementById('labelLang').textContent = t.Language;
  document.getElementById('hdrFermenters').textContent = t.Fermenters;
  document.getElementById('colBeer').textContent = t.Beer;
  document.getElementById('colTarget').textContent = t.Target;
  document.getElementById('colValve').textContent = t.Valve;
  document.getElementById('colSet').textContent = t.SetTarget;
  document.getElementById('hdrAlarms').textContent = t.Alarms;
  document.getElementById('hdrProfiles').textContent = t.Profiles;
  document.getElementById('hdrProfilesList').textContent = t.SavedProfiles;
  document.getElementById('hdrCreateProfile').textContent = t.CreateProfile;
  document.getElementById('lblProfName').textContent = t.Name;
  document.getElementById('thStepType').textContent = t.Type;
  document.getElementById('thTarget').textContent = t.Target;
  document.getElementById('thDuration').textContent = t.Duration;
  document.getElementById('thRate').textContent = t.Rate;
  document.getElementById('btnAddStep').textContent = t.AddStep;
  document.getElementById('btnSaveProfile').textContent = t.SaveProfile;
  document.getElementById('btnAssign').textContent = t.AssignToFV;
  document.getElementById('btnCancelProfile').textContent = t.CancelActive;
  document.getElementById('lblActiveProfile').textContent = t.ActiveProfile;
  if (document.getElementById('activeProfileText').dataset.none==="1") {
    document.getElementById('activeProfileText').textContent = t.None;
  }
}
function applyUnitsButtons(){
  document.getElementById('btnC').classList.toggle('active', PREFS.units==="C");
  document.getElementById('btnF').classList.toggle('active', PREFS.units==="F");
}

function savePrefs(){
  localStorage.setItem("phb_prefs", JSON.stringify(PREFS));
}

function loadPrefs(){
  // No fetch; just apply what we have
  document.getElementById('selLocale').value = PREFS.locale;
  applyLocale();
  applyUnitsButtons();
}

async function setUnits(u){
  PREFS.units = u;
  applyUnitsButtons();
  savePrefs();
  loadHistory();
  refresh();
}

async function setLocale(loc){
  PREFS.locale = loc;
  applyLocale();
  savePrefs();
  loadHistory();
  refresh();
}


async function refresh(){
  const st = await (await fetch('/api/state')).json();
  const tbody = document.querySelector('#tbl tbody');
  tbody.innerHTML = '';
  const t = i18n[PREFS.locale] || i18n.en;
  
  const changed = updateFVSelectors(st);

    // one-time wiring for range/FV changes
    if (!HISTORY_WIRED) {
        const selRange = document.getElementById('selRange');
        if (selRange) selRange.addEventListener('change', () => maybeRefreshHistory(true));
        // updateFVSelectors already wires FV selects to call loadHistory()
        HISTORY_WIRED = true;
    }

    // if FV list/selection changed, force refresh immediately
    if (changed) {
        maybeRefreshHistory(true);
    } else {
        // otherwise refresh on a gentle timer
        maybeRefreshHistory(false);
    }

  for (const f of st.fv) {
	  const tr = document.createElement('tr');

	  const STALE_AFTER = 15;
	  if (typeof f.ageS === 'number' && f.ageS > STALE_AFTER) {
		tr.classList.add('stale');
	  }

	  const input = document.createElement('input');
	  input.type='number'; input.step='0.1';
	  input.value = PREFS.units==="F" ? cToF(f.targetC).toFixed(1) : f.targetC.toFixed(1);

	  const btn = document.createElement('button');
	  btn.className='btn'; btn.textContent=t.Apply;
	  btn.addEventListener('click', async ()=>{
		let v = parseFloat(input.value); if (Number.isNaN(v)) return;
		const targetC = PREFS.units==="F" ? fToC(v) : v;
		await authedFetch('/api/fv/'+f.id+'/target', {
		  method:'POST',
		  headers:{'content-type':'application/json'},
		  body: JSON.stringify({target_c: targetC})
		});
		refresh();
	  });

	  const setCell = document.createElement('td');
	  setCell.appendChild(input); setCell.appendChild(btn);

      const modeTxt = (f.mode === 'manual') ? 'Manual' : 'Auto';
      const until = (f.overrideUntilS && f.overrideUntilS > 0)
          ? ` (until ${new Date(f.overrideUntilS*1000).toLocaleTimeString()})` : '';
      const modeCell = `<span class="${f.mode==='manual'?'badge-warn':'badge-ok'}">${modeTxt}${until}</span>`;

      tr.innerHTML =
		'<td>'+f.id+'</td><td>'+f.name+'</td>' +
        '<td>'+fmtTemp(f.beerC)+'</td>' +
        '<td>'+fmtTemp(f.targetC)+'</td>' +
        '<td><span class="'+(f.valve==='open'?'valve-open':'valve-closed')+'">'+f.valve+'</span></td>' +
        '<td>'+modeCell+'</td>' +
        `<td>${(f.ageS ?? 0)}s</td>`;

	  tr.appendChild(setCell);
	  tbody.appendChild(tr);
	}


  const alarms = await (await fetch('/api/alarms')).json();
  const ul = document.getElementById('alarms'); ul.innerHTML='';
  for(const a of alarms){
    const li=document.createElement('li');
    li.textContent = a.id+' — '+(a.message || a.type);
    ul.appendChild(li);
  }
  
    // Active profile label per currently selected FV (if server exposes it)
	  const fvSel = document.getElementById('selFV');
	  const activeLbl = document.getElementById('activeProfileLabel');
	  let activeText = '';
	  if (fvSel && activeLbl && Array.isArray(st.fv)) {
		const f = st.fv.find(x => x.id === fvSel.value) || st.fv[0];
		if (f && f.activeProfile) {
		  const a = f.activeProfile;
		  activeText = `Active: ${a.name || ('Profile #' + a.profile_id)} (step ${a.step_idx+1})` + (a.paused ? ' [paused]' : '');
		}
	  }
	  if (activeLbl) activeLbl.textContent = activeText;

}

function maybeRefreshHistory(force=false){
    const now = Date.now();
    if (force || (now - HISTORY_LAST_FETCH >= HISTORY_MIN_PERIOD_MS)) {
        loadHistory();
    }
}

async function loadHistory(){
    if (HISTORY_INFLIGHT) return;
    HISTORY_INFLIGHT = true;
    try {
        const fv = getCurrentFV();
        const range = document.getElementById('selRange').value || '6h';
        const now = Math.floor(Date.now()/1000);
        const secs = durToSec(range);
        const step = niceStep(secs);

        // series
        const url = `/api/history/${encodeURIComponent(fv)}?from=${now-secs}&to=${now}&step=${step}s`;
        const res = await fetch(url, {cache:'no-store'});
        const j = await res.json();
        const series = j.data || [];

        // events (same window)
        const evRes = await fetch(`/api/events?fv=${encodeURIComponent(fv)}&from=${now-secs}&to=${now}`, {cache:'no-store'});
        const evJson = evRes.ok ? await evRes.json() : {events:[]};
        const events = Array.isArray(evJson.events) ? evJson.events : [];

        const out = series.map(p => ({
            t: p.t,
            beer_c: (PREFS.units==="F") ? (p.beer_c*9/5+32) : p.beer_c,
            target_c: (PREFS.units==="F") ? (p.target_c*9/5+32) : p.target_c,
        }));
        const canvas = document.getElementById('chart');
        drawSeries(canvas, out, PREFS.units==="F" ? PREFS.labelF : PREFS.labelC, events);

        HISTORY_LAST_FETCH = Date.now();
    } finally {
        HISTORY_INFLIGHT = false;
    }
}

// ---------- Profiles UI ----------

async function fetchProfiles(){
  const r = await fetch('/api/profiles', {cache:'no-store'});
  if (!r.ok) return [];
  try { const j = await r.json(); return Array.isArray(j) ? j : []; } catch { return []; }
}

function renderProfilesList(list){
  const ul = document.getElementById('profilesList');
  ul.innerHTML = '';
  for(const p of list){
    const li = document.createElement('li');
    const rad = document.createElement('input');
    rad.type = 'radio'; rad.name='profsel'; rad.value=String(p.id);
    li.appendChild(rad);
    const lbl = document.createElement('label');
    lbl.textContent = ` ${p.name} (#${p.id})`;
    li.appendChild(lbl);
    ul.appendChild(li);
  }
  if (ul.firstChild) ul.querySelector('input[type=radio]').checked = true;
}

function addStepRow(type='hold'){
  const tb = document.getElementById('stepsBody');
  const tr = document.createElement('tr');

  // type
  const tdType = document.createElement('td');
  const selType = document.createElement('select');
  selType.innerHTML = `<option value="hold">hold</option><option value="ramp">ramp</option>`;
  selType.value = type;
  tdType.appendChild(selType);

  // target
  const tdTarget = document.createElement('td');
  const inpTarget = document.createElement('input'); inpTarget.type='number'; inpTarget.step='0.1';
  tdTarget.appendChild(inpTarget);

  // duration + unit (for hold)
  const tdDur = document.createElement('td');
  const wrapDur = document.createElement('div'); wrapDur.style.display='flex'; wrapDur.style.gap='.25rem';
  const inpDur = document.createElement('input'); inpDur.type='number'; inpDur.step='1'; inpDur.min='0';
  inpDur.style.width='7rem';
  const selUnit = document.createElement('select');
  selUnit.innerHTML = `<option value="h">h</option><option value="d">d</option><option value="m">m</option>`;
  selUnit.value = 'd'; // default to days (most common for fermentation)
  wrapDur.appendChild(inpDur); wrapDur.appendChild(selUnit);
  tdDur.appendChild(wrapDur);

  // rate (for ramp)
  const tdRate = document.createElement('td');
  const inpRate = document.createElement('input'); inpRate.type='number'; inpRate.step='0.1'; inpRate.min='0';
  inpRate.placeholder = '°/h';
  tdRate.appendChild(inpRate);

  // delete
  const tdDel = document.createElement('td');
  const btnDel = document.createElement('button'); btnDel.className='btn btn-secondary'; btnDel.textContent='×';
  btnDel.addEventListener('click',()=>tr.remove());
  tdDel.appendChild(btnDel);

  function updateVisible(){
    const isHold = selType.value === 'hold';
    wrapDur.style.opacity = isHold ? 1 : .4;
    inpDur.disabled = !isHold; selUnit.disabled = !isHold;

    inpRate.style.opacity = isHold ? .4 : 1;
    inpRate.disabled = isHold;
  }
  selType.addEventListener('change', updateVisible);

  tr.appendChild(tdType); tr.appendChild(tdTarget); tr.appendChild(tdDur); tr.appendChild(tdRate); tr.appendChild(tdDel);
  tb.appendChild(tr);
  updateVisible();
}


function readProfileSpecFromTable(){
  const steps = [];
  for(const tr of document.querySelectorAll('#stepsBody tr')){
    const [tdType, tdTarget, tdDur, tdRate] = tr.children;
    const type = tdType.querySelector('select').value;

    // Target in UI units → °C
    const targUI = parseFloat(tdTarget.querySelector('input').value);
    if (Number.isNaN(targUI)) continue;
    const targetC = (PREFS.units==="F") ? fToC(targUI) : targUI;

    if (type === 'hold'){
      const val = parseFloat(tdDur.querySelector('input[type=number]').value);
      const unit = tdDur.querySelector('select').value; // 'm' | 'h' | 'd'
      const mult = (unit==='d') ? 86400 : (unit==='h') ? 3600 : 60;
      const duration_s = Number.isFinite(val) ? Math.round(val * mult) : 0;
      steps.push({ type, target_c: targetC, duration_s });
    } else {
      const rate = parseFloat(tdRate.querySelector('input').value);
      steps.push({ type, target_c: targetC, rate_c_per_hour: (Number.isFinite(rate)? rate : 0.25) });
    }
  }
  return { steps };
}


async function saveProfile(){
  const name = document.getElementById('profName').value.trim();
  if (!name){ alert('Profile name is required'); return; }
  const spec = readProfileSpecFromTable();
  if (!spec.steps.length){ alert('Add at least one step'); return; }
  const r = await authedFetch('/api/profiles', {
    method:'POST', headers:{'content-type':'application/json'},
    body: JSON.stringify({name, spec})
  });
  if (!r.ok){ alert('Save failed: '+r.status); return; }
  document.getElementById('profName').value='';
  document.getElementById('stepsBody').innerHTML='';
  const list = await fetchProfiles(); renderProfilesList(list);
}

function selectedProfileID(){
  const r = document.querySelector('#profilesList input[name=profsel]:checked');
  return r ? parseInt(r.value,10) : null;
}

// --- helper: get selected profile ID from either UI control ---
function resolveProfileID() {
  // Try dropdown first
  const sel = document.getElementById('selProfile');
  if (sel && sel.value) {
    const v = parseInt(sel.value, 10);
    if (Number.isFinite(v) && v > 0) return v;
  }
  // Fallback to radio list
  const r = document.querySelector('#profilesList input[name=profsel]:checked');
  if (r) {
    const v = parseInt(r.value, 10);
    if (Number.isFinite(v) && v > 0) return v;
  }
  return null;
}

async function assignProfileUnified() {
  const btn = document.getElementById('btnAssign');
  const fv  = getCurrentFV();
  const pid = resolveProfileID();
  if (!fv || !pid) { alert('Select a fermenter and a profile'); return; }

  if (btn) btn.disabled = true;
  try {
    const res = await authedFetch(`/api/fv/${encodeURIComponent(fv)}/profile/assign`, {
      method: 'POST',
      headers: {'content-type':'application/json'},
      body: JSON.stringify({ profile_id: pid })
    });
    if (!res.ok) {
      const txt = await res.text().catch(()=>String(res.status));
      alert('Assign failed: ' + txt);
      return;
    }
    await refreshActiveProfile();
  } finally {
    if (btn) btn.disabled = false;
    refresh();
  }
}

async function cancelActiveProfile(){
  const fv = getCurrentFV();
  const r = await authedFetch(`/api/fv/${encodeURIComponent(fv)}/profile/cancel`, { method:'POST' });
  if (!r.ok){ alert('Cancel failed: '+r.status); return; }
  await refreshActiveProfile();
}

// Optional: display current assignment if you implemented GET /api/fv/{id}/profile
async function refreshActiveProfile(){
  const el = document.getElementById('activeProfileText');
  if (!el) return;
  const fv = getCurrentFV();
  const r = await fetch(`/api/fv/${encodeURIComponent(fv)}/profile`, {cache:'no-store'});
  if (!r.ok){ el.dataset.none="1"; el.textContent = (i18n[PREFS.locale]||i18n.en).None; return; }
  try {
    const j = await r.json();
    if (!j.active){ el.dataset.none="1"; el.textContent = (i18n[PREFS.locale]||i18n.en).None; return; }
    el.dataset.none="0";
    const step = (j.current_step && j.current_step.type) ? j.current_step.type : 'step '+j.step_idx;
    el.textContent = `#${j.profile_id} ${step}`;
  } catch {
    el.dataset.none="1"; el.textContent = (i18n[PREFS.locale]||i18n.en).None;
  }
}

async function loadProfiles() {
  const res = await fetch('/api/profiles', {cache:'no-store'});
  if (!res.ok) return;
  const list = await res.json();
  const sel = document.getElementById('selProfile');
  if (!sel) return;                // guard
  sel.innerHTML = '';
  for (const p of list) {
    const opt = document.createElement('option');
    opt.value = p.id;
    opt.textContent = p.name || ('Profile #' + p.id);
    sel.appendChild(opt);
  }
}


async function pauseResumeProfile() {
  const fv = getCurrentFV();
  if (!fv) return;
  // read current
  let paused = false;
  const r = await fetch(`/api/fv/${encodeURIComponent(fv)}/profile`, {cache:'no-store'});
  if (r.status === 200) {
    const j = await r.json(); paused = !!j.paused;
  }
  const res = await authedFetch(`/api/fv/${encodeURIComponent(fv)}/profile/pause?paused=${!paused}`, {method:'POST'});
  if (!res.ok) console.warn('pause failed', res.status);
  refresh();
}

async function stopProfile() {
  const fv = getCurrentFV();                 // was reading #selFV directly
  if (!fv) return;
  const res = await authedFetch(`/api/fv/${encodeURIComponent(fv)}/profile/cancel`, {method:'POST'});
  if (!res.ok) console.warn('cancel failed', res.status);
  refresh();
}

async function updateActiveLabel() {
  const fv = getCurrentFV();
  const els = [
    document.getElementById('activeProfileLabelTop'),
    document.getElementById('activeProfileLabel'),
  ].filter(Boolean);

  if (!fv || els.length === 0) return;

  const r = await fetch(`/api/fv/${encodeURIComponent(fv)}/profile`, { cache:'no-store' });
  if (r.status === 204 || !r.ok) { els.forEach(el => el.textContent=''); return; }

  const a = await r.json();
  const name = a.name || ('Profile #' + a.profile_id);
  const text = `Active: ${name} (step ${a.step_idx+1})` + (a.paused ? ' [paused]' : '');
  els.forEach(el => el.textContent = text);
}

function setSessionToken(tok) {
    if (tok) localStorage.setItem('phb_session', tok);
    else localStorage.removeItem('phb_session');
}

async function authedFetch(url, opts={}) {
    let res = await fetch(url, opts);
    if (res.status === 401) {
        const pin = prompt('Enter PIN to authorize this action:');
        if (!pin) return res;
        const rr = await fetch('/api/auth/login', {
            method:'POST',
            headers:{'content-type':'application/json'},
            body: JSON.stringify({pin})
        });
        if (!rr.ok) {
            alert('PIN rejected');
            return res;
        }
        // retry original request
        res = await fetch(url, opts);
    }
    return res;
}

function convertSecondsToMinutesAndSeconds(totalSeconds) {
    const minutes = Math.floor(totalSeconds / 60);
    const remainingSeconds = totalSeconds % 60;

    // Optional: Format the output for better readability (e.g., "05:03" instead of "5:3")
    const formattedMinutes = String(minutes).padStart(2, '0');
    const formattedSeconds = String(remainingSeconds).padStart(2, '0');

    return `${formattedMinutes}:${formattedSeconds}`;
}

async function updateLockStatus() {
    const r = await fetch('/api/auth/status', {cache:'no-store'});
    if (!r.ok) return;
    const j = await r.json();
    const sessionTimeLeft = Math.max(0, Math.round(j.expires_at - (Math.floor(Date.now()/1000))));
    if (j.authorized) {
        document.getElementById('lockStatus').textContent =`System Unlocked (${convertSecondsToMinutesAndSeconds(sessionTimeLeft)}s)`;
        document.getElementById('btnLockdown').style.display = 'inline';
    } else {
        document.getElementById('lockStatus').textContent = 'System Locked';
        document.getElementById('btnLockdown').style.display = 'none';
    }
}

async function lockNow() {
    await authedFetch('/api/auth/logout', {method:'POST'});
    setSessionToken('');
    updateLockStatus();
}

async function setMode(mode) {
    const fv = getCurrentFV();
    await fetch(`/api/fv/${encodeURIComponent(fv)}/mode`, {
        method:'POST', headers:{'content-type':'application/json'},
        body: JSON.stringify({ mode })
    });
    refresh();
}

async function clearValveOverride() {
    const fv = getCurrentFV();
    await fetch(`/api/fv/${encodeURIComponent(fv)}/valve/clear`, {method:'POST'});
}

async function setValveOverride(state, minutes) {
    const fv = getCurrentFV();
    const body = { state };
    if (Number.isFinite(minutes) && minutes > 0) body.for_s = Math.round(minutes*60);
    const res = await fetch(`/api/fv/${encodeURIComponent(fv)}/valve`, {
        method:'POST', headers:{'content-type':'application/json'},
        body: JSON.stringify(body)
    });
    if (!res.ok) alert('Override failed: '+(await res.text()));
}

async function loadSettings() {
    try {
        const r = await fetch('/api/settings', {cache:'no-store'});
        if (!r.ok) return;
        const s = await r.json();
        document.getElementById('setBandC').value = s.band_c ?? '';
        document.getElementById('setMinChangeS').value = s.min_change_s ?? '';
        document.getElementById('setMaxOpen').value = s.max_open ?? 0;
    } catch {}
}

async function saveSettings() {
    const body = {
        band_c: parseFloat(document.getElementById('setBandC').value),
        min_change_s: parseInt(document.getElementById('setMinChangeS').value, 10),
        max_open: parseInt(document.getElementById('setMaxOpen').value, 10),
    };
    const r = await fetch('/api/settings', {
        method:'POST', headers:{'content-type':'application/json'},
        body: JSON.stringify(body)
    });
    if (!r.ok) {
        alert('Save failed: '+(await r.text()).slice(0,200));
        return;
    }
    // no reload needed; controller applies live
}

/**
 * Listeners
 **/
document.getElementById('btnC').addEventListener('click',()=>setUnits('C'));
document.getElementById('btnF').addEventListener('click',()=>setUnits('F'));
document.getElementById('selLocale').addEventListener('change',e=>setLocale(e.target.value));

document.getElementById('btnAssign').addEventListener('click', assignProfileUnified);
document.getElementById('btnAssignTop').addEventListener('click', assignProfileUnified);
document.getElementById('btnAddStep').addEventListener('click', ()=> addStepRow('hold'));
document.getElementById('btnSaveProfile').addEventListener('click', saveProfile);
document.getElementById('btnCancelProfile').addEventListener('click', cancelActiveProfile);
document.getElementById('btnPauseResumeTop').addEventListener('click', pauseResumeProfile);
document.getElementById('btnStopTop').addEventListener('click', stopProfile);

document.getElementById('btnLockdown').addEventListener('click', lockNow);

document.getElementById('btnModeValveClear') .addEventListener('click', clearValveOverride);
document.getElementById('btnModeValve').addEventListener('click', ()=>setMode('valve'));
document.getElementById('btnModeFixed')?.addEventListener('click', ()=>setMode('fixed'));

document.getElementById('btnSaveSettings').addEventListener('click', saveSettings);

// After your existing loadPrefs() init:
loadSettings();
loadProfiles();        // <— populate profiles dropdown
refresh();
setInterval(refresh, 1000);

// Init page lock
setInterval(updateLockStatus, 1000);
updateLockStatus();

// Init profiles pane
fetchProfiles().then(renderProfilesList);
addStepRow('hold');            // seed one row for convenience
refreshActiveProfile();        // shows active/none for current FV