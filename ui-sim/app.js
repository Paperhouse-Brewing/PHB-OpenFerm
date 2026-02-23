let unit = localStorage.getItem('phb_unit') || 'C';
let locale = localStorage.getItem('phb_locale') || 'en-US';

const I18N = {
  "en-US": {
    title: "PHB — UI Simulator",
    liveHint: "Static prototype only (no backend).",
    unit: "Unit",
    toggle: "Toggle C/°F",
    id: "ID",
    name: "Name",
    beer: "Beer",
    target: "Target",
    valve: "Valve"
  },
  "es-ES": {
    title: "PHB — Simulador de UI",
    liveHint: "Prototipo estático (sin backend).",
    unit: "Unidad",
    toggle: "Cambiar C/°F",
    id: "ID",
    name: "Nombre",
    beer: "Cerveza",
    target: "Objetivo",
    valve: "Válvula"
  }
};

function cToF(c){ return c*9/5 + 32; }
function fmtTemp(c){ const v = unit==='F' ? cToF(c) : c; return v.toFixed(2) + ' °' + unit; }

const fermenters = [
  {id:'fv1', name:'Fermenter 1', beerC:19.5, targetC:18.0, valve:'closed', stage:'hold'},
  {id:'fv2', name:'Fermenter 2', beerC:22.0, targetC:19.0, valve:'closed', stage:'ramp'},
  {id:'fv3', name:'Fermenter 3', beerC:4.0,  targetC:2.0,  valve:'closed', stage:'hold'},
  {id:'fv4', name:'Fermenter 4', beerC:10.0, targetC:12.0, valve:'closed', stage:'hold'}
];

const profile = [
  {type:'hold', temp_c:18.0, duration_h:48, label:'primary'},
  {type:'ramp', to_c:20.0, rate_c_per_h:0.5, label:'diacetyl'},
  {type:'hold', temp_c:20.0, duration_h:72, label:'rest'},
  {type:'ramp', to_c:2.0,  rate_c_per_h:1.0, label:'crash'},
  {type:'hold', temp_c:2.0,  duration_h:48, label:'condition'}
];

function renderProfile() {
  const tl = document.getElementById('timeline');
  const lg = document.getElementById('legend');
  tl.innerHTML = '';
  let totalH = profile.reduce((a,s)=> a + (s.duration_h || 12), 0);
  profile.forEach((s, i) => {
    const div = document.createElement('div');
    div.className = 'step ' + (s.type === 'ramp' ? 'ramp' : 'hold');
    div.dataset.label = s.label || s.type;
    const w = ((s.duration_h || 12) / (totalH || 1)) * 100;
    div.style.width = Math.max(8, w) + '%';
    tl.appendChild(div);
  });
  lg.innerHTML = '<span class="hold">Hold</span><span class="ramp">Ramp</span>';
}

function renderTable() {
  const tbody = document.getElementById('fv-rows');
  tbody.innerHTML = fermenters.map(f => `
    <tr>
      <td>${f.id}</td>
      <td>${f.name}</td>
      <td>${fmtTemp(f.beerC)}</td>
      <td>
        <input type="number" step="0.1" value="${(unit==='F'? cToF(f.targetC): f.targetC).toFixed(1)}"
               onchange="setTarget('${f.id}', this.value)"
               style="width:80px"/>
      </td>
      <td><span class="badge ${f.valve==='open'?'open':'closed'}">${f.valve}</span></td>
      <td>${f.stage}</td>
      <td class="controls">
        <button onclick="nudge('${f.id}', -0.5)">-0.5°C</button>
        <button onclick="nudge('${f.id}',  0.5)">+0.5°C</button>
      </td>
    </tr>
  `).join('');
}

window.setTarget = (id, v) => {
  const f = fermenters.find(x=>x.id===id);
  if (!f) return;
  const val = parseFloat(v);
  f.targetC = unit==='F' ? (val - 32) * 5/9 : val;
  renderTable();
};

window.nudge = (id, dv) => {
  const f = fermenters.find(x=>x.id===id); if (!f) return;
  f.targetC = +(f.targetC + (unit==='F' ? dv * 5/9 : dv)).toFixed(2);
  renderTable();
};

function tick() {
  fermenters.forEach(f => {
    const band = 0.3;
    if (f.beerC > f.targetC + band) { f.valve = 'open';  f.beerC -= 0.05 + Math.random()*0.02; }
    else if (f.beerC < f.targetC - band) { f.valve = 'closed'; f.beerC += 0.02 * (0.5+Math.random()); }
    else { f.beerC += (Math.random()-0.5)*0.01; }
  });
  renderTable(); pushSample(); drawSpark();
}

const hist = [];
function pushSample(){ const v = fermenters[0].beerC; hist.push(v); if (hist.length > 200) hist.shift(); }
function drawSpark(){
  const c = document.getElementById('spark'); const g = c.getContext('2d');
  g.clearRect(0,0,c.width,c.height);
  const min = Math.min(...hist, 0), max = Math.max(...hist, 25);
  const pad = 10;
  g.beginPath();
  hist.forEach((v,i)=>{
    const x = pad + i*( (c.width-2*pad) / Math.max(1,(hist.length-1)) );
    const y = c.height - pad - ( (v-min)/(max-min+1e-6) )*(c.height-2*pad);
    i?g.lineTo(x,y):g.moveTo(x,y);
  });
  g.strokeStyle = '#4e8cff'; g.lineWidth = 2; g.stroke();
  g.fillStyle = '#888'; g.font = '12px system-ui';
  g.fillText(`fv1 beerC (min ${min.toFixed(1)}°C / max ${max.toFixed(1)}°C)`, 10, 14);
}

function applyI18n(){
  const t = I18N[locale] || I18N['en-US'];
  document.title = t.title;
  document.getElementById('title').textContent = t.title;
  document.getElementById('liveHint').textContent = t.liveHint;
  document.getElementById('thId').textContent = t.id;
  document.getElementById('thName').textContent = t.name;
  document.getElementById('thBeer').textContent = t.beer;
  document.getElementById('thTarget').textContent = t.target;
  document.getElementById('thValve').textContent = t.valve;
  document.getElementById('toggleUnit').textContent = t.toggle;
  document.getElementById('unitLabel').textContent = `${t.unit}: °${unit}`;
  const picker = document.getElementById('localePicker'); if (picker) picker.value = locale;
}

document.getElementById('toggleUnit').addEventListener('click', ()=>{
  unit = (unit==='C') ? 'F' : 'C';
  localStorage.setItem('phb_unit', unit);
  applyI18n();
});
const picker = document.getElementById('localePicker');
picker.addEventListener('change', ()=>{
  locale = picker.value || 'en-US';
  localStorage.setItem('phb_locale', locale);
  applyI18n();
});

renderProfile(); renderTable(); setInterval(tick, 1000);
applyI18n();
