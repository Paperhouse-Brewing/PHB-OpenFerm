const i18n = {
  en: { Units:"Units", Language:"Language", Fermenters:"Fermenters", Beer:"Beer", Target:"Target", Valve:"Valve", SetTarget:"Set Target", Alarms:"Alarms", Apply:"Apply" },
  es: { Units:"Unidades", Language:"Idioma", Fermenters:"Fermentadores", Beer:"Cerveza", Target:"Objetivo", Valve:"Válvula", SetTarget:"Ajustar objetivo", Alarms:"Alarmas", Apply:"Aplicar" }
};

let FV_LIST = [];
let CHART_INIT = false;

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

function updateFVSelector(st){
  const sel = document.getElementById('selFV');
  if(!sel) return;

  const ids = st.fv.map(f => f.id);
  const current = Array.from(sel.options).map(o => o.value);
  const needsRebuild =
    current.length !== ids.length ||
    current.some((v,i) => v !== ids[i]);

  if(needsRebuild){
    const keep = sel.value; // try to keep current selection
    sel.innerHTML = '';
    for(const f of st.fv){
      const opt = document.createElement('option');
      opt.value = f.id;
      opt.textContent = (f.name || f.id) + ' (' + f.id + ')';
      sel.appendChild(opt);
    }
    if(ids.includes(keep)) sel.value = keep;
  }
}


// very small canvas line plotter
function drawSeries(canvas, series, unitsLabel){
  const ctx = canvas.getContext('2d');
  const W = canvas.width, H = canvas.height;
  ctx.clearRect(0,0,W,H);
  if(!series || series.length === 0){ ctx.fillText('No data', 10, 20); return; }

  const tMin = series[0].t, tMax = series[series.length-1].t;
  let yMin = Infinity, yMax = -Infinity;
  for(const p of series){
    yMin = Math.min(yMin, p.beer_c, p.target_c);
    yMax = Math.max(yMax, p.beer_c, p.target_c);
  }
  if(yMin === yMax){ yMin -= 0.5; yMax += 0.5; }

  const L=50, R=10, T=10, B=30;
  const X = t => L + ( (t - tMin) / (tMax - tMin) ) * (W - L - R);
  const Y = v => H - B - ( (v - yMin) / (yMax - yMin) ) * (H - T - B);

  // axes
  ctx.strokeStyle = '#bbb'; ctx.beginPath();
  ctx.moveTo(L,T); ctx.lineTo(L,H-B); ctx.lineTo(W-R,H-B); ctx.stroke();

  ctx.fillStyle = '#666'; ctx.font = '12px system-ui';
  ctx.fillText(unitsLabel, 8, 12);

  // gridlines (5)
  ctx.strokeStyle = '#eee';
  for(let i=0;i<=5;i++){
    const y = T + i*(H-T-B)/5;
    ctx.beginPath(); ctx.moveTo(L,y); ctx.lineTo(W-R,y); ctx.stroke();
    const v = yMax - i*(yMax-yMin)/5;
    ctx.fillText(v.toFixed(1), 4, y+4);
  }

  // x labels (5)
  for(let i=0;i<=5;i++){
    const t = tMin + i*(tMax-tMin)/5;
    const d = new Date(t*1000);
    const label = d.toLocaleTimeString([], {hour:'2-digit', minute:'2-digit'});
    const x = X(t);
    ctx.fillText(label, x-18, H-10);
  }

  // line helper
  function line(getY){
    ctx.beginPath();
    let first=true;
    for(const p of series){
      const x = X(p.t), y = getY(p);
      if(first){ ctx.moveTo(x,y); first=false; } else { ctx.lineTo(x,y); }
    }
    ctx.stroke();
  }

  // beer line
  ctx.strokeStyle = '#2d7'; line(p => Y(p.beer_c));
  // target line
  ctx.strokeStyle = '#36c'; line(p => Y(p.target_c));
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
document.getElementById('btnC').addEventListener('click',()=>setUnits('C'));
document.getElementById('btnF').addEventListener('click',()=>setUnits('F'));
document.getElementById('selLocale').addEventListener('change',e=>setLocale(e.target.value));

async function refresh(){
  const st = await (await fetch('/api/state')).json();
  const tbody = document.querySelector('#tbl tbody');
  tbody.innerHTML = '';
  const t = i18n[PREFS.locale] || i18n.en;
  
  // populate FV selector once
	updateFVSelector(st);

  for(const f of st.fv){
    const tr = document.createElement('tr');

    // input to set target (render in chosen units, send in C)
    const input = document.createElement('input');
    input.type='number'; input.step='0.1';
    input.value = PREFS.units==="F" ? cToF(f.targetC).toFixed(1) : f.targetC.toFixed(1);

    const btn = document.createElement('button');
    btn.className='btn'; btn.textContent=t.Apply;
    btn.addEventListener('click', async ()=>{
      let v=parseFloat(input.value); if(Number.isNaN(v)) return;
      const targetC = PREFS.units==="F" ? fToC(v) : v;
      await fetch('/api/fv/'+f.id+'/target',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({target_c:targetC})});
      refresh();
    });

    const setCell = document.createElement('td'); setCell.appendChild(input); setCell.appendChild(btn);

    tr.innerHTML =
      '<td>'+f.id+'</td><td>'+f.name+'</td>' +
      '<td>'+fmtTemp(f.beerC)+'</td>' +
      '<td>'+fmtTemp(f.targetC)+'</td>' +
      '<td><span class="'+(f.valve==='open'?'valve-open':'valve-closed')+'">'+f.valve+'</span></td>';
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
}

async function loadHistory(){
  const id = document.getElementById('selFV').value || (FV_LIST[0]||'fv1');
  const range = document.getElementById('selRange').value || '6h';
  const now = Math.floor(Date.now()/1000);
  const secs = durToSec(range);
  const step = niceStep(secs);
  const url = `/api/history/${encodeURIComponent(id)}?from=${now-secs}&to=${now}&step=${step}s`;
  const res = await fetch(url, {cache:'no-store'});
  const j = await res.json();
  const series = j.data || [];
  // convert to selected units for plotting
  const out = series.map(p => ({
    t: p.t,
    beer_c: (PREFS.units==="F") ? (p.beer_c*9/5+32) : p.beer_c,
    target_c: (PREFS.units==="F") ? (p.target_c*9/5+32) : p.target_c,
  }));
  const canvas = document.getElementById('chart');
  drawSeries(canvas, out, PREFS.units==="F" ? PREFS.labelF : PREFS.labelC);
}


loadPrefs().then(()=>{ refresh(); setInterval(refresh,1000); });
