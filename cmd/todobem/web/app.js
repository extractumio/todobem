'use strict';
/* todobem SPA. Data comes from /api/* (see docs/SCHEMA.md). All times are Unix ms.
   Aggregates use the root lane's exclusive partition; sub-agent time is shown separately. */
const $ = (s, root = document) => root.querySelector(s);
const $$ = (s, root = document) => [...root.querySelectorAll(s)];
const esc = v => String(v ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const clamp = (n, a, b) => Math.max(a, Math.min(b, n));
const sum = a => a.reduce((n, x) => n + x, 0);
const pad = n => String(n).padStart(2, '0');
const overlap = (s, e, a, b) => Math.max(0, Math.min(e, b) - Math.max(s, a));

const paths = { grid: '<rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/>', timeline: '<path d="M3 6h8M15 6h6M3 12h4M11 12h10M3 18h12M19 18h2"/>', help: '<circle cx="12" cy="12" r="9"/><path d="M9.5 8.5a2.5 2.5 0 1 1 4 2c-1 .7-1.5 1.5-1.5 2.5m0 3h.01"/>', right: '<path d="m9 5 7 7-7 7"/>', left: '<path d="m15 5-7 7 7 7"/>', loop: '<path d="M19 7h-8a6 6 0 1 0 0 12h2M16 3l4 4-4 4M5 17h8a6 6 0 1 0 0-12h-2M8 21l-4-4 4-4"/>', repo: '<path d="M5 3h14v18H6a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2M4 17h15M8 7h6"/>', target: '<circle cx="12" cy="12" r="8"/><circle cx="12" cy="12" r="3"/><path d="m15 9 6-6m-4 0h4v4"/>', clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>', code: '<path d="m8 6-6 6 6 6m8-12 6 6-6 6m-3-15-2 18"/>', wait: '<path d="M7 3h10M7 21h10M8 3v5l8 8v5M16 3v5l-8 8v5"/>', gap: '<path d="M4 5v14m16-14v14M8 12h2m4 0h2"/>', expand: '<path d="M8 3H3v5m18 0V3h-5M3 16v5h5m8 0h5v-5"/>', latest: '<path d="M3 12h14m-5-5 5 5-5 5m9-13v16"/>', close: '<path d="m6 6 12 12M6 18 18 6"/>', info: '<circle cx="12" cy="12" r="9"/><path d="M12 11v6m0-10h.01"/>', shield: '<path d="m12 2 9 4v6c0 5-9 10-9 10S3 17 3 12V6z"/><path d="m8 11 3 3 5-6"/>', search: '<circle cx="10.5" cy="10.5" r="6.5"/><path d="m16 16 5 5"/>', check: '<path d="m5 12 4 4L19 6"/>', brain: '<path d="M12 4a3 3 0 0 0-3 3v10a3 3 0 0 0 6 0V7a3 3 0 0 0-3-3zM6 9a3 3 0 0 0 0 6M18 9a3 3 0 0 1 0 6"/>', refresh: '<path d="M20 12a8 8 0 1 1-2.3-5.7M20 4v5h-5"/>', agents: '<circle cx="7" cy="7" r="3"/><circle cx="17" cy="7" r="3"/><circle cx="12" cy="17" r="3"/><path d="M9 9l2 5M15 9l-2 5"/>' };
const icon = (name, small = false) => `<svg class="ico ${small ? 'small-ico' : ''}" viewBox="0 0 24 24" aria-hidden="true">${paths[name] || paths.info}</svg>`;

const PHASES = {
  llm: { name: 'LLM', short: 'LLM', color: '#348989', kind: 'model' },
  code: { name: 'Coding', short: 'Code', color: '#4690ce', kind: 'work' },
  build: { name: 'Build', short: 'Build', color: '#a889fc', kind: 'work' },
  test: { name: 'Testing', short: 'Test', color: '#d9bc3d', kind: 'work' },
  release: { name: 'Release & deploy', short: 'Release', color: '#4aa65f', kind: 'work' },
  infra: { name: 'Infrastructure', short: 'Infra', color: '#d76368', kind: 'work' },
  wait_worker: { name: 'Waiting for workers / harness', short: 'Workers', color: '#48aa8c', kind: 'wait' },
  wait_user: { name: 'Waiting for user', short: 'User', color: '#7c5560', kind: 'wait' },
  idle: { name: 'Idle (awaiting parent)', short: 'Idle', color: '#30414f', kind: 'wait' },
  compaction: { name: 'Context compaction', short: 'Compact', color: '#c08a45', kind: 'overhead' },
  no_telemetry: { name: 'No telemetry', short: 'No data', color: '#7d8fa1', kind: 'unknown' },
  unknown: { name: 'Unknown', short: 'Unknown', color: '#98a4ad', kind: 'unknown' },
};
const PHASE_ORDER = Object.keys(PHASES);
const alphaOf = p => 1;
const swatch = p => p === 'no_telemetry' ? `<i class="color-square hatch" style="background:#22313d"></i>` : `<i class="color-square" style="background:${PHASES[p].color}"></i>`;
const ROLES = { background: { name: 'Background process', color: '#8a6d3b' }, parallel: { name: 'Parallel runs (same command)', color: '#7fa6c9' }, first: { name: 'First runs', color: '#d9bc3d' }, retry_after_failure: { name: 'Retry after failure', color: '#d77729' }, rerun: { name: 'Reruns (after pass)', color: '#e2cf6a' }, fix: { name: 'Fix between attempts', color: '#b22998' }, infra_recovery: { name: 'Infra recovery', color: '#d76368' }, worker_queue: { name: 'Worker queue', color: '#48aa8c' }, single: { name: 'Single runs', color: '#b3a04a' }, remote: { name: 'On remote runner', color: '#8fb8d8' } };
const ROLE_ORDER = ['first', 'retry_after_failure', 'rerun', 'parallel', 'fix', 'infra_recovery', 'worker_queue', 'single', 'remote'];
const MARKS = { llm_error: { glyph: 'x', color: '#f0a742', name: 'LLM failure (invalid tool call)' }, system_message: { glyph: 'sys', color: '#7d8fa1', name: 'Harness message' }, user_message: { glyph: 'user', color: '#ec838d', name: 'User message' }, question: { glyph: 'q', color: '#f2a7b2', name: 'Question to user' }, final_answer: { glyph: 'check', color: '#4aa65f', name: 'Final answer' }, interrupted: { glyph: 'x', color: '#d76368', name: 'Interrupted' }, compaction: { glyph: 'diamond', color: '#c08a45', name: 'Context compaction' }, plan: { glyph: 'plan', color: '#c9b2ff', name: 'Plan update' }, result_returned: { glyph: 'result', color: '#48aa8c', name: 'Sub-agent result' }, agent_started: { glyph: 'spawn', color: '#86d0b9', name: 'Sub-agent spawned' }, agent_interacted: { glyph: 'tick', color: '#86d0b9', name: 'Message to sub-agent' }, agent_completed: { glyph: 'done', color: '#86d0b9', name: 'Sub-agent turn completed' }, agent_interrupted: { glyph: 'x', color: '#d76368', name: 'Sub-agent interrupted' } };

const state = { page: 'sessions', id: null, model: null, sessions: [], a: 0, b: 0, follow: false, interval: 60, expanded: new Set(), hiddenLanes: new Set(), phase: 'all', role: 'all', lane: 'all', sort: 'longest', listPage: 0, search: '', fleetSort: 'updated', selected: null, groups: true, inTurn: false, allLanes: false, failedOnly: false, breakdownSort: 'longest', convOpen: new Set(), version: null, lastRefresh: 0, pollTimer: null, tick: null };
let geometry = null, overviewDrag = null, plotDrag = null, lastDragTime = 0, toastTimer, resizeTimer, tipTimer;
// Async work may finish after navigation, another selection, or a newer refresh.
let navigationRequest = 0, sessionRequest = 0, sessionsRequest = 0, inspectorRequest = 0, pollSchedule = 0, pollBusy = false;
let requestedSessionID = null; // The loaded model may still belong to the previous navigation.

/* ---------- formatting ---------- */
function fmt(ms, seconds = false) { let n = Math.max(0, Math.round(ms / 1000)); const h = Math.floor(n / 3600), m = Math.floor(n % 3600 / 60), s = n % 60; if (h >= 48) { const d = Math.floor(h / 24); return `${d}d ${h % 24}h`; } return h ? `${h}h${m ? ' ' + m + 'm' : ''}${seconds && s ? ' ' + s + 's' : ''}` : m ? `${m}m${seconds && s ? ' ' + s + 's' : ''}` : `${s}s`; }
const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
function stamp(t, withDate = true) { const d = new Date(t); return (withDate ? `${pad(d.getDate())} ${MONTHS[d.getMonth()]} ` : '') + `${pad(d.getHours())}:${pad(d.getMinutes())}`; }
function stampS(t) { const d = new Date(t); return `${pad(d.getDate())} ${MONTHS[d.getMonth()]} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`; }
function spanLabel(a, b) { const x = new Date(a), y = new Date(b); return `${stamp(a)} → ${stamp(b, x.toDateString() !== y.toDateString())}`; }
const TZ = (() => { try { return new Intl.DateTimeFormat().resolvedOptions().timeZone; } catch (e) { return 'local'; } })();
function ago(ms) { const s = Math.round((Date.now() - ms) / 1000); return s < 60 ? `${s}s ago` : s < 3600 ? `${Math.floor(s / 60)}m ago` : `${Math.floor(s / 3600)}h ago`; }
function shortPath(p) { if (!p) return ''; const parts = p.split('/'); return parts.length > 2 ? '…/' + parts.slice(-2).join('/') : p; }
function modelOf(o) { const m = current(); const l = m.laneById.get(o.lane); const t = l && l.turns.find(t => t.id === o.turn); return (t && t.model) || (l && l.model) || ''; }
function effortOf(o) { const m = current(); const l = m.laneById.get(o.lane); const t = l && l.turns.find(t => t.id === o.turn); return (t && t.effort) || ''; }
function roleOf(kind) { const i = (kind || '').indexOf('|'); return i >= 0 ? kind.slice(i + 1) : ''; }
function baseKind(kind) { const i = (kind || '').indexOf('|'); return i >= 0 ? kind.slice(0, i) : (kind || ''); }

/* ---------- data ---------- */
async function api(path) { const r = await fetch(path, { cache: 'no-store' }); if (!r.ok) throw new Error(`${r.status} ${await r.text()}`); return r.json(); }
function current() { return state.model; }
function root() { return state.model?.lanes[0]; }
function laneById(id) { return state.model?.lanes.find(l => l.id === id); }
function prepare(m) {
  m.groups = m.groups || []; m.lanes = m.lanes || []; m.totals = m.totals || {}; m.totals.by_kind = m.totals.by_kind || {}; m.totals.by_phase = m.totals.by_phase || {};
  for (const l of m.lanes) { l.turns = l.turns || []; l.ops = l.ops || []; l.markers = l.markers || []; l.segments = l.segments || []; l.stages = l.stages || []; l.active = l.active || []; l.by_phase = l.by_phase || {}; }
  for (const l of m.lanes) for (const mk of l.markers) mk.text = mk.text ?? '';
  m.opById = new Map();
  m.laneById = new Map();
  for (const l of m.lanes) { m.laneById.set(l.id, l); l.ops.sort((a, b) => a.start - b.start); for (const o of l.ops) m.opById.set(o.id, o); l.opsByEnd = [...l.ops].sort((a, b) => b.end - a.end); }
  m.groupById = new Map(m.groups.map(g => [g.id, g]));
  // sub-agent spawn/complete markers live on the parent lane; index them by child lane id
  m.agentMarks = new Map();
  for (const l of m.lanes) for (const mk of l.markers) if (mk.kind.startsWith('agent_') && mk.ref) { if (!m.agentMarks.has(mk.ref)) m.agentMarks.set(mk.ref, []); m.agentMarks.get(mk.ref).push(mk); }
  return m;
}
async function loadSessions() {
  const request = ++sessionsRequest, navigation = navigationRequest;
  const ownsRequest = () => request === sessionsRequest && navigation === navigationRequest;
  try {
    const sessions = await api('/api/sessions');
    if (!ownsRequest()) return false;
    state.sessions = sessions || []; $('#navCount').textContent = state.sessions.length;
    return true;
  } catch (e) { if (ownsRequest()) throw e; return false; }
}
async function loadSession(id, { keepWindow = false, isCurrent = () => true } = {}) {
  if (state.page !== 'session' || id !== requestedSessionID) return false;
  const request = ++sessionRequest, navigation = navigationRequest;
  const ownsRequest = () => request === sessionRequest && navigation === navigationRequest && state.page === 'session' && id === requestedSessionID && isCurrent();
  let m;
  try { m = await api('/api/sessions/' + encodeURIComponent(id)); }
  catch (e) { if (ownsRequest()) throw e; return false; }
  if (!ownsRequest()) return false;
  m = prepare(m);
  const prev = state.model;
  state.model = m; state.id = id; state.version = m.version; state.lastRefresh = Date.now();
  if (!keepWindow || !prev || prev.id !== id) { state.a = m.started; state.b = m.ended; state.expanded = new Set(); state.hiddenLanes = new Set(); state.phase = 'all'; state.role = 'all'; state.lane = 'all'; state.listPage = 0; state.selected = null; state.follow = m.live; }
  else if (state.follow) { const span = state.b - state.a; state.b = m.ended; state.a = Math.max(m.started, m.ended - span); }
  else if (Math.abs(prev.ended - state.b) < 1000) state.b = m.ended; // window was pinned to the end
  state.a = clamp(state.a, m.started, m.ended); state.b = clamp(state.b, state.a + 1000, m.ended);
  return true;
}

/* ---------- stats over a window (root lane exclusive partition) ---------- */
function windowStats(a, b) {
  const m = current(), r = root();
  const by = Object.fromEntries(PHASE_ORDER.map(k => [k, 0]));
  for (const sg of r.segments) { if (sg.e <= a) continue; if (sg.s >= b) break; by[sg.p] += overlap(sg.s, sg.e, a, b); }
  const roles = Object.fromEntries(ROLE_ORDER.map(k => [k, 0]));
  for (const o of r.ops) { if (o.start >= b) break; const ov = overlap(o.start, o.end, a, b); if (!ov) continue; const role = roleOf(o.kind); if (role) roles[role] += ov; else if (o.phase === 'test') roles.single += ov; if (o.remote && (o.phase === 'test' || o.phase === 'build')) roles.remote += ov; }
  let agentMs = 0, agentLanes = 0;
  const ivs = [];
  for (const l of m.lanes.slice(1)) { let any = false; for (const iv of l.active || []) { const ov = overlap(iv.s, iv.e, a, b); if (ov) { agentMs += ov; any = true; ivs.push([Math.max(iv.s, a), Math.min(iv.e, b)]); } } if (any) agentLanes++; }
  ivs.sort((x, y) => x[0] - y[0]); let wall = 0, cs = 0, ce = -1; for (const [s, e] of ivs) { if (s > ce) { if (ce > cs) wall += ce - cs; cs = s; ce = e; } else if (e > ce) ce = e; } if (ce > cs) wall += ce - cs;
  const work = ['code', 'build', 'test', 'release', 'infra'].reduce((n, k) => n + by[k], 0);
  const allBy = Object.fromEntries(PHASE_ORDER.map(k => [k, 0])), subBy = Object.fromEntries(PHASE_ORDER.map(k => [k, 0]));
  for (const l of m.lanes) for (const sg of l.segments) { if (sg.e <= a) continue; if (sg.s >= b) break; const ov = overlap(sg.s, sg.e, a, b); allBy[sg.p] += ov; if (l !== r) subBy[sg.p] += ov; }
  const stageBy = {}; for (const st of r.stages) { if (st.e <= a) continue; if (st.s >= b) break; stageBy[st.p] = (stageBy[st.p] || 0) + overlap(st.s, st.e, a, b); }
  let raw = 0, bg = 0, bgOps = 0; for (const o of r.ops) { if (o.start >= b) break; const ov = overlap(o.start, o.end, a, b); if (o.background) { bg += ov; if (ov) bgOps++; } else raw += ov; }
  let inTurn = 0; for (const t of r.turns) inTurn += overlap(t.start, t.status === 'open' ? m.now : t.end, a, b);
  const workOf = o => ['code', 'build', 'test', 'release', 'infra'].reduce((n, k) => n + o[k], 0);
  return { duration: b - a, by, allBy, subBy, stageBy, roles, work, raw, bg, bgOps, inTurn, llm: by.llm, allWork: workOf(allBy), subWork: workOf(subBy), waiting: by.wait_user + by.wait_worker + by.idle, overhead: by.compaction, unknown: by.no_telemetry + by.unknown, agentMs, agentWall: wall, agentLanes };
}
function opsInWindow(a, b) {
  const m = current(); const out = [];
  for (const l of m.lanes) { if (state.lane !== 'all' && state.lane !== l.id) continue; for (const o of l.ops) { if (o.start >= b) break; if (o.end <= a && !o.open) continue; if (o.end <= a) continue; out.push(o); } }
  return out;
}

/* ---------- navigation / shell ---------- */
function nav() {
  const m = current();
  $('#navSessions').classList.toggle('active', state.page === 'sessions'); $('#navSession').classList.toggle('active', state.page === 'session');
  $('#mobileSessions').classList.toggle('active', state.page === 'sessions'); $('#mobileSession').classList.toggle('active', state.page === 'session');
  $('#navSession').disabled = !m; $('#mobileSession').disabled = !m;
  $('#breadcrumb').innerHTML = `<span>Codex</span><span class="slash">/</span><button data-action="sessions">Sessions</button>${state.page === 'session' && m ? `<span class="slash">/</span><span class="mono">${esc(m.id.slice(0, 8))}</span>` : ''}`;
  $('#focusCard').innerHTML = m ? `<button class="current-card" data-action="session"><span class="row"><i class="dot" style="background:${m.live ? '#5fe0a0' : 'var(--accent)'}"></i><span class="mono">${esc(m.id.slice(0, 8))}</span></span><strong>${esc(m.title)}</strong><span>${fmt(m.ended - m.started)} · ${m.lanes.length - 1} sub-agents</span></button>` : '<div class="current-card"><span>No session open</span></div>';
  document.title = `${state.page === 'session' && m ? m.title : 'Sessions'} · todobem`;
  liveLabel();
}
function liveLabel() {
  const m = current(), el = $('#liveLabel');
  if (!m || state.page !== 'session') { el.innerHTML = ''; return; }
  el.innerHTML = m.live ? `<span class="chip live"><i class="dot"></i>LIVE · refreshed ${ago(state.lastRefresh)}</span>` : `<span class="chip">${icon('check', true)}Session closed · ${ago(m.ended)}</span>`;
}
function setHash(h) { try { if (location.hash !== h) history.pushState(null, '', h); } catch (e) { } }
function go(page, id) {
  const navigation = ++navigationRequest;
  ++sessionRequest; ++sessionsRequest; ++inspectorRequest;
  if ($('#inspector').open) $('#inspector').close();
  stopPoll();
  state.page = page;
  requestedSessionID = page === 'session' ? id || state.id : null;
  if (page === 'session' && id && id !== state.id) {
    setHash('#session/' + encodeURIComponent(id)); $('#main').innerHTML = '<div class="loading">Parsing session…</div>';
    loadSession(id).then(applied => { if (applied && navigation === navigationRequest) { render(); schedulePoll(); } }).catch(e => {
      if (navigation !== navigationRequest) return;
      console.error(e); $('#main').innerHTML = `<div class="empty"><h3>Could not load session</h3><p>${esc(e.message)}</p></div>`;
    });
    return;
  }
  if (page === 'session' && id) setHash('#session/' + encodeURIComponent(id));
  if (page === 'sessions') {
    setHash('#sessions');
    loadSessions().then(applied => { if (applied && navigation === navigationRequest) render(); }).catch(e => {
      if (navigation === navigationRequest) toast('Could not refresh sessions: ' + e.message);
    });
  }
  render(); schedulePoll(); window.scrollTo(0, 0);
}
function render() { nav(); if (state.page === 'sessions') { $('#main').innerHTML = fleetPage(); renderFleetRows(); } else if (current()) { $('#main').innerHTML = sessionPage(); renderOverview(); renderTimeline(); renderLower(); } $$('[data-icon]').forEach(el => el.innerHTML = icon(el.dataset.icon)); }
function toast(text) { const el = $('#toast'); el.textContent = text; el.classList.add('visible'); clearTimeout(toastTimer); toastTimer = setTimeout(() => el.classList.remove('visible'), 3500); }

/* ---------- polling ---------- */
function stopPoll() {
  ++pollSchedule;
  clearInterval(state.pollTimer); clearInterval(state.tick); state.pollTimer = null; state.tick = null;
}
function schedulePoll() {
  stopPoll();
  if (state.page !== 'session' || !current() || state.id !== requestedSessionID) return;
  const schedule = pollSchedule, navigation = navigationRequest, id = state.id;
  const ownsPoll = () => schedule === pollSchedule && navigation === navigationRequest && state.page === 'session' && state.id === id;
  state.tick = setInterval(liveLabel, 5000);
  const poll = async () => {
    if (pollBusy || document.hidden || !ownsPoll()) return;
    pollBusy = true;
    let request = sessionRequest;
    try {
      const v = await api(`/api/sessions/${encodeURIComponent(id)}/version`);
      if (!ownsPoll() || request !== sessionRequest) return;
      if (v.version !== state.version) {
        const loading = loadSession(id, { keepWindow: true, isCurrent: ownsPoll });
        request = sessionRequest;
        const applied = await loading;
        if (!applied || !ownsPoll()) return;
        render(); toast(current().live ? 'Live update applied.' : 'Session updated.');
      } else { state.lastRefresh = Date.now(); liveLabel(); }
    } catch (e) { if (ownsPoll() && request === sessionRequest) console.warn(e); }
    finally { pollBusy = false; }
  };
  state.pollTimer = setInterval(poll, state.interval * 1000);
}

/* ---------- fleet page ---------- */
function fleetPage() {
  return `<section class="page-heading"><div><div class="eyebrow">Codex sessions on this machine</div><h1>Sessions</h1><p class="subtitle">Root threads under ~/.codex/sessions. Open one to see where the time went.</p></div><span class="chip">${state.sessions.length} sessions</span></section>
<section class="fleet-summary" id="fleetSummary"></section>
<section class="card"><div class="fleet-toolbar"><label class="search-box">${icon('search', true)}<input id="sessionSearch" type="search" value="${esc(state.search)}" placeholder="Search title, path, branch…" aria-label="Search sessions"></label><label class="sr-only" for="fleetSort">Sort sessions</label><select class="select" id="fleetSort"><option value="updated" ${state.fleetSort === 'updated' ? 'selected' : ''}>Recently updated</option><option value="size" ${state.fleetSort === 'size' ? 'selected' : ''}>Largest logs</option><option value="agents" ${state.fleetSort === 'agents' ? 'selected' : ''}>Most sub-agents</option><option value="started" ${state.fleetSort === 'started' ? 'selected' : ''}>Started first</option></select></div><div id="fleetRows"></div></section>${footer()}`;
}
function renderFleetRows() {
  const q = state.search.toLowerCase();
  const rows = state.sessions.filter(s => `${s.title} ${s.cwd} ${s.branch || ''} ${s.id}`.toLowerCase().includes(q)).sort((a, b) => state.fleetSort === 'size' ? b.bytes - a.bytes : state.fleetSort === 'agents' ? b.agents - a.agents : state.fleetSort === 'started' ? a.started - b.started : b.updated - a.updated);
  const live = state.sessions.filter(s => Date.now() - s.updated < 10 * 60e3).length;
  $('#fleetSummary').innerHTML = `<div><strong class="num">${rows.length}</strong><span>Sessions in view</span></div><div><strong class="num">${sum(rows.map(s => s.agents))}</strong><span>Sub-agent threads</span></div><div><strong class="num">${(sum(rows.map(s => s.bytes)) / 1e6).toFixed(0)} MB</strong><span>Rollout logs</span></div><div><strong class="num">${live}</strong><span>Written in the last 10 min</span></div>`;
  const link = s => `<button class="session-link" data-action="session" data-id="${esc(s.id)}"><strong>${esc(s.title)}</strong><span>${esc(shortPath(s.cwd))}${s.branch ? ' · ' + esc(s.branch) : ''} · ${esc(s.model || '')}${s.cli ? ' · cli ' + esc(s.cli) : ''}</span></button>`;
  const stat = s => s.totals ? `<span class="mono">${fmt(s.totals.elapsed_ms)}</span>` : `<span class="mono" style="color:var(--subtle)" title="from file timestamps; open to parse">≈ ${fmt(Math.max(0, s.updated - s.started))}</span>`;
  $('#fleetRows').innerHTML = rows.length ? `<table class="fleet-table"><thead><tr><th>Session</th><th>Started</th><th>Updated</th><th>Elapsed</th><th>Sub-agents</th><th>Log size</th></tr></thead><tbody>${rows.map(s => `<tr><td>${link(s)}</td><td class="mono">${stamp(s.started)}</td><td class="mono">${stamp(s.updated)}${Date.now() - s.updated < 10 * 60e3 ? ' <span class="chip live" style="padding:2px 6px"><i class="dot"></i>active</span>' : ''}</td><td>${stat(s)}</td><td class="mono">${s.agents}</td><td class="mono">${(s.bytes / 1e6).toFixed(1)} MB</td></tr>`).join('')}</tbody></table><div class="mobile-fleet">${rows.map(s => `<article class="session-tile">${link(s)}<div class="session-tile-metrics"><div><strong class="mono">${stamp(s.updated)}</strong><small>Updated</small></div><div><strong class="mono">${s.agents}</strong><small>Sub-agents</small></div><div><strong class="mono">${(s.bytes / 1e6).toFixed(0)} MB</strong><small>Log</small></div></div></article>`).join('')}</div>` : `<div class="empty"><h3>No sessions match.</h3><p>Try another search.</p></div>`;
}
function footer() { return `<footer class="page-footer"><span class="row">${icon('shield', true)}Local files only. Nothing leaves this machine.</span><span>Times shown in ${esc(TZ)} · todobem / 0.1</span></footer>`; }

/* ---------- session page ---------- */
function fmtTok(n) { n = Number(n) || 0; return n >= 1e9 ? (n / 1e9).toFixed(2) + ' B' : n >= 1e6 ? (n / 1e6).toFixed(1) + ' M' : n >= 1e3 ? (n / 1e3).toFixed(0) + ' k' : String(n); }
function metricsHTML() {
  const m = current(), t = windowStats(m.started, m.ended), tk = m.totals.tokens || {};
  const working = t.duration - t.by.wait_user;
  const comp = l => l.ops.reduce((acc, o) => o.phase === 'compaction' ? { n: acc.n + 1, ms: acc.ms + (o.end - o.start) } : acc, { n: 0, ms: 0 });
  const rootComp = comp(m.lanes[0]), subComp = m.lanes.slice(1).map(comp).reduce((a, b) => ({ n: a.n + b.n, ms: a.ms + b.ms }), { n: 0, ms: 0 });
  const A = t.allBy, S = t.subBy, sub = (rootV, subV) => `root ${fmt(rootV)} · sub-agents ${fmt(subV)}`;
  const cells = [
    ['Working time', fmt(working), `wall clock minus waiting for user input · ${fmt(t.inTurn)} inside turns`, 'code'],
    ['Tokens', fmtTok(tk.total), tk.total ? `in ${fmtTok(tk.input)} (cached ${fmtTok(tk.cached)}) · out ${fmtTok(tk.output)} · reasoning ${fmtTok(tk.reasoning)} · all lanes` : 'no token_count events', 'brain'],
    ['LLM', fmt(A.llm), `all lanes · ${sub(t.by.llm, S.llm)}`, 'brain'],
    ['Tools', fmt(t.allWork), `code, build, test, release, infra · all lanes · ${sub(t.work, t.subWork)}`, 'code'],
    ['Known waiting', fmt(t.by.wait_user + A.wait_worker), `user ${fmt(t.by.wait_user)} · workers/harness ${fmt(A.wait_worker)} (all lanes)`, 'wait'],
    ['Context compaction', fmt(rootComp.ms + subComp.ms), `${rootComp.n + subComp.n}× the harness re-summarized a context window (root ${rootComp.n}× ${fmt(rootComp.ms)} · sub-agents ${subComp.n}× ${fmt(subComp.ms)}) · ${fmt(A.compaction)} of it not overlapping other work${m.totals.background_ops ? ` · ${m.totals.background_ops} background proc. ${fmt(m.totals.background_ms)} not counted` : ''}`, 'loop'],
    ['No telemetry / unattributed', fmt(A.no_telemetry + A.unknown), `no events ${fmt(A.no_telemetry)} · unmatched events ${fmt(A.unknown)} · all lanes`, 'gap'],
    ['Sub-agent time', fmt(t.agentMs), `${t.agentLanes} lanes · ${fmt(t.agentWall)} wall · runs in parallel with the root`, 'agents'],
  ];
  return `<section class="metrics" aria-label="Whole-session accounting">${cells.map(([label, v, note, ic]) => `<div class="metric"><div class="metric-label">${icon(ic, true)}${label}</div><strong class="metric-value num">${v}</strong><span class="metric-note">${esc(note)}</span></div>`).join('')}</section><p class="metrics-note">LLM, Tools, waiting, compaction and telemetry sum agent time over all lanes (sub-agents run in parallel, so they can exceed the wall clock). Elapsed and Working time are wall clock of the root.</p>`;
}
function sessionSummary() {
  const m = current(), r = root(), last = r.turns[r.turns.length - 1];
  const status = m.live ? { cls: 'live', text: 'In progress · turn open' } : last?.status === 'aborted' ? { cls: 'failed', text: 'Interrupted' } : last?.status === 'orphaned' ? { cls: '', text: 'Ended without closing the last turn' } : { cls: 'passed', text: 'Completed' };
  const firstMsg = r.markers.find(k => k.kind === 'user_message');
  const cells = [['Created', stampS(m.started)], ['First message', firstMsg ? stampS(firstMsg.t) + (firstMsg.t - m.started > 60e3 ? ` (+${fmt(firstMsg.t - m.started)})` : '') : '—'], [m.live ? 'Last event' : 'Ended', stampS(m.ended)], ['Duration', fmt(m.ended - m.started, true) + (m.live ? ' so far' : '')], ['Status', `<span class="chip ${status.cls}">${status.cls === 'live' ? '<i class="dot"></i>' : ''}${status.text}</span> <span class="mono" style="color:var(--subtle)">${r.turns.length} turns · ${m.totals.user_messages} user messages</span>`]];
  return `<dl class="session-summary">${cells.map(([k, v]) => `<div><dt>${k}</dt><dd>${v}</dd></div>`).join('')}</dl>`;
}
function stripMd(t) { return (t || '').replace(/\[([^\]]+)\]\([^)]*\)/g, '$1').replace(/[*_`#>]+/g, '').replace(/\s+/g, ' ').trim(); }
function lastState() {
  const m = current(), r = root(); const st = r.stages[r.stages.length - 1]; if (!st) return '<p>No activity recorded</p>';
  const op = r.opsByEnd.find(o => o.phase !== 'llm') || r.opsByEnd[0];
  const fin = [...r.markers].reverse().find(k => k.kind === 'final_answer');
  const rows = [];
  rows.push(['Status', m.live ? `<span class="chip live" style="padding:2px 7px"><i class="dot"></i>live</span> ${PHASES[st.p].name} since ${stamp(st.s, false)}` : `Closed ${stamp(m.ended)} · ${r.turns.length} turns`]);
  if (op) rows.push(['Last operation', `<button class="text-btn" data-action="inspect" data-id="${esc(op.id)}" style="text-align:left;font-weight:500"><i class="color-square" style="background:${PHASES[op.phase].color}"></i>${esc(op.title.slice(0, 90))}</button><small style="color:var(--subtle);display:block">${stampS(op.start)} · ${fmt(op.end - op.start, true)} · ${esc(op.status)}</small>`]);
  if (fin) rows.push(['Last answer', `<button class="text-btn" data-action="jump" data-t="${fin.t}" style="text-align:left;font-weight:500;white-space:normal">${esc(stripMd(fin.text).slice(0, 160))}${fin.text.length > 160 ? '…' : ''}</button><small style="color:var(--subtle);display:block">${stampS(fin.t)} · full text in Conversation</small>`]);
  return `<dl class="state-list">${rows.map(([k, v]) => `<div><dt>${k}</dt><dd>${v}</dd></div>`).join('')}</dl>`;
}
function sessionPage() {
  const m = current(), r = root(), firstUser = r.markers.find(k => k.kind === 'user_message');
  const status = m.live ? `<span class="chip live"><i class="dot"></i>Live · turn in progress</span>` : `<span class="chip">${icon('check', true)}Completed · ${r.turns.length} turns</span>`;
  return `<section class="page-heading"><div><div class="eyebrow">Session ${esc(m.id.slice(0, 8))} · ${esc(m.model || 'codex')} · cli ${esc(m.cli || '?')}</div><h1>${esc(m.title)}</h1><p class="subtitle">${icon('repo', true)}${esc(m.cwd)}${m.branch ? `<span>·</span>${esc(m.branch)}` : ''}<span>·</span>${stamp(m.started)} — ${stamp(m.ended)} ${esc(TZ)}</p></div><div class="heading-actions">${status}<label class="chip">Refresh <select class="select" id="intervalSelect" style="min-height:26px;padding:2px 6px">${[[30, '30s'], [60, '1 min'], [120, '2 min'], [300, '5 min']].map(([v, l]) => `<option value="${v}" ${state.interval === v ? 'selected' : ''}>${l}</option>`).join('')}</select></label><button class="btn" data-action="refresh">${icon('refresh', true)}Refresh now</button></div></section>
<section class="request-card" aria-label="Recorded user request"><div class="request-icon">${icon('target')}</div><div class="request-copy"><span class="eyebrow">First user message · ${firstUser ? stamp(firstUser.t) : 'not recorded'}</span><p>${esc(firstUser?.text || '—')}</p>${sessionSummary()}</div><div class="recorded-state"><span class="eyebrow">Last recorded state</span><div id="lastRecorded">${lastState()}</div></div></section>
<div id="sessionMetrics">${metricsHTML()}</div>
<section class="card timeline-card" aria-labelledby="timelineTitle"><div class="card-head"><div><h2 id="timelineTitle">Session timeline</h2><p><span id="totalOps">${m.totals.ops}</span> operations · ${m.groups.length} retry groups · ${m.lanes.length - 1} sub-agent lanes · ${m.totals.user_messages} user messages</p></div><div class="actions"><button class="btn ghost" data-action="fit" title="Show the entire session">${icon('expand', true)}Fit all</button><button class="btn ghost ${state.follow ? 'active' : ''}" data-action="follow" id="followBtn" aria-pressed="${state.follow}">${icon('latest', true)}Follow latest</button></div></div>
<div class="overview-section"><div class="overview-heading"><strong id="overviewDuration">${fmt(m.ended - m.started)} overview · root lane${m.lanes.length > 1 ? ' · sub-agent activity' : ''} · errors (red = tools, orange = LLM) below</strong><span>Drag to select a window · handles resize it</span><span class="mono" id="overviewRange"></span></div><div id="overview" class="overview" aria-label="Session overview. Drag to select a time window."><svg id="overviewSvg" aria-hidden="true"></svg><div class="brush-shade" id="shadeLeft"></div><div class="brush-shade" id="shadeRight"></div><div class="brush" id="brush"><div class="brush-handle left" data-handle="start" tabindex="0" role="slider" aria-label="Visible window start"></div><div class="brush-handle right" data-handle="end" tabindex="0" role="slider" aria-label="Visible window end"></div></div></div><div class="overview-axis" id="overviewAxis"></div><div class="legend" id="legend">${PHASE_ORDER.filter(k => k !== 'idle').map(k => `<span class="row">${swatch(k)}${PHASES[k].name}</span>`).join('')}</div></div>
<div class="timeline-toolbar"><div class="zoom-group"><div class="zoom-presets" aria-label="Visible time window">${[[0, 'All'], [86400000, '24h'], [21600000, '6h'], [3600000, '1h'], [900000, '15m'], [300000, '5m']].map(([n, l]) => `<button data-action="preset" data-span="${n}">${l}</button>`).join('')}</div><div class="zoom-step" aria-label="Zoom controls"><button data-action="zoom" data-dir="-1" aria-label="Zoom out">−</button><span class="zoom-caption" id="zoomCaption"></span><button data-action="zoom" data-dir="1" aria-label="Zoom in">+</button></div></div><div class="toolbar-right"><label><input id="showGroups" type="checkbox" ${state.groups ? 'checked' : ''}>Retry groups</label><button class="btn small ghost" data-action="expand-all">Expand all lanes</button><button class="btn small ghost" data-action="collapse-all">Collapse</button></div></div>
<div class="range-bar"><span class="range-label" id="rangeLabel"></span><div class="nav-arrows"><button class="btn icon-only" data-action="pan" data-dir="-1" aria-label="Move to earlier time">${icon('left', true)}</button><button class="btn icon-only" data-action="pan" data-dir="1" aria-label="Move to later time">${icon('right', true)}</button></div></div>
<div class="timeline-plot" id="plot" tabindex="0" role="group" aria-label="Interactive session timeline"><svg id="chartSvg" aria-hidden="true"></svg></div>

<div class="chart-footer"><span class="chart-note" id="chartNote"></span><span class="chart-shortcuts"><kbd>+</kbd> <kbd>−</kbd> zoom · <kbd>←</kbd> <kbd>→</kbd> pan · <kbd>Home</kbd> fit · drag to pan</span></div><div class="legend legend-marks" id="legendMarks"><span class="row"><svg width="12" height="12"><path d="M6 1l5 10H1z" fill="#ec838d"/></svg>User message</span><span class="row"><svg width="12" height="12"><circle cx="6" cy="6" r="5" fill="#4aa65f"/></svg>Final answer</span><span class="row"><svg width="14" height="12"><rect x="0" y="5" width="14" height="3" rx="1.5" fill="#c9a15c"/></svg>Background process</span><span class="row"><svg width="12" height="12"><circle cx="6" cy="6" r="5.5" fill="#4aa65f"/><path d="M3 6l2 2 4-4" stroke="#0f1b23" stroke-width="1.6" fill="none"/></svg>Turn completed</span><span class="row"><svg width="12" height="12"><circle cx="6" cy="6" r="5.5" fill="#d76368"/><path d="M3.5 3.5l5 5m0-5l-5 5" stroke="#0f1b23" stroke-width="1.6"/></svg>Interrupted</span><span class="row"><svg width="12" height="12"><circle cx="6" cy="6" r="5" fill="#22313d" stroke="#7d8fa1" stroke-width="1.5"/><path d="M2.5 9.5l7-7" stroke="#7d8fa1" stroke-width="1.5"/></svg>Never closed</span><span class="row"><svg width="14" height="12"><path d="M1 8v-3h12v3" fill="none" stroke="#4690ce" stroke-width="1.5"/></svg>Stage bracket (tool calls + LLM time before them)</span><span class="row"><svg width="12" height="12"><path d="M6 2l4.5 8h-9z" fill="#e05252"/></svg>Tool failure (non-zero exit)</span><span class="row"><svg width="12" height="12"><path d="M6 2l4.5 8h-9z" fill="#f0a742"/></svg>LLM failure: invalid tool call / broken exec script</span></div></section>
<div class="analysis-grid"><section class="card" aria-labelledby="breakdownTitle"><div class="card-head"><div><h2 id="breakdownTitle">Time in this window</h2><p id="breakdownScope"></p></div><span class="scope" id="breakdownScopeChip">Root lane · exclusive</span></div><div id="breakdownBody" class="breakdown-body"></div><div class="panel-foot">${icon('info', true)}Sub-agent time runs in parallel and is listed separately. Gaps are unknown, not idle.</div></section>
<section class="card" aria-labelledby="operationsTitle"><div class="card-head"><div><h2 id="operationsTitle">Operations in view</h2><p id="operationCount"></p></div><span class="scope">All lanes</span></div><div class="operation-tools"><select class="select" id="phaseSelect" aria-label="Filter by phase"><option value="all">All phases</option>${PHASE_ORDER.map(k => `<option value="${k}">${PHASES[k].name}</option>`).join('')}</select><select class="select" id="laneSelect" aria-label="Filter by lane"><option value="all">All lanes</option>${m.lanes.map(l => `<option value="${esc(l.id)}">${esc(l.path)}</option>`).join('')}</select><select class="select" id="sortSelect" aria-label="Sort operations"><option value="longest">Longest in window</option><option value="latest">Latest first</option><option value="earliest">Earliest first</option><option value="failed">Failed first</option></select><label class="row chip" style="cursor:pointer"><input type="checkbox" id="failedOnly" ${state.failedOnly ? 'checked' : ''}>Failures only</label></div><div id="roleFilter"></div><div class="operation-list" id="operationList"></div><div class="list-footer" id="listFooter"></div></section></div>
<div class="analysis-grid"><section class="card" aria-labelledby="convTitle"><div class="card-head"><div><h2 id="convTitle">Conversation</h2><p>User messages, questions and final answers, verbatim. Select one to jump there.</p></div><span class="scope">${m.totals.user_messages} inputs</span></div><div class="conv" id="conversation"></div></section>
<section class="card" aria-labelledby="agentsTitle"><div class="card-head"><div><h2 id="agentsTitle">Agents</h2><p>One lane per thread. Active time = the thread's own turns.</p></div><span class="scope">${m.lanes.length} lanes</span></div><div class="agents-wrap"><table class="agents-table" id="agentsTable"></table></div></section></div>
${footer()}`;
}

/* ---------- overview ---------- */
function renderOverview() {
  const m = current(), r = root(), el = $('#overviewSvg'); if (!el) return;
  const W = 1000, H = 44, N = 250, span = m.ended - m.started, bin = span / N, SUB = m.lanes.length > 1 ? 12 : 0, ERR = 12;
  const bins = Array.from({ length: N }, () => ({}));
  for (const sg of r.segments) { let i = Math.max(0, Math.floor((sg.s - m.started) / bin)); for (; i < N; i++) { const a = m.started + i * bin, b = a + bin; if (a >= sg.e) break; const ov = overlap(sg.s, sg.e, a, b); if (ov > 0) bins[i][sg.p] = (bins[i][sg.p] || 0) + ov; } }
  let html = `<defs><pattern id="ovGap" width="5" height="5" patternUnits="userSpaceOnUse" patternTransform="rotate(35)"><path d="M0 0v5" stroke="#7d8fa1" stroke-width="1.5"/></pattern></defs>`;
  const order = ['wait_user', 'idle', 'no_telemetry', 'compaction', 'llm', 'unknown', 'wait_worker', 'code', 'build', 'infra', 'test', 'release'];
  bins.forEach((b, i) => { let y = H; for (const p of order) { const n = b[p]; if (!n) continue; const h = n / bin * H; y -= h; const fill = p === 'no_telemetry' ? 'url(#ovGap)' : PHASES[p].color; const op = p === 'no_telemetry' ? .75 : 1; html += `<rect x="${(i * 4).toFixed(1)}" y="${y.toFixed(2)}" width="4.05" height="${(h + .05).toFixed(2)}" fill="${fill}" opacity="${op}"/>`; } });
  // user message ticks
  for (const mk of r.markers) if (mk.kind === 'user_message') { const x = (mk.t - m.started) / span * W; html += `<rect x="${x.toFixed(1)}" y="0" width="1.5" height="6" fill="#ec838d"/>`; }
  // sub-agent activity band: how many sub-agent lanes are active in each bin
  if (SUB) {
    const cnt = new Array(N).fill(0), maxN = Math.max(1, m.lanes.length - 1);
    for (const l of m.lanes.slice(1)) for (const iv of l.active || []) { let i = Math.max(0, Math.floor((iv.s - m.started) / bin)); for (; i < N; i++) { const a = m.started + i * bin; if (a >= iv.e) break; if (overlap(iv.s, iv.e, a, a + bin) > bin * .2) cnt[i]++; } }
    html += `<rect x="0" y="${H}" width="${W}" height="${SUB}" fill="#132029"/>`;
    cnt.forEach((n, i) => { if (!n) return; const h = Math.max(2, SUB * Math.min(1, n / Math.min(maxN, 6))); html += `<rect x="${(i * 4).toFixed(1)}" y="${(H + SUB - h).toFixed(1)}" width="4.05" height="${h.toFixed(1)}" fill="#86d0b9" opacity="${(.45 + .55 * Math.min(1, n / 4)).toFixed(2)}"/>`; });
  }
  // errors band: red = tool failures (non-zero exit / failed status), orange = LLM failures
  // (invalid tool arguments, exec scripts that failed before running anything)
  const tool = new Array(N).fill(0), llm = new Array(N).fill(0), names = Array.from({ length: N }, () => []);
  const isLlmErr = o => o.kind === 'exec-script-error' || baseKind(o.kind) === 'llm-invalid-args';
  for (const l of m.lanes) for (const o of l.ops) { if (o.status !== 'failed' || o.background) continue; const i = clamp(Math.floor((o.start - m.started) / bin), 0, N - 1); if (isLlmErr(o)) llm[i]++; else tool[i]++; if (names[i].length < 6) names[i].push(`${stamp(o.start, false)} ${isLlmErr(o) ? 'LLM' : 'exit ' + (o.exit ?? '?')} · ${o.title.slice(0, 60)}`); }
  const y0 = H + SUB;
  html += `<rect x="0" y="${y0}" width="${W}" height="${ERR}" fill="#1a1f2a"/>`;
  for (let i = 0; i < N; i++) { const t = tool[i], u = llm[i]; if (!t && !u) continue; const ht = t ? Math.max(3, Math.min(ERR - 1, 3 + 2 * t)) : 0, hu = u ? Math.max(3, Math.min(ERR - 1, 3 + 2 * u)) : 0; const tip = `<title>${esc(`${t} tool failure${t === 1 ? '' : 's'}, ${u} LLM failure${u === 1 ? '' : 's'}\n` + names[i].join('\n'))}</title>`; if (t) html += `<rect x="${(i * 4).toFixed(1)}" y="${(y0 + ERR - ht).toFixed(1)}" width="4.05" height="${ht}" fill="#e05252" opacity="${(.6 + .4 * Math.min(1, t / 4)).toFixed(2)}">${tip}</rect>`; if (u) html += `<rect x="${(i * 4).toFixed(1)}" y="${(y0 + ERR - ht - hu).toFixed(1)}" width="4.05" height="${hu}" fill="#f0a742" opacity=".95">${tip}</rect>`; }
  el.setAttribute('viewBox', `0 0 ${W} ${H + SUB + ERR}`); el.setAttribute('preserveAspectRatio', 'none'); el.innerHTML = html;
  $('#overviewAxis').innerHTML = [0, 1 / 3, 2 / 3, 1].map(n => `<span>${stamp(m.started + span * n)}</span>`).join('');
  updateBrush();
}
function updateBrush() {
  const m = current(); if (!$('#brush')) return; const span = m.ended - m.started, left = (state.a - m.started) / span * 100, width = (state.b - state.a) / span * 100;
  $('#brush').style.left = left + '%'; $('#brush').style.width = width + '%'; $('#shadeLeft').style.left = '0'; $('#shadeLeft').style.width = left + '%'; $('#shadeRight').style.right = '0'; $('#shadeRight').style.width = (100 - left - width) + '%';
  $('#overviewRange').textContent = `${fmt(state.b - state.a)} selected`;
}

/* ---------- timeline ---------- */
function niceTick(spanMs, width) { const target = spanMs / Math.max(2, Math.floor(width / 96)); return [10e3, 30e3, 60e3, 120e3, 300e3, 600e3, 900e3, 1800e3, 3600e3, 7200e3, 10800e3, 21600e3, 43200e3, 86400e3, 172800e3].find(n => n >= target) || 604800e3; }
function laneRows() {
  const m = current(); const rows = [];
  for (const l of m.lanes) {
    if (state.hiddenLanes.has(l.id)) continue;
    rows.push({ kind: 'lane', lane: l, h: l.depth === 0 ? 54 : 44 });
    if (l.depth === 0) { rows.push({ kind: 'markers', lane: l, h: 22 }); if (state.groups && m.groups.length) rows.push({ kind: 'groups', h: 26 }); }
    if (state.expanded.has(l.id)) { const present = PHASE_ORDER.filter(p => l.by_phase[p] > 0 && p !== 'idle' && p !== 'wait_user'); for (const p of present) rows.push({ kind: 'phase', lane: l, phase: p, h: 22 }); }
  }
  return rows;
}
// Bucketize intervals ([s,e,phase,...]) over the window into pixel columns; return merged runs.
function bucketRuns(items, a, b, P, bw, pick) {
  const n = Math.max(1, Math.ceil(P / bw)); const cols = new Array(n).fill(null); const per = (b - a) / n;
  for (const it of items) { if (it.e <= a || it.s >= b) continue; let i = Math.max(0, Math.floor((it.s - a) / per)); const last = Math.min(n - 1, Math.floor((it.e - a - 1) / per)); for (; i <= last; i++) { const ca = a + i * per, ov = overlap(it.s, it.e, ca, ca + per); if (ov <= 0) continue; if (!cols[i]) cols[i] = {}; const k = pick(it); cols[i][k] = (cols[i][k] || 0) + ov; cols[i].__ops = (cols[i].__ops || 0) + (it.n || (it.op ? 1 : 0)); } }
  const runs = []; let cur = null;
  for (let i = 0; i < n; i++) {
    const c = cols[i]; if (!c) { cur = null; continue; }
    let best = null, bestV = -1, total = 0; for (const k in c) { if (k === '__ops') continue; total += c[k]; if (c[k] > bestV) { bestV = c[k]; best = k; } }
    const cov = clamp(total / per, 0, 1);
    if (cur && cur.key === best && cur.end === i) { cur.end = i + 1; cur.cov = (cur.cov * cur.n + cov) / (cur.n + 1); cur.n++; cur.ops += c.__ops || 0; } else { cur = { key: best, start: i, end: i + 1, cov, n: 1, ops: c.__ops || 0 }; runs.push(cur); }
  }
  return { runs, per, n };
}
function renderTimeline() {
  const m = current(); if (state.page !== 'session' || !$('#chartSvg')) return;
  const host = $('#plot'), W = Math.max(280, host.clientWidth), mobile = W < 560, L = mobile ? 88 : 168, R = mobile ? 10 : 20, P = W - L - R, span = state.b - state.a, x = t => L + (t - state.a) / span * P, top = 30;
  const rows = laneRows(); let H = top + sum(rows.map(r => r.h)) + 8;
  geometry = { W, L, R, P, span, x, rows, top, H, mobile };
  const bw = 3; // px per bucket
  let html = `<defs><clipPath id="plotClip"><rect x="${L}" y="0" width="${P}" height="${H}"/></clipPath><pattern id="gapHatch" width="6" height="6" patternUnits="userSpaceOnUse" patternTransform="rotate(35)"><rect width="6" height="6" fill="#22313d"/><path d="M0 0v6" stroke="#7d8fa1" stroke-width="1.3"/></pattern><pattern id="waitHatch" width="5" height="5" patternUnits="userSpaceOnUse" patternTransform="rotate(35)"><rect width="5" height="5" fill="#48aa8c"/><path d="M0 0v5" stroke="#236854" stroke-width="1"/></pattern></defs>`;
  const ticks = niceTick(span, P);
  for (let t = Math.ceil(state.a / ticks) * ticks; t <= state.b; t += ticks) { const xx = x(t); html += `<line x1="${xx}" y1="26" x2="${xx}" y2="${H - 4}" stroke="#2b3c48" stroke-width=".65"/>`; if (xx < L + 12 || xx > W - R - 14) continue; const d = new Date(t), midnight = d.getHours() === 0 && d.getMinutes() === 0; const lbl = span > 86400e3 * 2 || midnight ? stamp(t) : ticks < 60e3 ? stampS(t).slice(-8) : stamp(t, false); html += `<text class="axis-label" x="${xx}" y="17" text-anchor="middle" ${midnight ? 'font-weight="700"' : ''}>${lbl}</text>`; }
  for (const tn of root().turns) { if (tn.start >= state.a && tn.start <= state.b) { const xx = x(tn.start); html += `<line x1="${xx}" y1="${top}" x2="${xx}" y2="${H - 4}" stroke="#ec838d" stroke-width="1" stroke-dasharray="2 4" opacity=".55"/>`; } }
  let y = top;
  const fillFor = p => p === 'no_telemetry' ? 'url(#gapHatch)' : p === 'wait_worker' ? 'url(#waitHatch)' : PHASES[p]?.color || '#888';
  const opacityFor = alphaOf;
  rows.forEach((row, ri) => {
    const rh = row.h, bg = ri % 2 ? '#1c2a35' : '#20303c';
    html += `<rect x="0" y="${y}" width="${W}" height="${rh}" fill="${bg}" opacity=".75"/>`;
    if (row.kind === 'lane') {
      const l = row.lane, sel = state.lane === l.id, indent = Math.min(l.depth, 3) * (mobile ? 6 : 10), name = l.depth === 0 ? 'root' : l.path.split('/').pop();
      const exp = state.expanded.has(l.id);
      html += `<g data-lane="${esc(l.id)}" class="lane-head"><rect x="0" y="${y}" width="${L - 4}" height="${rh}" fill="transparent"/><path d="${exp ? `m${8 + indent} ${y + rh / 2 - 3} 3.5 4 3.5-4` : `m${9 + indent} ${y + rh / 2 - 4} 4 4-4 4`}" fill="none" stroke="#c7d5de" stroke-width="1.6"/><text class="lane-label ${sel ? 'selected' : ''}" x="${20 + indent}" y="${y + (l.depth === 0 ? rh / 2 - 2 : rh / 2 + 4)}">${esc(name.slice(0, mobile ? 9 : 17))}</text>${l.depth === 0 ? `<text class="lane-sub" x="${20 + indent}" y="${y + rh / 2 + 11}">${mobile ? `${l.turns.length}t · ${l.ops.length}` : `${l.turns.length} turns · ${l.ops.length} ops · ${esc(l.model || '')}`}</text>` : ''}</g>`;
      html += `<g clip-path="url(#plotClip)">`;
      if (l.depth > 0) { // lifetime box: from spawn (▶) to the last turn end / now
        const marks = m.agentMarks.get(l.id) || [];
        const spawn = marks.find(k => k.kind === 'agent_started');
        const t0 = spawn ? spawn.t : l.started, t1 = l.live ? m.now : l.ended;
        if (t1 > state.a && t0 < state.b) {
          const xa = x(Math.max(t0, state.a)), xb = x(Math.min(t1, state.b));
          html += `<rect x="${xa.toFixed(1)}" y="${y + 15}" width="${Math.max(2, xb - xa).toFixed(1)}" height="${rh - 18}" rx="4" fill="#233443" stroke="#4f6d80" stroke-width="1"/>`;
          // idle gaps between turns: dashed centre line (waiting for the parent)
          const cy0 = y + 17 + (rh - 22) / 2;
          html += `<line x1="${xa.toFixed(1)}" x2="${xb.toFixed(1)}" y1="${cy0}" y2="${cy0}" stroke="#4f6d80" stroke-width="1" stroke-dasharray="2 3"/>`;
        }
        for (const iv of l.active || []) { if (iv.e <= state.a || iv.s >= state.b) continue; html += `<rect x="${x(Math.max(iv.s, state.a))}" y="${y + 16}" width="${Math.max(1, x(Math.min(iv.e, state.b)) - x(Math.max(iv.s, state.a)))}" height="${rh - 20}" fill="#2e4655" rx="3"/>`; }
      }
      // Fill = the raw exclusive partition (purple is real model time). Stage brackets above the
      // fill name the run of tool calls (Explore / Code / Test …) as a grouping, not as time.
      const top0 = y + 17, fh = rh - 17 - 5; // fill box under the 16 px bracket band
      const segsVis = l.segments.filter(sg => sg.e > state.a && sg.s < state.b && !(l.depth > 0 && sg.p === 'idle'));
      const { runs, per } = bucketRuns(segsVis, state.a, state.b, P, bw, it => it.p);
      for (const run of runs) {
        const xa = L + run.start * bw, ww = Math.max(1, (run.end - run.start) * bw - (run.end - run.start > 1 ? .6 : 0)), p = run.key;
        const ta = state.a + run.start * per, tb = state.a + run.end * per;
        html += `<g class="mark" data-stage="1" data-lane="${esc(l.id)}" data-ta="${Math.round(ta)}" data-tb="${Math.round(tb)}" data-phase="${p}"><rect x="${xa.toFixed(1)}" y="${top0}" width="${ww.toFixed(1)}" height="${fh}" rx="${ww > 4 ? 2 : 0}" fill="${fillFor(p)}"/>`;
        if (ww > 52 && p !== 'wait_user' && p !== 'idle' && p !== 'llm') { const label = `${PHASES[p].short} ${fmt(tb - ta)}`; html += `<text class="op-label ${p === 'code' || p === 'infra' || p === 'compaction' ? 'light' : ''}" x="${xa + 5}" y="${top0 + fh / 2 + 3.5}">${esc(label.slice(0, Math.floor((ww - 8) / 5.8)))}</text>`; }
        html += `</g>`;
      }
      // stage brackets (tool-call runs of one phase, thinking before each call included)
      const stageVis = l.stages.filter(st => st.e > state.a && st.s < state.b && PHASES[st.p] && PHASES[st.p].kind === 'work');
      let lastLabelEnd = -1;
      for (const st of stageVis) {
        const xa = x(Math.max(st.s, state.a)), xb = x(Math.min(st.e, state.b)), ww = xb - xa; if (ww < 14) continue;
        const c = PHASES[st.p].color, ly = y + 15;
        html += `<g class="mark" data-stage="1" data-lane="${esc(l.id)}" data-ta="${st.s}" data-tb="${st.e}" data-phase="${st.p}" data-bracket="1"><rect x="${xa.toFixed(1)}" y="${y + 2}" width="${ww.toFixed(1)}" height="12" rx="2" fill="${c}" opacity=".16"/><path d="M${xa.toFixed(1)} ${ly}v-3h${ww.toFixed(1)}v3" fill="none" stroke="${c}" stroke-width="2"/><rect x="${xa.toFixed(1)}" y="${y}" width="${ww.toFixed(1)}" height="16" fill="transparent"/>`;
        const label = `${PHASES[st.p].short} ${fmt(st.e - st.s)}`; const lw = label.length * 6 + 8;
        if (ww >= lw && xa >= lastLabelEnd) { html += `<text x="${xa + 4}" y="${y + 11.5}" font-size="10" font-weight="700" fill="${c}">${esc(label)}</text>`; lastLabelEnd = xa + lw; }
        html += `</g>`;
      }
      // turn ends: ✓ completed · ✕ interrupted · ⊘ never closed
      for (const tn of l.turns) {
        if (tn.status === 'open' || tn.end < state.a || tn.end > state.b) continue;
        const xx = x(tn.end), cy = top0 + fh / 2, key = `${l.id}:${tn.id}`;
        if (tn.status === 'completed') html += `<g class="marker" data-turn="${esc(key)}"><circle cx="${xx}" cy="${cy}" r="6" fill="#4aa65f" stroke="#0f1b23" stroke-width="1.2"/><path d="M${xx - 3} ${cy}l2.2 2.2 4-4.2" stroke="#0f1b23" stroke-width="1.7" fill="none"/></g>`;
        else if (tn.status === 'aborted') html += `<g class="marker" data-turn="${esc(key)}"><circle cx="${xx}" cy="${cy}" r="6" fill="#d76368" stroke="#0f1b23" stroke-width="1.2"/><path d="M${xx - 2.8} ${cy - 2.8}l5.6 5.6m0-5.6l-5.6 5.6" stroke="#0f1b23" stroke-width="1.7"/></g>`;
        else html += `<g class="marker" data-turn="${esc(key)}"><circle cx="${xx}" cy="${cy}" r="6" fill="#22313d" stroke="#7d8fa1" stroke-width="1.5"/><path d="M${xx - 3.5} ${cy + 3.5}l7-7" stroke="#7d8fa1" stroke-width="1.5"/></g>`;
      }
      // sub-agent spawn / complete ticks from the parent's markers
      if (l.depth > 0) for (const mk of m.agentMarks.get(l.id) || []) { if (mk.t < state.a || mk.t > state.b) continue; const xx = x(mk.t); if (mk.kind === 'agent_started') html += `<g class="marker" data-mk="${esc(`${mk.lane}:${mk.t}:${mk.kind}`)}"><path d="M${xx - 5} ${y + 16}l10 ${(rh - 20) / 2}-10 ${(rh - 20) / 2}z" fill="#86d0b9" stroke="#0f1b23" stroke-width="1"/></g>`; else if (mk.kind === 'agent_completed') html += `<rect class="marker" data-mk="${esc(`${mk.lane}:${mk.t}:${mk.kind}`)}" x="${xx - 1}" y="${y + 16}" width="2" height="${rh - 20}" fill="#86d0b9" opacity=".5"/>`; else if (mk.kind === 'agent_interacted') html += `<rect class="marker" data-mk="${esc(`${mk.lane}:${mk.t}:${mk.kind}`)}" x="${xx - 1}" y="${y + rh - 9}" width="2" height="6" fill="#c7d5de" opacity=".8"/>`; else if (mk.kind === 'agent_interrupted') html += `<path class="marker" data-mk="${esc(`${mk.lane}:${mk.t}:${mk.kind}`)}" d="M${xx - 4} ${y + 6}l8 8m0-8l-8 8" stroke="#d76368" stroke-width="2"/>`; }
      for (const o of l.ops) { if (!o.background || o.end <= state.a || o.start >= state.b) continue; const xa = x(Math.max(o.start, state.a)), ww = Math.max(2, x(Math.min(o.end, state.b)) - xa); html += `<g class="mark" data-op="${esc(o.id)}" data-lane="${esc(l.id)}" data-ta="${o.start}" data-tb="${o.end}" data-phase="${o.phase}"><rect x="${xa.toFixed(1)}" y="${y + rh - 5}" width="${ww.toFixed(1)}" height="3" rx="1.5" fill="#c9a15c" opacity=".9"/><rect x="${xa.toFixed(1)}" y="${y + rh - 8}" width="${ww.toFixed(1)}" height="8" fill="transparent"/></g>`; }
      // failures: red = tool (non-zero exit / failed status), orange = LLM (invalid tool call / broken exec script)
      { let lastX = -99; const fy = y + rh - 4;
        for (const o of l.ops) { if (o.status !== 'failed' || o.background || o.start < state.a || o.start > state.b) continue; const xx = x(o.start); const isLlm = o.kind === 'exec-script-error' || baseKind(o.kind) === 'llm-invalid-args'; const c = isLlm ? '#f0a742' : '#e05252'; if (xx - lastX < 4) { html += `<rect class="mark" data-op="${esc(o.id)}" data-lane="${esc(l.id)}" data-ta="${o.start}" data-tb="${o.end}" data-phase="${o.phase}" x="${(xx - 1).toFixed(1)}" y="${fy - 7}" width="2" height="7" fill="${c}"/>`; continue; } lastX = xx; html += `<g class="mark" data-op="${esc(o.id)}" data-lane="${esc(l.id)}" data-ta="${o.start}" data-tb="${o.end}" data-phase="${o.phase}"><path d="M${xx.toFixed(1)} ${fy - 8}l4.5 8h-9z" fill="${c}" stroke="#0f1b23" stroke-width=".8"/><rect x="${xx - 5}" y="${fy - 10}" width="10" height="11" fill="transparent"/></g>`; }
      }
      if (l.live) { const xx = x(Math.min(m.now, state.b)); if (m.now >= state.a) html += `<g class="marker" data-turn="${esc(l.id + ':' + (l.turns[l.turns.length - 1] || {}).id)}"><circle cx="${xx}" cy="${y + 17 + (rh - 22) / 2}" r="4" fill="#5fe0a0"><animate attributeName="r" values="3.5;6;3.5" dur="1.6s" repeatCount="indefinite"/></circle></g>`; }
      html += `</g>`;
    } else if (row.kind === 'markers') {
      html += `<text class="lane-sub" x="${mobile ? 14 : 20}" y="${y + 15}">markers</text><g clip-path="url(#plotClip)">`;
      const mks = row.lane.markers.filter(k => MARKS[k.kind] && !k.kind.startsWith('agent_') && k.t >= state.a && k.t <= state.b).sort((p, q) => p.t - q.t);
      const clusters = []; for (const mk of mks) { const xx = x(mk.t); const c = clusters[clusters.length - 1]; if (c && xx - c.x1 < 16 && xx - c.x0 < 48) { c.items.push(mk); c.x1 = xx; } else clusters.push({ x0: xx, x1: xx, items: [mk] }); }
      const glyph = (mk, xx) => { const c = MARKS[mk.kind].color; switch (MARKS[mk.kind].glyph) { case 'user': return `<path d="M${xx} ${y + 4}l6 13h-12z" fill="${c}"/>`; case 'q': return `<circle cx="${xx}" cy="${y + 11}" r="6" fill="none" stroke="${c}" stroke-width="2"/><text x="${xx}" y="${y + 14.5}" text-anchor="middle" font-size="9" font-weight="700" fill="${c}">?</text>`; case 'check': return `<circle cx="${xx}" cy="${y + 11}" r="5.5" fill="${c}"/><path d="M${xx - 2.8} ${y + 11}l2 2 3.6-4" stroke="#0f1b23" stroke-width="1.6" fill="none"/>`; case 'x': return `<path d="M${xx - 4} ${y + 7}l8 8m0-8l-8 8" stroke="${c}" stroke-width="2"/>`; case 'diamond': return `<path d="M${xx} ${y + 6}l5 5-5 5-5-5z" fill="${c}" opacity=".9"/>`; case 'plan': return `<rect x="${xx - 4}" y="${y + 7}" width="8" height="8" rx="1.5" fill="${c}"/>`; case 'sys': return `<path d="M${xx} ${y + 5}l5 6-5 6-5-6z" fill="none" stroke="${c}" stroke-width="1.5"/>`; case 'result': return `<path d="M${xx + 4} ${y + 5}l-8 6 8 6z" fill="${c}"/>`; default: return `<rect x="${xx - 1}" y="${y + 6}" width="2" height="10" fill="${c}"/>`; } };
      for (const cl of clusters) {
        if (cl.items.length === 1) { const mk = cl.items[0]; html += `<g class="marker" data-mk="${esc(`${mk.lane}:${mk.t}:${mk.kind}`)}">${glyph(mk, cl.x0)}<rect x="${cl.x0 - 7}" y="${y}" width="14" height="${rh}" fill="transparent"/></g>`; continue; }
        // cluster pill: colour of the most important kind (user > question > final > other)
        const pri = ['user_message', 'question', 'final_answer', 'interrupted']; const lead = cl.items.slice().sort((p, q) => (pri.indexOf(p.kind) + 1 || 9) - (pri.indexOf(q.kind) + 1 || 9))[0]; const users = cl.items.filter(k => k.kind === 'user_message').length; const xm = (cl.x0 + cl.x1) / 2, wpill = Math.max(22, cl.x1 - cl.x0 + 14);
        html += `<g class="mark marker-cluster" data-stage="1" data-lane="${esc(row.lane.id)}" data-ta="${cl.items[0].t}" data-tb="${cl.items[cl.items.length - 1].t + 1}" data-phase="cluster"><rect x="${xm - wpill / 2}" y="${y + 4}" width="${wpill}" height="14" rx="7" fill="${MARKS[lead.kind].color}" opacity=".85"/><text x="${xm}" y="${y + 14.5}" text-anchor="middle" font-size="9.5" font-weight="700" fill="#0f1b23">${users ? '▲' + users + (cl.items.length > users ? '+' + (cl.items.length - users) : '') : cl.items.length}</text></g>`;
      }
      html += `</g>`;
    } else if (row.kind === 'groups') {
      html += `<text class="lane-sub" x="${mobile ? 14 : 20}" y="${y + 17}">↻ retry groups</text><g clip-path="url(#plotClip)">`;
      const vis = m.groups.filter(g => g.end > state.a && g.start < state.b);
      // stack overlapping groups into up to 2 sub-rows
      const lanesEnd = [0, 0], labelEnd = [0, 0];
      for (const g of vis) { const xa = x(Math.max(state.a, g.start)), xb = x(Math.min(state.b, g.end)), ww = Math.max(2, xb - xa); let sub = lanesEnd[0] <= xa ? 0 : lanesEnd[1] <= xa ? 1 : 0; lanesEnd[sub] = xa + ww + 2; const yy = y + 5 + sub * 9; const strong = g.failed > 0; const next = vis.find(o => o !== g && x(Math.max(state.a, o.start)) > xa + 2); const room = Math.min(W - R, next ? x(Math.max(state.a, next.start)) : W - R) - xa; const label = `${g.id} · ${g.attempts}×${g.failed ? ' · ' + g.failed + ' failed' : ''}`; const showLabel = room > label.length * 6.2 + 8 && xa >= labelEnd[sub]; if (showLabel) labelEnd[sub] = xa + label.length * 6.2 + 12; html += `<g class="mark" data-group="${esc(g.id)}"><path d="M${xa} ${yy + 7}v-5h${ww}v5" fill="none" stroke="${strong ? '#b22998' : '#8a5f8a'}" stroke-width="1.5" stroke-dasharray="${ww < 12 ? '' : '3 3'}"/><rect x="${xa}" y="${yy - 2}" width="${Math.max(ww, 8)}" height="10" fill="transparent"/>${showLabel ? `<text class="group-label" x="${xa + 4}" y="${y + 21}">${esc(label)}</text>` : ''}</g>`; }
      html += `</g>`;
    } else if (row.kind === 'phase') {
      const l = row.lane, p = row.phase, sel = state.phase === p; const indent = (mobile ? 8 : 14) + Math.min(l.depth, 3) * (mobile ? 6 : 10);
      html += `<g data-phase-row="${p}" data-lane="${esc(l.id)}"><rect x="0" y="${y}" width="${L - 4}" height="${rh}" fill="transparent"/><rect x="${indent}" y="${y + rh / 2 - 4}" width="8" height="8" rx="2.5" fill="${PHASES[p].color}"/><text class="lane-label ${sel ? 'selected' : ''}" x="${indent + 14}" y="${y + rh / 2 + 4}" style="font-size:11.5px">${esc(mobile ? PHASES[p].short : PHASES[p].name)}</text></g><g clip-path="url(#plotClip)">`;
      const segs = l.segments.filter(sg => sg.p === p && sg.e > state.a && sg.s < state.b);
      if (span / P < 800) { // fine zoom: draw each segment (≈ op) individually
        for (const sg of segs) { const xa = x(Math.max(sg.s, state.a)), ww = Math.max(1.2, x(Math.min(sg.e, state.b)) - xa); const o = sg.op ? m.opById.get(sg.op) : null; html += `<g class="mark" data-op="${esc(sg.op || '')}" data-lane="${esc(l.id)}" data-ta="${sg.s}" data-tb="${sg.e}" data-phase="${p}"><rect x="${xa.toFixed(1)}" y="${y + 4}" width="${ww.toFixed(1)}" height="${rh - 8}" rx="${ww > 4 ? 2 : 0}" fill="${fillFor(p)}" ${o && o.status === 'failed' ? 'stroke="#ff8a8a" stroke-width="1.2"' : ''} ${sg.op && sg.op === state.selected ? 'stroke="#eef6fa" stroke-width="2"' : ''}/>${ww > 40 && o ? `<text class="op-label ${p === 'llm' || p === 'code' || p === 'infra' || p === 'compaction' ? 'light' : ''}" x="${xa + 4}" y="${y + rh / 2 + 3.5}">${esc(o.title.slice(0, Math.floor((ww - 8) / 5.6)))}</text>` : ''}</g>`; }
      } else {
        const { runs, per } = bucketRuns(segs, state.a, state.b, P, bw, () => p);
        for (const run of runs) { const xa = L + run.start * bw, ww = Math.max(1, (run.end - run.start) * bw - .6); html += `<g class="mark" data-stage="1" data-lane="${esc(l.id)}" data-ta="${Math.round(state.a + run.start * per)}" data-tb="${Math.round(state.a + run.end * per)}" data-phase="${p}"><rect x="${xa.toFixed(1)}" y="${y + 4}" width="${ww.toFixed(1)}" height="${rh - 8}" rx="1.5" fill="${fillFor(p)}" opacity="${(.4 + .6 * run.cov).toFixed(2)}"/></g>`; }
      }
      html += `</g>`;
    }
    y += rh;
  });
  // connectors: spawn time from root row to the child row
  if (!mobile) { let yr = top; const rowY = new Map(); for (const row of rows) { if (row.kind === 'lane') rowY.set(row.lane.id, { y: yr, h: row.h }); yr += row.h; } for (const l of m.lanes.slice(1)) { const mk = (m.agentMarks.get(l.id) || []).find(k => k.kind === 'agent_started'); const c = rowY.get(l.id), pr = rowY.get(l.parent); if (!mk || !c || !pr || mk.t < state.a || mk.t > state.b) continue; const xx = x(mk.t); html += `<line x1="${xx}" y1="${pr.y + pr.h - 6}" x2="${xx}" y2="${c.y + 6}" stroke="#86d0b9" stroke-width="1" stroke-dasharray="2 3" opacity=".7"/>`; } }
  if (m.live && m.now >= state.a && m.now <= state.b) { const xx = x(m.now); html += `<line x1="${xx}" x2="${xx}" y1="26" y2="${H - 4}" stroke="#5fe0a0" stroke-width="1.2" opacity=".8"/>`; }
  else if (state.b >= m.ended - 1 && !m.live) { const xx = x(m.ended); html += `<line x1="${xx - 1}" x2="${xx - 1}" y1="26" y2="${H - 4}" stroke="#a5c5df" stroke-width="1.2"/>`; }
  const svg = $('#chartSvg'); svg.setAttribute('viewBox', `0 0 ${W} ${H}`); svg.style.height = H + 'px'; svg.innerHTML = html;
  $('#rangeLabel').textContent = spanLabel(state.a, state.b) + ' ' + TZ; $('#zoomCaption').textContent = fmt(span);
  $('#chartNote').innerHTML = icon('info', true) + (span / P >= 800 ? `${fmt(bucketRuns([], state.a, state.b, P, bw, () => 0).per)} per column: fill = dominant phase (teal is LLM time), brackets above = stages; in expanded rows bar height = coverage. Select a block to zoom.` : `Fine zoom: expanded rows show individual operations. Select one to inspect the source event.`);
  $$('[data-action="preset"]').forEach(el => { const v = Number(el.dataset.span) || (m.ended - m.started); el.classList.toggle('active', Math.abs(span - v) < 1000); el.disabled = Number(el.dataset.span) > m.ended - m.started; });
  $('[data-action="zoom"][data-dir="-1"]').disabled = span >= m.ended - m.started - 500; $('[data-action="zoom"][data-dir="1"]').disabled = span <= 60500;
  $('[data-action="pan"][data-dir="-1"]').disabled = state.a <= m.started + 1; $('[data-action="pan"][data-dir="1"]').disabled = state.b >= m.ended - 1;
  $('#followBtn').classList.toggle('active', state.follow); $('#followBtn').setAttribute('aria-pressed', state.follow);
  updateBrush();
}
function setWindow(a, b, { manual = true, lower = true } = {}) { const m = current(); const span = clamp(b - a, 60000, m.ended - m.started); a = clamp(a, m.started, m.ended - span); state.a = a; state.b = a + span; state.listPage = 0; if (manual && state.b < m.ended - 1000) state.follow = false; hideTooltip(); renderTimeline(); if (lower) renderLower(); }
function chooseSpan(span, center = (state.a + state.b) / 2) { const m = current(); span = Math.min(span, m.ended - m.started); setWindow(center - span / 2, center + span / 2); }
function zoom(dir, anchor = .5) { const m = current(), cur = state.b - state.a, total = m.ended - m.started, levels = [60e3, 300e3, 900e3, 3600e3, 10800e3, 21600e3, 43200e3, 86400e3, 172800e3, total].filter(n => n <= total).sort((a, b) => a - b), next = dir > 0 ? [...levels].reverse().find(n => n < cur - 1) : levels.find(n => n > cur + 1); if (!next) return; const t = state.a + cur * anchor; setWindow(t - next * anchor, t + next * (1 - anchor)); }
function pan(dir) { const span = state.b - state.a; setWindow(state.a + dir * span * .6, state.b + dir * span * .6); }
function focusInterval(a, b) { if ($('#inspector').open) $('#inspector').close(); const span = Math.max(120e3, (b - a) * 1.6); chooseSpan(span, (a + b) / 2); $('#plot').scrollIntoView({ block: 'center', behavior: 'instant' }); }

/* ---------- lower panels ---------- */
function renderLower() {
  if (state.page !== 'session' || !$('#breakdownBody')) return;
  const t = windowStats(state.a, state.b);
  const rows = (entries, total) => entries.map(([k, n, def, sel, data]) => { const stg = PHASES[k]?.kind === 'work' ? t.stageBy[k] || 0 : 0; return `<button class="breakdown-item ${sel ? 'active' : ''}" ${data} title="${esc(def.name)}: ${fmt(n, true)} of tool time${stg ? ` · stage ${fmt(stg, true)} including LLM time before each call` : ''}"><span class="name"><i class="color-square" style="background:${def.color}"></i>${def.name}</span><span class="bar-track"><span class="bar-fill" style="width:${total ? clamp(n / total * 100, 0, 100) : 0}%;background:${def.color}"></span>${stg ? `<span class="bar-stage" style="width:${total ? clamp(stg / total * 100, 0, 100) : 0}%;border-color:${def.color}"></span>` : ''}</span><span class="time num">${fmt(n)}${stg ? `<small>stage ${fmt(stg)}</small>` : ''}</span></button>`; }).join('');
  if ($('#breakdownScopeChip')) $('#breakdownScopeChip').textContent = state.allLanes ? 'All lanes · agent time' : 'Root lane · exclusive';
  $('#breakdownScope').textContent = `${fmt(t.duration)} selected · ${fmt(t.inTurn)} inside turns · sub-agents ${fmt(t.agentMs)} in parallel · "stage" = tool calls + LLM time before them`;
  const testTotal = Math.max(1, ROLE_ORDER.filter(k => k !== 'remote').reduce((n, k) => n + t.roles[k], 0));
  const src = state.allLanes ? t.allBy : t.by;
  const denom = state.allLanes ? Math.max(1, PHASE_ORDER.reduce((n, k) => n + (k === 'idle' ? 0 : src[k]), 0)) : state.inTurn ? Math.max(1, t.inTurn) : t.duration;
  const byLongest = (a, b) => b[1] - a[1];
  const order = arr => state.breakdownSort === 'longest' ? arr.slice().sort(byLongest) : arr;
  const phaseRows = PHASE_ORDER.filter(k => k !== 'idle' && !(state.inTurn && k === 'wait_user'));
  $('#breakdownBody').innerHTML = `<div class="mini-heading" style="padding-top:0;flex-wrap:wrap;gap:8px"><label class="row" style="font-size:12.5px"><input type="checkbox" id="inTurnToggle" ${state.inTurn ? 'checked' : ''}>Inside turns only</label><label class="row" style="font-size:12.5px"><input type="checkbox" id="allLanesToggle" ${state.allLanes ? 'checked' : ''}>Include sub-agents</label><span class="row" style="font-size:12px;color:var(--subtle)">sort <select class="select" id="breakdownSort" style="min-height:26px;padding:2px 6px;font-size:12px"><option value="longest" ${state.breakdownSort === 'longest' ? 'selected' : ''}>longest first</option><option value="stage" ${state.breakdownSort === 'stage' ? 'selected' : ''}>by stage</option></select></span><span class="mono" style="color:var(--subtle)">${fmt(t.raw)} op time in ${fmt(t.inTurn)}</span></div>` + rows(order(phaseRows.map(k => [k, src[k], PHASES[k], state.phase === k, `data-action="filter" data-phase="${k}"`])), denom) + `<div class="mini-heading"><h3>Inside testing & release</h3><span class="mono" style="color:var(--subtle)">op time, root lane</span></div>` + rows(order(ROLE_ORDER.map(k => [k, t.roles[k], ROLES[k], state.role === k, `data-action="filter-role" data-role="${k}"`])), testTotal) + (t.bg ? `<div class="mini-heading"><h3>Background processes</h3><span class="mono" style="color:var(--subtle)">${t.bgOps} · not in totals</span></div>${rows([['bg', t.bg, { name: 'Outlived their turn (servers, watchers)', color: '#8a6d3b' }, state.role === 'background', 'data-action="filter-role" data-role="background"']], Math.max(t.bg, t.duration))}` : '') + (t.agentMs ? `<div class="mini-heading"><h3>Sub-agents in window</h3><span class="mono" style="color:var(--subtle)">${t.agentLanes} lanes</span></div>${rows([['agents', t.agentMs, { name: 'Agent time (sum)', color: '#86d0b9' }, false, 'data-action="noop"'], ['wall', t.agentWall, { name: 'Wall clock (union)', color: '#5f8f86' }, false, 'data-action="noop"']], Math.max(t.agentMs, 1))}` : '');
  renderOperations(); renderConversation(); renderAgents();
}
function filteredOps() {
  const a = state.a, b = state.b; let ops = opsInWindow(a, b).filter(o => (!state.failedOnly || o.status === 'failed') && (state.phase === 'all' || o.phase === state.phase) && (state.role === 'all' || roleOf(o.kind) === state.role || (state.role === 'single' && o.phase === 'test' && !roleOf(o.kind)) || (state.role === 'remote' && o.remote) || (state.role === 'background' && o.background)));
  const dur = o => overlap(o.start, o.end, a, b);
  ops.sort((p, q) => state.sort === 'latest' ? q.start - p.start : state.sort === 'earliest' ? p.start - q.start : state.sort === 'failed' ? ((q.status === 'failed') - (p.status === 'failed')) || dur(q) - dur(p) : dur(q) - dur(p) || p.start - q.start);
  return ops;
}
function renderOperations() {
  const m = current(), ops = filteredOps(), pageSize = 8, pageCount = Math.max(1, Math.ceil(ops.length / pageSize)); state.listPage = clamp(state.listPage, 0, pageCount - 1); const start = state.listPage * pageSize, items = ops.slice(start, start + pageSize);
  $('#phaseSelect').value = state.phase; $('#sortSelect').value = state.sort; $('#laneSelect').value = state.lane;
  $('#operationCount').textContent = `${ops.length} operations${state.failedOnly ? ' · failures only' : ''} · durations clipped to the window`;
  $('#roleFilter').innerHTML = state.role === 'all' ? '' : `<div style="padding:0 22px 10px"><button class="chip" data-action="clear-role">${ROLES[state.role].name} ${icon('close', true)}</button></div>`;
  $('#operationList').innerHTML = items.length ? items.map((o, i) => { const l = m.laneById.get(o.lane); const role = roleOf(o.kind); return `<button class="operation" data-action="inspect" data-id="${esc(o.id)}"><span class="op-number">${pad(start + i + 1)}</span><i class="color-square" style="background:${PHASES[o.phase].color}"></i><span class="operation-copy"><strong>${esc(o.title)}</strong><small>${stampS(o.start)} · ${esc(baseKind(o.kind))}${o.phase === 'llm' ? ' · ' + esc(modelOf(o)) + (effortOf(o) ? ' / ' + esc(effortOf(o)) : '') : ''}${role ? ' · ' + esc(ROLES[role]?.name || role) : ''}${o.group ? ' · ' + esc(o.group) + (o.attempt ? ' #' + o.attempt : '') : ''} · ${esc(o.status)}${o.exit != null && o.exit !== 0 ? ' exit ' + o.exit : ''}${o.status === 'failed' && (o.kind === 'exec-script-error' || baseKind(o.kind) === 'llm-invalid-args') ? ' · LLM failure' : ''}${o.remote ? ' · remote' : ''}${o.queued ? ' · after poll loop' : ''}${o.background ? ' · background (outlived its turn)' : ''}${l && l.depth > 0 ? ' · ' + esc(l.path.split('/').pop()) : ''}</small></span><span class="op-time">${fmt(overlap(o.start, o.end, state.a, state.b), true)}</span>${icon('right', true)}</button>`; }).join('') : `<div class="empty"><h3>No matching operations in this window.</h3><p>Change the filters or move the window.</p><button class="text-btn" data-action="clear-filters">Show everything</button></div>`;
  $('#listFooter').innerHTML = `<span>${items.length ? `${start + 1}–${start + items.length} of ${ops.length}` : '0 operations'}</span><div class="row"><button class="btn ghost small" data-action="list-page" data-dir="-1" aria-label="Previous" ${state.listPage === 0 ? 'disabled' : ''}>${icon('left', true)}</button><span class="mono">${state.listPage + 1} / ${pageCount}</span><button class="btn ghost small" data-action="list-page" data-dir="1" aria-label="Next" ${state.listPage >= pageCount - 1 ? 'disabled' : ''}>${icon('right', true)}</button></div>`;
}
function renderConversation() {
  const r = root(); const items = r.markers.filter(k => ['user_message', 'question', 'final_answer', 'system_message'].includes(k.kind));
  const cls = k => k.kind === 'user_message' ? 'user' : k.kind === 'question' ? 'question' : k.kind === 'system_message' ? 'system' : 'final';
  const label = k => k.kind === 'user_message' ? 'User' : k.kind === 'question' ? 'Agent asks' : k.kind === 'system_message' ? 'Harness' : 'Final answer';
  $('#conversation').innerHTML = items.length ? items.map((k, i) => { const key = `${k.t}:${k.kind}`, open = state.convOpen.has(key), inWin = k.t >= state.a && k.t <= state.b; return `<div class="conv-item ${cls(k)}" style="${inWin ? '' : 'opacity:.55'}"><button class="conv-time" data-action="jump" data-t="${k.t}" title="Jump to this moment"><b>${label(k)}</b>${stamp(k.t)}</button><div><div class="conv-text ${open ? 'open' : ''}">${esc(k.text)}</div>${k.text.length > 380 ? `<button class="text-btn" data-action="conv-toggle" data-key="${esc(key)}" style="margin-top:4px">${open ? 'Show less' : 'Show all'}</button>` : ''}</div></div>`; }).join('') : `<div class="empty"><p>No user messages recorded.</p></div>`;
}
function renderAgents() {
  const m = current();
  const row = l => { const active = sum((l.active || []).map(iv => iv.e - iv.s)); const by = l.by_phase || {}; const work = ['code', 'build', 'test', 'release', 'infra'].reduce((n, k) => n + (by[k] || 0), 0); const total = Math.max(1, l.ended - l.started); const marks = m.agentMarks.get(l.id) || []; const started = marks.find(k => k.kind === 'agent_started'); const st = [[PHASES.code.color, work], [PHASES.llm.color, by.llm || 0], [PHASES.wait_worker.color, by.wait_worker || 0], [PHASES.wait_user.color, (by.wait_user || 0) + (by.idle || 0)]]; return `<tr class="lane-row" data-action="lane-focus" data-lane="${esc(l.id)}"><td><div style="padding-left:${Math.min(l.depth, 3) * 14}px"><strong>${esc(l.depth === 0 ? 'root' : l.path.split('/').pop())}</strong><div style="font-size:11.5px;color:var(--subtle)">${esc(l.role || 'root')}${l.nickname ? ' · ' + esc(l.nickname) : ''}${l.model ? ' · ' + esc(l.model) : ''}${(l.turns.find(t => t.effort) || {}).effort ? ' / ' + esc(l.turns.find(t => t.effort).effort) : ''}</div><div class="stack" aria-hidden="true">${st.map(([c, n]) => `<span style="width:${n / total * 100}%;background:${c}"></span>`).join('')}</div></div></td><td class="mono">${stamp(started ? started.t : l.started, false)}<br><span style="color:var(--subtle)">${stamp(l.ended, false)}</span></td><td class="mono">${l.depth === 0 ? fmt(l.ended - l.started) : fmt(active)}</td><td class="mono">${l.turns.length}</td><td class="mono">${l.ops.length}</td><td class="mono">${fmt(work)}</td><td class="mono">${l.tokens ? fmtTok(l.tokens.total) : '—'}</td><td class="mono">${l.live ? '<span class="chip live" style="padding:2px 6px">live</span>' : ''}</td></tr>`; };
  $('#agentsTable').innerHTML = `<thead><tr><th>Lane</th><th>Start / end</th><th>Active</th><th>Turns</th><th>Ops</th><th>Tool work</th><th>Tokens</th><th></th></tr></thead><tbody>${m.lanes.map(row).join('')}</tbody>`;
}
function filterPhase(phase) { state.phase = phase; state.listPage = 0; renderTimeline(); renderLower(); }

/* ---------- inspector ---------- */
async function inspect(id) {
  const m = current(), o = m.opById.get(id); if (!o) return; state.selected = id; hideTooltip();
  const request = ++inspectorRequest;
  const l = m.laneById.get(o.lane), g = o.group ? m.groupById.get(o.group) : null, role = roleOf(o.kind);
  const dlg = $('#inspector');
  const facts = [['Duration', fmt(o.end - o.start, true) + (o.open ? ' · still open' : '')], ['Started', stampS(o.start)], [o.open ? 'Observed until' : 'Ended', stampS(o.end)], ['Lane', l ? l.path : o.lane], ['Phase · kind', `${PHASES[o.phase].name} · ${baseKind(o.kind)}`], ['Model', modelOf(o) ? `${modelOf(o)}${effortOf(o) ? ' · effort ' + effortOf(o) : ''}` : '—'], ['Status', `${o.status}${o.exit != null ? ' · exit ' + o.exit : ''}`], ['Rule', o.rule || '—'], ['Retry group', g ? `${g.id} · attempt ${o.attempt || '—'} of ${g.attempts}` : 'none (no repeated identity)'], ['Flags', [o.background ? 'background process: outlived its turn, excluded from totals' : '', o.remote ? 'remote runner' : '', o.queued ? 'poll loop before work' : '', role ? ROLES[role]?.name || role : '', o.parallel ? `${o.parallel} parallel commands` : ''].filter(Boolean).join(', ') || '—']];
  dlg.innerHTML = `<div class="dialog-head"><span class="eyebrow">Operation</span><button class="btn icon-only ghost" data-action="close-inspector" aria-label="Close" autofocus>${icon('close')}</button></div><div class="dialog-body"><span class="chip ${o.status === 'failed' ? 'failed' : o.status === 'completed' ? 'passed' : o.status === 'running' ? 'running' : ''}"><i class="color-square" style="background:${PHASES[o.phase].color}"></i>${PHASES[o.phase].name} · ${esc(o.status)}</span><h2 id="inspectorTitle">${esc(o.title)}</h2><p class="desc">Timing and labels come from the source event and the rule table. They do not say why the step was slow.</p><dl class="facts">${facts.map(([k, v]) => `<div><dt>${k}</dt><dd class="mono">${esc(v)}</dd></div>`).join('')}</dl><div class="dialog-actions"><button class="btn primary" data-action="focus-op" data-id="${esc(o.id)}">${icon('expand', true)}Focus this interval</button>${g ? `<button class="btn" data-action="focus-group" data-group="${esc(g.id)}">${icon('loop', true)}See full group</button>` : ''}</div><h3>Command / detail</h3><pre class="event-log" id="opDetail">loading…</pre>${g ? `<h3>${esc(g.id)} · ${g.attempts} attempts · ${g.failed} failed</h3><p class="dialog-note desc">Grouped by identical normalized command (redirections stripped). Not inferred from similar text.</p><div class="group-sequence">${g.members.map(id => m.opById.get(id)).filter(Boolean).sort((a, b) => a.start - b.start).map(it => `<button class="group-op ${it.id === id ? 'active' : ''}" data-action="inspect" data-id="${esc(it.id)}"><i class="color-square" style="background:${ROLES[roleOf(it.kind)]?.color || PHASES[it.phase].color}"></i>${it.attempt ? '#' + it.attempt : esc(ROLES[roleOf(it.kind)]?.name || it.phase)}<span class="subtle">${it.status === 'failed' ? 'failed' : it.status === 'completed' ? 'ok' : esc(it.status)}</span><span class="mono">${fmt(it.end - it.start)}</span></button>`).join('')}</div>` : ''}<details class="json-details"><summary>Source event (raw)</summary><pre class="event-log" id="opSource">loading…</pre></details><details class="json-details"><summary>Normalized operation</summary><pre class="event-log">${esc(JSON.stringify(o, null, 2))}</pre></details></div>`;
  if (!dlg.open) dlg.showModal();
  renderTimeline();
  const detail = $('#opDetail'), source = $('#opSource');
  const ownsRequest = () => request === inspectorRequest && dlg.open && state.page === 'session' && current()?.id === m.id;
  try {
    const d = await api(`/api/sessions/${encodeURIComponent(m.id)}/op/${encodeURIComponent(id)}`);
    if (!ownsRequest()) return;
    detail.textContent = d.detail || '(no detail)'; source.textContent = JSON.stringify(d.source, null, 1)?.slice(0, 60000) || '(unavailable)';
  } catch (e) { if (ownsRequest()) detail.textContent = 'unavailable: ' + e.message; }
}
async function inspectMarker(key) {
  const m = current(); const [lane, t, kind] = key.split(':'); const l = m.laneById.get(lane); const mk = l?.markers.find(k => String(k.t) === t && k.kind === kind); if (!mk) return;
  const request = ++inspectorRequest;
  const dlg = $('#inspector'); const def = MARKS[kind] || { name: kind, color: '#aaa' };
  dlg.innerHTML = `<div class="dialog-head"><span class="eyebrow">Marker</span><button class="btn icon-only ghost" data-action="close-inspector" aria-label="Close" autofocus>${icon('close')}</button></div><div class="dialog-body"><span class="chip"><i class="color-square" style="background:${def.color}"></i>${esc(def.name)}</span><h2 id="inspectorTitle">${stampS(mk.t)} · ${esc(l.path)}</h2>${mk.ref && m.laneById.get(mk.ref) ? `<p class="desc">Sub-agent: ${esc(m.laneById.get(mk.ref).path)}</p>` : ''}<pre class="event-log" style="margin-top:12px">${esc(mk.text || '(no text)')}</pre><div class="dialog-actions"><button class="btn primary" data-action="jump" data-t="${mk.t}">${icon('expand', true)}Zoom around this moment</button></div><details class="json-details"><summary>Source event (raw)</summary><pre class="event-log" id="mkSource">loading…</pre></details></div>`;
  if (!dlg.open) dlg.showModal();
  const source = $('#mkSource');
  const ownsRequest = () => request === inspectorRequest && dlg.open && state.page === 'session' && current()?.id === m.id;
  if (mk.src) {
    try {
      const d = await api(`/api/event?file=${encodeURIComponent(mk.src.file)}&off=${mk.src.off}&len=${mk.src.len}`);
      if (ownsRequest()) source.textContent = JSON.stringify(d, null, 1).slice(0, 60000);
    } catch (e) { if (ownsRequest()) source.textContent = 'unavailable'; }
  } else source.textContent = '(no source pointer)';
}
async function guide() {
  const dlg = $('#guide');
  dlg.innerHTML = `<div class="dialog-head"><h2 id="guideTitle">Reading the timeline</h2><button class="btn icon-only ghost" data-action="close-guide" aria-label="Close" autofocus>${icon('close')}</button></div><div class="dialog-body"><section class="guide-section"><h3>One row per agent</h3><p>The root thread is the first lane; every sub-agent thread is its own lane, indented by depth, with a ▶ at spawn, ticks at each message from the parent, and a bar at each completed turn. A dashed line connects spawn time to the parent. Expand a lane (click its name) to see phase rows and, at fine zoom, individual operations.</p></section><section class="guide-section"><h3>Stages, not tool calls</h3><p>A lane's fill is the real exclusive time split: teal is LLM time (the model generating — reasoning, answers and the code of every patch), colours are tool phases, pink is waiting for the user, hatched is missing telemetry. Coding covers everything about writing code: reading and searching sources, edits, local git, formatting. Above the fill, thin <em>stage brackets</em> name runs of tool calls of one phase (Code, Test, Release …) — a grouping that includes the LLM time before each call, so a bracket can be long while its coloured fill is thin. Turn ends carry ✓ (completed), ✕ (interrupted) or ⊘ (never closed); a pulsing dot means the turn is still open. Dashed pink verticals are turn starts.</p></section><section class="guide-section"><h3>What is inferred and what is not</h3><p>Phases come from a rule table over the command text (below). Turn boundaries give waiting-for-user time; a turn that never closed becomes "No telemetry". Retry groups only join operations with an identical normalized command. Nothing is derived from how long something took, and no "goal reached" score exists — read the conversation panel.</p></section><section class="guide-section"><h3>Parallel time</h3><p>Session totals are the root lane's exclusive wall clock. Sub-agent time is summed separately and its union shown as "wall".</p></section><section class="guide-section"><h3>Keyboard</h3><p><kbd>+</kbd>/<kbd>−</kbd> zoom, <kbd>←</kbd>/<kbd>→</kbd> pan, <kbd>Home</kbd> fit, <kbd>Esc</kbd> close. Drag the plot to pan; drag on the overview to select.</p></section><section class="guide-section"><h3>Classification rules</h3><p id="rulesNote">loading…</p><div style="overflow:auto;max-height:340px"><table class="rules-table" id="rulesTable"></table></div></section></div>`;
  dlg.showModal();
  try { const d = await api('/api/rules'); $('#rulesNote').textContent = `${d.rules.length} rules. A command takes the highest-priority phase among its segments (release > test > build > workers > infra > develop > explore). Unmatched commands stay Unknown.`; $('#rulesTable').innerHTML = `<thead><tr><th>Match</th><th>Phase</th><th>Kind</th><th>Note</th></tr></thead><tbody>${d.rules.map(r => `<tr><td><code>${esc(r.match)}</code></td><td><span class="row"><i class="color-square" style="background:${PHASES[r.phase]?.color}"></i>${esc(r.phase)}</span></td><td>${esc(r.kind)}</td><td style="color:var(--subtle)">${esc(r.note || '')}</td></tr>`).join('')}</tbody>`; } catch (e) { $('#rulesNote').textContent = 'rules unavailable'; }
}

/* ---------- tooltip ---------- */
function hideTooltip() { $('#tooltip').style.display = 'none'; }
function tooltip(event) {
  if (event.pointerType === 'touch' || plotDrag || overviewDrag || $('#inspector').open || $('#guide').open) return;
  const target = event.target.closest('[data-turn],[data-stage],[data-op],[data-group],[data-mk]'); if (!target) { hideTooltip(); return; }
  const m = current(), tip = $('#tooltip'); let body = '';
  if (target.dataset.turn) { const [lane, tid] = target.dataset.turn.split(':'); const l = m.laneById.get(lane); const tn = l?.turns.find(t => t.id === tid); if (!tn) return; const n = l.ops.filter(o => o.turn === tid).length; const names = { completed: 'Turn completed', aborted: 'Turn interrupted', orphaned: 'Turn never closed (process ended?)', open: 'Turn in progress' }; body = `<strong>${names[tn.status] || tn.status} · ${stamp(tn.end, false)}</strong><span>${spanLabel(tn.start, tn.end)} · ${fmt(tn.end - tn.start, true)} · ${n} operations${tn.model ? ' · ' + esc(tn.model) + (tn.effort ? ' / ' + esc(tn.effort) : '') : ''}</span>${tn.final ? `<span>${esc(stripMd(tn.final).slice(0, 200))}</span>` : ''}`; }
  else if (target.dataset.mk) { const [lane, t, kind] = target.dataset.mk.split(':'); const l = m.laneById.get(lane); const mk = l?.markers.find(k => String(k.t) === t && k.kind === kind); if (!mk) return; body = `<strong>${esc(MARKS[kind]?.name || kind)}</strong><span>${stampS(mk.t)}</span><span>${esc((mk.text || '').slice(0, 220))}</span>`; }
  else if (target.dataset.group) { const g = m.groupById.get(target.dataset.group); body = `<strong>${esc(g.id)} · ${g.attempts} attempts · ${g.failed} failed</strong><span>${spanLabel(g.start, g.end)}</span><span>${esc(g.title.slice(0, 160))}</span><span>Same normalized command. Select to focus.</span>`; }
  else if (target.dataset.op) { const o = m.opById.get(target.dataset.op); if (!o) return; body = `<strong>${esc(o.title.slice(0, 160))}</strong><span>${spanLabel(o.start, o.end)} · ${fmt(o.end - o.start, true)}</span><span>${PHASES[o.phase].name} · ${esc(baseKind(o.kind))}${o.phase === 'llm' ? ' · ' + esc(modelOf(o)) : ''} · ${esc(o.status)}${o.group ? ' · ' + esc(o.group) : ''}${o.background ? ' · background process (outlived its turn; not in totals)' : ''}</span><span>Select to inspect the source event.</span>`; }
  else if (target.dataset.bracket) { const ta = Number(target.dataset.ta), tb = Number(target.dataset.tb), p = target.dataset.phase, l = m.laneById.get(target.dataset.lane); const ops = l ? l.ops.filter(o => o.end > ta && o.start < tb && o.phase === p).length : 0; body = `<strong>Stage: ${PHASES[p].name} · ${fmt(tb - ta)}</strong><span>${spanLabel(ta, tb)}</span><span>${ops} ${PHASES[p].short.toLowerCase()} operations plus the LLM time before each. The fill below shows the real split.</span>`; }
  else if (target.dataset.phase === 'cluster') { const ta = Number(target.dataset.ta), tb = Number(target.dataset.tb), l = m.laneById.get(target.dataset.lane); const items = l.markers.filter(k => k.t >= ta && k.t < tb && MARKS[k.kind] && !k.kind.startsWith('agent_')); body = `<strong>${items.length} markers · ${spanLabel(ta, tb)}</strong>` + items.slice(0, 8).map(k => `<span>${stamp(k.t, false)} ${esc(MARKS[k.kind].name)}: ${esc((k.text || '').slice(0, 70))}</span>`).join('') + (items.length > 8 ? `<span>…</span>` : '') + `<span>Select to zoom in.</span>`; }
  else { const ta = Number(target.dataset.ta), tb = Number(target.dataset.tb), p = target.dataset.phase, l = m.laneById.get(target.dataset.lane); const ops = l ? l.ops.filter(o => o.end > ta && o.start < tb && (target.dataset.phaseRow ? o.phase === p : true)).length : 0; body = `<strong>${PHASES[p].name} · ${fmt(tb - ta)}</strong><span>${spanLabel(ta, tb)}</span><span>${esc(l ? l.path : '')} · ${ops} operations inside</span><span>Select to zoom in.</span>`; }
  tip.innerHTML = body; tip.style.display = 'block'; const r = tip.getBoundingClientRect(); tip.style.left = clamp(event.clientX + 13, 8, window.innerWidth - r.width - 8) + 'px'; tip.style.top = clamp(event.clientY + 17, 8, window.innerHeight - r.height - 8) + 'px';
}

/* ---------- events ---------- */
document.addEventListener('click', e => {
  const el = e.target.closest('[data-action]'); if (!el || el.disabled) return; const a = el.dataset.action, m = current();
  if (a === 'sessions') return go('sessions'); if (a === 'session') return go('session', el.dataset.id || state.id);
  if (a === 'guide') return guide(); if (a === 'close-guide') return $('#guide').close(); if (a === 'close-inspector') return $('#inspector').close();
  if (a === 'refresh') {
    const navigation = navigationRequest;
    loadSession(state.id, { keepWindow: true }).then(applied => { if (applied) { render(); toast('Refreshed.'); } }).catch(e => {
      if (navigation === navigationRequest) toast('Could not refresh session: ' + e.message);
    });
    return;
  }
  if (a === 'fit') return setWindow(m.started, m.ended);
  if (a === 'preset') { const v = Number(el.dataset.span); if (!v) return setWindow(m.started, m.ended); if (state.follow || state.b >= m.ended - 1000) return setWindow(m.ended - v, m.ended, { manual: false }); return chooseSpan(v); }
  if (a === 'zoom') return zoom(Number(el.dataset.dir)); if (a === 'pan') return pan(Number(el.dataset.dir));
  if (a === 'follow') { state.follow = !state.follow; if (state.follow) { const span = Math.min(state.b - state.a, 6 * 3600e3); setWindow(m.ended - span, m.ended, { manual: false }); toast(m.live ? 'Following the live edge.' : 'At the latest event. Session is not live.'); } else renderTimeline(); return; }
  if (a === 'expand-all') { m.lanes.forEach(l => state.expanded.add(l.id)); renderTimeline(); return; }
  if (a === 'collapse-all') { state.expanded.clear(); renderTimeline(); return; }
  if (a === 'filter') { filterPhase(state.phase === el.dataset.phase ? 'all' : el.dataset.phase); return; }
  if (a === 'filter-role') { state.role = state.role === el.dataset.role ? 'all' : el.dataset.role; state.listPage = 0; renderLower(); return; }
  if (a === 'clear-role') { state.role = 'all'; renderLower(); return; }
  if (a === 'clear-filters') { state.phase = 'all'; state.role = 'all'; state.lane = 'all'; renderTimeline(); renderLower(); return; }
  if (a === 'list-page') { state.listPage += Number(el.dataset.dir); renderOperations(); return; }
  if (a === 'inspect') return inspect(el.dataset.id);
  if (a === 'focus-op') { const o = m.opById.get(el.dataset.id); if (o) focusInterval(o.start, o.end); return; }
  if (a === 'focus-group') { const g = m.groupById.get(el.dataset.group); if (g) focusInterval(g.start, g.end); return; }
  if (a === 'jump') { const t = Number(el.dataset.t); if ($('#inspector').open) $('#inspector').close(); chooseSpan(Math.min(state.b - state.a, 3600e3), t); $('#plot').scrollIntoView({ block: 'center', behavior: 'smooth' }); return; }
  if (a === 'conv-toggle') { const k = el.dataset.key; state.convOpen.has(k) ? state.convOpen.delete(k) : state.convOpen.add(k); renderConversation(); return; }
  if (a === 'lane-focus') { state.lane = state.lane === el.dataset.lane ? 'all' : el.dataset.lane; state.listPage = 0; renderTimeline(); renderOperations(); renderAgents(); return; }
});
document.addEventListener('change', e => { const id = e.target.id, v = e.target.value; if (id === 'showGroups') { state.groups = e.target.checked; renderTimeline(); } else if (id === 'inTurnToggle') { state.inTurn = e.target.checked; renderLower(); } else if (id === 'allLanesToggle') { state.allLanes = e.target.checked; renderLower(); } else if (id === 'failedOnly') { state.failedOnly = e.target.checked; state.listPage = 0; renderOperations(); } else if (id === 'breakdownSort') { state.breakdownSort = v; renderLower(); } else if (id === 'phaseSelect') filterPhase(v); else if (id === 'laneSelect') { state.lane = v; state.listPage = 0; renderTimeline(); renderOperations(); renderAgents(); } else if (id === 'sortSelect') { state.sort = v; state.listPage = 0; renderOperations(); } else if (id === 'fleetSort') { state.fleetSort = v; renderFleetRows(); } else if (id === 'intervalSelect') { state.interval = Number(v); schedulePoll(); toast(`Refreshing every ${v}s.`); } });
document.addEventListener('input', e => { if (e.target.id === 'sessionSearch') { state.search = e.target.value; renderFleetRows(); } });
// overview brush
document.addEventListener('pointerdown', e => { const ov = e.target.closest('#overview'); if (!ov || e.button !== 0) return; const m = current(), rect = ov.getBoundingClientRect(), total = m.ended - m.started, xx = clamp(e.clientX - rect.left, 0, rect.width), time = m.started + xx / rect.width * total, handle = e.target.closest('[data-handle]'); const mode = handle ? handle.dataset.handle : e.target.closest('#brush') && state.b - state.a < total - 1000 ? 'move' : 'draw'; overviewDrag = { id: e.pointerId, x: e.clientX, a: state.a, b: state.b, time, rect, mode, moved: false, total }; ov.setPointerCapture(e.pointerId); hideTooltip(); });
document.addEventListener('pointermove', e => {
  if (overviewDrag && e.pointerId === overviewDrag.id) { const d = overviewDrag, m = current(), delta = (e.clientX - d.x) / d.rect.width * d.total, t = clamp(m.started + (e.clientX - d.rect.left) / d.rect.width * d.total, m.started, m.ended); if (Math.abs(e.clientX - d.x) > 4) d.moved = true; if (!d.moved) return; if (d.mode === 'move') setWindow(d.a + delta, d.b + delta, { lower: false }); else if (d.mode === 'start') setWindow(Math.min(d.b - 60000, Math.max(m.started, d.a + delta)), d.b, { lower: false }); else if (d.mode === 'end') setWindow(d.a, Math.max(d.a + 60000, Math.min(m.ended, d.b + delta)), { lower: false }); else setWindow(Math.min(d.time, t), Math.max(d.time, t), { lower: false }); return; }
  if (plotDrag && e.pointerId === plotDrag.id) { const d = plotDrag, dx = e.clientX - d.x; if (Math.abs(dx) > 5 && !d.moved) { d.moved = true; $('#plot').setPointerCapture(e.pointerId); } if (d.moved) { const delta = -dx / d.P * (d.b - d.a); setWindow(d.a + delta, d.b + delta, { lower: false }); } return; }
  tooltip(e);
});
function finishPointer(e, cancelled = false) { if (overviewDrag && e.pointerId === overviewDrag.id) { const d = overviewDrag; overviewDrag = null; if (!cancelled && !d.moved && d.mode === 'draw') chooseSpan(Math.min(6 * 3600e3, state.b - state.a), d.time); else renderLower(); lastDragTime = Date.now(); return; } if (plotDrag && e.pointerId === plotDrag.id) { const d = plotDrag; plotDrag = null; if (d.moved) { renderLower(); lastDragTime = Date.now(); } } }
document.addEventListener('pointerup', e => finishPointer(e)); document.addEventListener('pointercancel', e => finishPointer(e, true));
document.addEventListener('pointerdown', e => { const plot = e.target.closest('#plot'); if (!plot || e.button !== 0 || e.target.closest('.lane-head,[data-phase-row]') || !geometry) return; plotDrag = { id: e.pointerId, x: e.clientX, a: state.a, b: state.b, P: geometry.P, moved: false }; });
document.addEventListener('click', e => {
  if (Date.now() - lastDragTime < 180) return; const plot = e.target.closest('#plot'); if (!plot) return;
  const head = e.target.closest('.lane-head'), prow = e.target.closest('[data-phase-row]'), op = e.target.closest('[data-op]'), stage = e.target.closest('[data-stage]'), group = e.target.closest('[data-group]'), mk = e.target.closest('[data-mk]');
  if (head) { const id = head.dataset.lane; state.expanded.has(id) ? state.expanded.delete(id) : state.expanded.add(id); renderTimeline(); }
  else if (prow) { filterPhase(state.phase === prow.dataset.phaseRow ? 'all' : prow.dataset.phaseRow); }
  else if (e.target.closest('[data-turn]')) { const [lane, tid] = e.target.closest('[data-turn]').dataset.turn.split(':'); const tn = current().laneById.get(lane)?.turns.find(t => t.id === tid); if (tn) focusInterval(tn.start, tn.end); }
  else if (mk) inspectMarker(mk.dataset.mk);
  else if (op && op.dataset.op) inspect(op.dataset.op);
  else if (group) { const g = current().groupById.get(group.dataset.group); if (g) focusInterval(g.start, g.end); }
  else if (stage) { const ta = Number(stage.dataset.ta), tb = Number(stage.dataset.tb); const span = Math.max(120e3, Math.min((tb - ta) * 3, (state.b - state.a) / 3)); chooseSpan(span, (ta + tb) / 2); }
});
document.addEventListener('keydown', e => {
  if (e.target.id !== 'plot') return; if (['+', '=', '-', '_', 'ArrowLeft', 'ArrowRight', 'Home'].includes(e.key)) e.preventDefault();
  if (e.key === '+' || e.key === '=') zoom(1); else if (e.key === '-' || e.key === '_') zoom(-1); else if (e.key === 'ArrowLeft') pan(-1); else if (e.key === 'ArrowRight') pan(1); else if (e.key === 'Home') setWindow(current().started, current().ended);
});
document.addEventListener('wheel', e => { if (!e.target.closest('#plot') || e.ctrlKey || e.metaKey || Math.abs(e.deltaX) < Math.abs(e.deltaY) || Math.abs(e.deltaX) < 2 || !geometry) return; e.preventDefault(); const delta = e.deltaX / geometry.P * (state.b - state.a); setWindow(state.a + delta, state.b + delta, { lower: false }); clearTimeout(tipTimer); tipTimer = setTimeout(renderLower, 250); }, { passive: false });
['inspector', 'guide'].forEach(id => { const d = $('#' + id); d.addEventListener('click', e => { if (e.target === d) d.close(); }); });
$('#inspector').addEventListener('close', () => { if (!$('#inspector').open) ++inspectorRequest; });
window.addEventListener('resize', () => { clearTimeout(resizeTimer); resizeTimer = setTimeout(() => { if (state.page === 'session') renderTimeline(); }, 80); });
window.addEventListener('popstate', route);
window.addEventListener('hashchange', route);
document.addEventListener('visibilitychange', () => { if (!document.hidden && state.page === 'session') schedulePoll(); });
function route() {
  const h = location.hash;
  if (h.startsWith('#session/')) {
    let id;
    try { id = decodeURIComponent(h.slice('#session/'.length)); } catch (e) { go('sessions'); return; }
    if (id !== requestedSessionID || state.page !== 'session') go('session', id);
  } else if (state.page !== 'sessions') go('sessions');
}
window.todobem = { state, current, windowStats, setWindow, go };
loadSessions().then(applied => { if (!applied) return; if (location.hash.startsWith('#session/')) route(); else go('sessions'); }).catch(e => { $('#main').innerHTML = `<div class="empty"><h3>Cannot reach the todobem server</h3><p>${esc(e.message)}</p></div>`; });
