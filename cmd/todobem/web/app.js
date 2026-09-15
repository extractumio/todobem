'use strict';
/* todobem SPA. Data comes from /api/* (see docs/ARCHITECTURE.md §3). All times are Unix ms.
   Aggregates use the root lane's exclusive partition; sub-agent time is shown separately. */
const LIST_CHUNK = 60; // operations rendered per scroll step
// BAND is the height of the stage band at the top of every lane row: the tinted run of one SDLC
// stage, its baseline at the band's bottom edge, and the label sitting clear above that line.
const BAND = 20;
const $ = (s, root = document) => root.querySelector(s);
const $$ = (s, root = document) => [...root.querySelectorAll(s)];
const esc = v => String(v ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const clamp = (n, a, b) => Math.max(a, Math.min(b, n));
const sum = a => a.reduce((n, x) => n + x, 0);
const pad = n => String(n).padStart(2, '0');
const plural = (n, word) => `${n} ${word}${n === 1 ? '' : 's'}`;
const MAIN_THREAD = 'main thread';
const MOTHER_AGENT = 'mother agent';
const overlap = (s, e, a, b) => Math.max(0, Math.min(e, b) - Math.max(s, a));

const paths = { grid: '<rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/>', timeline: '<path d="M3 6h8M15 6h6M3 12h4M11 12h10M3 18h12M19 18h2"/>', help: '<circle cx="12" cy="12" r="9"/><path d="M9.5 8.5a2.5 2.5 0 1 1 4 2c-1 .7-1.5 1.5-1.5 2.5m0 3h.01"/>', right: '<path d="m9 5 7 7-7 7"/>', left: '<path d="m15 5-7 7 7 7"/>', loop: '<path d="M19 7h-8a6 6 0 1 0 0 12h2M16 3l4 4-4 4M5 17h8a6 6 0 1 0 0-12h-2M8 21l-4-4 4-4"/>', repo: '<path d="M5 3h14v18H6a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2M4 17h15M8 7h6"/>', target: '<circle cx="12" cy="12" r="8"/><circle cx="12" cy="12" r="3"/><path d="m15 9 6-6m-4 0h4v4"/>', clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>', code: '<path d="m8 6-6 6 6 6m8-12 6 6-6 6m-3-15-2 18"/>', wait: '<path d="M7 3h10M7 21h10M8 3v5l8 8v5M16 3v5l-8 8v5"/>', gap: '<path d="M4 5v14m16-14v14M8 12h2m4 0h2"/>', expand: '<path d="M8 3H3v5m18 0V3h-5M3 16v5h5m8 0h5v-5"/>', latest: '<path d="M3 12h14m-5-5 5 5-5 5m9-13v16"/>', close: '<path d="m6 6 12 12M6 18 18 6"/>', info: '<circle cx="12" cy="12" r="9"/><path d="M12 11v6m0-10h.01"/>', shield: '<path d="m12 2 9 4v6c0 5-9 10-9 10S3 17 3 12V6z"/><path d="m8 11 3 3 5-6"/>', search: '<circle cx="10.5" cy="10.5" r="6.5"/><path d="m16 16 5 5"/>', check: '<path d="m5 12 4 4L19 6"/>', brain: '<path d="M12 4a3 3 0 0 0-3 3v10a3 3 0 0 0 6 0V7a3 3 0 0 0-3-3zM6 9a3 3 0 0 0 0 6M18 9a3 3 0 0 1 0 6"/>', refresh: '<path d="M20 12a8 8 0 1 1-2.3-5.7M20 4v5h-5"/>', agents: '<circle cx="7" cy="7" r="3"/><circle cx="17" cy="7" r="3"/><circle cx="12" cy="17" r="3"/><path d="M9 9l2 5M15 9l-2 5"/>', copy: '<rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h8"/>', gear: '<circle cx="12" cy="12" r="3"/><path d="M12 2v3m0 14v3M2 12h3m14 0h3M4.9 4.9l2.1 2.1m10 10 2.1 2.1M4.9 19.1 7 17m10-10 2.1-2.1"/>' };
// userGlyph draws the user-message marker: a person (head and shoulders) 13 px tall at (cx, top).
function userGlyph(cx, top, color, scale = 1) {
  const r = 2.7 * scale;
  const w = 5.6 * scale;
  return `<circle cx="${cx}" cy="${top + 3.2 * scale}" r="${r}" fill="${color}"/><path d="M${cx - w} ${top + 13 * scale}a${w} ${w} 0 0 1 ${2 * w} 0z" fill="${color}"/>`;
}
const icon = (name, small = false) => `<svg class="ico ${small ? 'small-ico' : ''}" viewBox="0 0 24 24" aria-hidden="true">${paths[name] || paths.info}</svg>`;

// prefGet / prefSet persist a small UI preference (e.g. the sessions-list period) in a cookie,
// so the choice survives a reload. Client-side only; the loopback server ignores these cookies.
// Every access is guarded: a locked-down browser can make document.cookie throw.
function prefGet(key) {
  try {
    const m = document.cookie.match(new RegExp('(?:^|; )' + key.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + '=([^;]*)'));
    return m ? decodeURIComponent(m[1]) : '';
  } catch (e) { return ''; }
}
function prefSet(key, val) {
  try { document.cookie = `${key}=${encodeURIComponent(val)}; path=/; max-age=31536000; samesite=lax`; } catch (e) { }
}
// hourglass marks a live tail whose telemetry has not arrived yet — the agent is generating and
// nothing has been recorded since the last event. It flips slowly so it reads as "in progress".
function hourglass(cx, cy, color) {
  return `<g><path d="M${cx - 4} ${cy - 5.5}h8M${cx - 4} ${cy + 5.5}h8" stroke="${color}" stroke-width="1.4" stroke-linecap="round"/><path d="M${cx - 3.3} ${cy - 4.6}L${cx} ${cy}L${cx + 3.3} ${cy - 4.6}Z" fill="${color}"/><path d="M${cx - 3.3} ${cy + 4.6}L${cx} ${cy}L${cx + 3.3} ${cy + 4.6}Z" fill="${color}" opacity=".5"/><animateTransform attributeName="transform" type="rotate" from="0 ${cx} ${cy}" to="180 ${cx} ${cy}" dur="2.6s" repeatCount="indefinite"/></g>`;
}

const PHASES = {
  llm: { name: 'LLM', short: 'LLM', color: '#348989', kind: 'model' },
  code: { name: 'Development', short: 'Dev', color: '#4690ce', kind: 'work' }, // the JSON key stays `code`: overlays, caches and the API reference it
  build: { name: 'Build', short: 'Build', color: '#a889fc', kind: 'work' },
  test: { name: 'Testing', short: 'Test', color: '#d9bc3d', kind: 'work' },
  release: { name: 'Release & deploy', short: 'Release', color: '#4aa65f', kind: 'work' },
  infra: { name: 'Infrastructure', short: 'Infra', color: '#c4956e', kind: 'work' },
  wait_worker: { name: 'Waiting for workers / harness', short: 'Workers', color: '#48aa8c', kind: 'wait' },
  wait_user: { name: 'Waiting for user', short: 'User', color: '#d3d8dc', kind: 'wait' }, // the user-marker pill's grey: the user's time, the user's colour
  idle: { name: 'Idle (awaiting parent)', short: 'Idle', color: '#30414f', kind: 'wait' },
  compaction: { name: 'Context compaction', short: 'Compact', color: '#c08a45', kind: 'overhead' },
  no_telemetry: { name: 'No telemetry', short: 'No data', color: '#7d8fa1', kind: 'unknown' },
  unknown: { name: 'Unknown', short: 'Unknown', color: '#98a4ad', kind: 'unknown' },
};
const PHASE_ORDER = Object.keys(PHASES);
// SUBGROUPS are the finer rows a phase splits into in the breakdown (classify.Subgroup, carried
// on every op as `sub`): only the phases that lump different work have them.
const SUBGROUPS = {
  code: { read: 'Reading files', search: 'Searching & listing', edit: 'Editing files', vcs: 'Version control (git)', hosting: 'Code hosting (gh / glab)', network: 'Network & web', mcp: 'MCP tools', shell: 'Shell, data & inspection' },
  wait_worker: { agents: 'Sub-agents', polling: 'Processes & CI polling', hooks: 'Hooks (harness)' },
  unknown: { script: 'Unclassified scripts', tool: 'Unmapped tools', command: 'Unmatched commands' },
};
const subgroupName = (phase, sub) => (SUBGROUPS[phase] || {})[sub] || sub;
// SDLC lifecycle stages: the second exclusive partition of a lane's time (docs/ARCHITECTURE.md §6
// "Lifecycle"). Phase says what a tool call was; lifecycle says which stage of the software
// lifecycle it served. The eight work stages come first in lifecycle order; non-work time
// passes through under its phase name and reuses that phase's colour.
// Colour = concept, shape = partition: a stage that is the default home of an activity shares
// its hue (implement = Development, test = Testing, release = Release, operate = Infrastructure);
// the four stages no activity maps to (plan, requirements, design, review) take hues no fill
// uses. Activity is drawn as a solid fill / square, a stage as a thin rail / dash — so purple
// is only ever Build and the rail under a fill is never read as an activity.
const LIFECYCLES = {
  plan: { name: 'Planning', short: 'Plan', color: '#7fcbe6', work: true },
  requirements: { name: 'Requirements', short: 'Reqs', color: '#f0a0c9', work: true },
  design: { name: 'Design', short: 'Design', color: '#e77f68', work: true },
  implement: { name: 'Implementation', short: 'Implement', color: '#4690ce', work: true },
  review: { name: 'Code review', short: 'Review', color: '#cf5e9e', work: true },
  test: { name: 'Testing / QA', short: 'Test', color: '#d9bc3d', work: true },
  release: { name: 'Deployment / release', short: 'Release', color: '#4aa65f', work: true },
  operate: { name: 'Maintenance / operations', short: 'Operate', color: '#c4956e', work: true },
  llm: { name: 'Model output, no tool call', short: 'Model', color: PHASES.llm.color },
  wait_user: { name: PHASES.wait_user.name, short: PHASES.wait_user.short, color: PHASES.wait_user.color },
  wait_worker: { name: PHASES.wait_worker.name, short: PHASES.wait_worker.short, color: PHASES.wait_worker.color },
  idle: { name: PHASES.idle.name, short: PHASES.idle.short, color: PHASES.idle.color },
  compaction: { name: PHASES.compaction.name, short: PHASES.compaction.short, color: PHASES.compaction.color },
  no_telemetry: { name: PHASES.no_telemetry.name, short: PHASES.no_telemetry.short, color: PHASES.no_telemetry.color },
  unknown: { name: PHASES.unknown.name, short: PHASES.unknown.short, color: PHASES.unknown.color },
};
const LIFECYCLE_ORDER = Object.keys(LIFECYCLES);
// lifecycleOf: an op's stage; a segment or op from an older cache without the field falls back
// to its phase name, which is exactly the pass-through value.
const lifecycleOf = o => o.lc || o.p || o.phase;
const alphaOf = p => 1;
const swatch = p => p === 'no_telemetry' ? `<i class="color-square hatch" style="background:#22313d"></i>` : `<i class="color-square" style="background:${PHASES[p].color}"></i>`;
// railSwatch is the legend mark of a lifecycle stage: a thin bar like the stage band above a lane's fill, so the
// breakdown, the filter chip and the guide show a stage the way the timeline does.
const railSwatch = color => `<i class="color-rail" style="background:${color}"></i>`;
// swatchFor picks the mark of a breakdown row from its definition: a rail for a work stage, a
// square for an activity or a pass-through (which is its activity).
const swatchFor = def => def.work ? railSwatch(def.color) : `<i class="color-square" style="background:${def.color}"></i>`;
const ROLES = { background: { name: 'Background process', color: '#8a6d3b' }, parallel: { name: 'Parallel runs (same command)', color: '#7fa6c9' }, first: { name: 'First runs', color: '#d9bc3d' }, retry_after_failure: { name: 'Retry after failure', color: '#d77729' }, rerun: { name: 'Reruns (after pass)', color: '#e2cf6a' }, fix: { name: 'Fix between attempts', color: '#b22998' }, infra_recovery: { name: 'Infra recovery', color: '#c4956e' }, worker_queue: { name: 'Worker queue', color: '#48aa8c' }, single: { name: 'Single runs', color: '#b3a04a' }, remote: { name: 'On remote runner', color: '#8fb8d8' } };
const ROLE_ORDER = ['first', 'retry_after_failure', 'rerun', 'parallel', 'fix', 'infra_recovery', 'worker_queue', 'single', 'remote'];
const MARKS = { llm_error: { glyph: 'x', color: '#f0a742', name: 'LLM failure (invalid tool call)' }, system_message: { glyph: 'sys', color: '#7d8fa1', name: 'Harness message' }, user_message: { glyph: 'user', color: '#f3f6f8', name: 'User message' }, question: { glyph: 'q', color: '#f2a7b2', name: 'Question to user' }, final_answer: { glyph: 'check', color: '#4aa65f', name: 'Final answer' }, interrupted: { glyph: 'x', color: '#d76368', name: 'Interrupted' }, compaction: { glyph: 'diamond', color: '#c08a45', name: 'Context compaction' }, plan: { glyph: 'plan', color: '#c9b2ff', name: 'Plan update' }, skill: { glyph: 'skill', color: LIFECYCLES.review.color, name: 'Skill invoked' }, result_returned: { glyph: 'result', color: '#48aa8c', name: 'Sub-agent result' }, agent_started: { glyph: 'spawn', color: '#86d0b9', name: 'Sub-agent spawned' }, agent_interacted: { glyph: 'tick', color: '#86d0b9', name: 'Message to sub-agent' }, agent_completed: { glyph: 'done', color: '#86d0b9', name: 'Sub-agent turn completed' }, agent_interrupted: { glyph: 'x', color: '#d76368', name: 'Sub-agent interrupted' } };

const state = { locked: false, authEnabled: false, page: 'sessions', id: null, model: null, sessions: [], a: 0, b: 0, follow: false, interval: 60, expanded: new Set(), hiddenLanes: new Set(), phase: 'all', sub: 'all', role: 'all', lifecycle: 'all', lane: 'all', sort: 'longest', listShown: 60, metricsOpen: false, openRuns: new Set(), search: '', fleetSort: 'updated', fleetFilter: { kind: '30d', from: '', to: '', cwd: '', sources: {} }, selected: null, groups: true, inTurn: false, allLanes: false, failedOnly: false, breakdownSort: 'longest', convOpen: new Set(), version: null, lastRefresh: 0, pollTimer: null, tick: null, focus: null, insights: null, pendingFocus: null };
let geometry = null, overviewDrag = null, plotDrag = null, lastDragTime = 0, toastTimer, resizeTimer, tipTimer, focusTimer;
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
// laneEffort is the reasoning effort of a lane's turns: one value when they all agree, 'mixed'
// when they do not, '' when none is recorded.
function laneEffort(l) {
  const efforts = new Set(l.turns.map(t => t.effort).filter(Boolean));
  return efforts.size > 1 ? 'mixed' : efforts.size === 1 ? [...efforts][0] : '';
}
// agentCaption is a lane's second label line: who the thread is (nickname) and what runs it (model).
function agentCaption(l, withEffort = false) {
  const model = l.model ? l.model + (withEffort && laneEffort(l) ? ' / ' + laneEffort(l) : '') : '';
  return [l.nickname, model].filter(Boolean).join(' · ');
}
// laneSpan is a lane's lifetime on the timeline: a sub-agent runs from the parent's spawn marker
// (▶) to its last turn end, or to now while live.
function laneSpan(l) {
  const m = current();
  const spawn = (m.agentMarks.get(l.id) || []).find(k => k.kind === 'agent_started');
  return { t0: spawn ? spawn.t : l.started, t1: l.live ? m.now : l.ended };
}
// laneInWindow: the root lane is always drawn; a sub-agent row only while its lifetime overlaps
// the visible window, so a session with a hundred short-lived workers stays readable.
function laneInWindow(l) {
  if (l.depth === 0) return true;
  const { t0, t1 } = laneSpan(l);
  return t1 >= state.a && t0 <= state.b;
}
function trunc(s, n) {
  return s.length > n ? s.slice(0, Math.max(1, n - 1)) + '…' : s;
}
function roleOf(kind) { const i = (kind || '').indexOf('|'); return i >= 0 ? kind.slice(i + 1) : ''; }
// failure: the harness recorded a failure and it was not a query miss (a search with no match, a
// read of a missing path: status and exit stay literal, the op is not counted as a failed step)
const failure = o => o.status === 'failed' && !o.query_miss;
function baseKind(kind) { const i = (kind || '').indexOf('|'); return i >= 0 ? kind.slice(0, i) : (kind || ''); }

/* ---------- data ---------- */
class AuthError extends Error {}
async function api(path) {
  const r = await fetch(path, { cache: 'no-store' });
  if (r.status === 401) {
    lock();
    throw new AuthError('locked');
  }
  if (!r.ok) throw new Error(`${r.status} ${await r.text()}`);
  return r.json();
}
// apiPost: every write the UI makes (login, logout, an Insights scan, the settings) is a JSON
// POST — the server refuses anything a foreign page could send without a preflight. payload is
// the decoded JSON answer when there is one; text the raw body (an error line, say).
async function apiPost(path, body) {
  const r = await fetch(path, { method: 'POST', cache: 'no-store', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body || {}) });
  let payload = null, text = '';
  try { text = r.status === 204 ? '' : await r.text(); payload = text ? JSON.parse(text) : null; } catch (e) { payload = null; }
  return { status: r.status, ok: r.ok, payload, text };
}
function current() { return state.model; }
function root() { return state.model?.lanes[0]; }
function laneById(id) { return state.model?.lanes.find(l => l.id === id); }
function prepare(m) {
  m.groups = m.groups || []; m.lanes = m.lanes || []; m.totals = m.totals || {}; m.totals.by_kind = m.totals.by_kind || {}; m.totals.by_phase = m.totals.by_phase || {};
  for (const l of m.lanes) { l.turns = l.turns || []; l.ops = l.ops || []; l.markers = l.markers || []; l.segments = l.segments || []; l.active = l.active || []; l.by_phase = l.by_phase || {}; }
  for (const l of m.lanes) for (const mk of l.markers) mk.text = mk.text ?? '';
  m.opById = new Map();
  m.laneById = new Map();
  for (const l of m.lanes) { m.laneById.set(l.id, l); l.ops.sort((a, b) => a.start - b.start); for (const o of l.ops) m.opById.set(o.id, o); l.opsByEnd = [...l.ops].sort((a, b) => b.end - a.end); }
  m.groupById = new Map(m.groups.map(g => [g.id, g]));
  // runs: identical tool calls (same kind and title) that follow each other in one lane with no
  // other tool call between them — a polling loop of sleeps, say; the model's reasoning between
  // two calls does not break a run. The list shows a run as one row.
  for (const l of m.lanes) {
    let run = null;
    for (const o of l.ops) {
      if (o.phase === 'llm') continue;
      if (o.background) { run = null; continue; }
      if (run && run.kind === o.kind && run.title === o.title) { o.run = run.id; continue; }
      run = { id: `${l.id}:${o.id}`, kind: o.kind, title: o.title };
      o.run = run.id;
    }
  }
  const runSize = new Map();
  for (const l of m.lanes) for (const o of l.ops) runSize.set(o.run, (runSize.get(o.run) || 0) + 1);
  for (const l of m.lanes) for (const o of l.ops) if (runSize.get(o.run) < 2) delete o.run;
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
async function loadSession(id, { keepWindow = false, isCurrent = () => true, refresh = false } = {}) {
  if (state.page !== 'session' || id !== requestedSessionID) return false;
  const request = ++sessionRequest, navigation = navigationRequest;
  const ownsRequest = () => request === sessionRequest && navigation === navigationRequest && state.page === 'session' && id === requestedSessionID && isCurrent();
  let m;
  // Default loads take the cached model when it is current; the Refresh button forces a re-parse.
  try { m = await api('/api/sessions/' + encodeURIComponent(id) + (refresh ? '?refresh=1' : '')); }
  catch (e) { if (ownsRequest()) throw e; return false; }
  if (!ownsRequest()) return false;
  m = prepare(m);
  const prev = state.model;
  state.model = m; state.id = id; state.version = m.version; state.lastRefresh = Date.now();
  if (!keepWindow || !prev || prev.id !== id) { state.a = m.started; state.b = m.ended; state.expanded = new Set(); state.hiddenLanes = new Set(); state.phase = 'all'; state.sub = 'all'; state.role = 'all'; state.lifecycle = 'all'; state.lane = 'all'; state.listShown = LIST_CHUNK; state.openRuns = new Set(); state.selected = null; state.follow = m.live; }
  else if (state.follow) { const span = state.b - state.a; state.b = m.ended; state.a = Math.max(m.started, m.ended - span); }
  else if (Math.abs(prev.ended - state.b) < 1000) state.b = m.ended; // window was pinned to the end
  state.a = clamp(state.a, m.started, m.ended); state.b = clamp(state.b, state.a + 1000, m.ended);
  return true;
}

/* ---------- stats over a window (root lane exclusive partition) ---------- */
function windowStats(a, b) {
  const m = current(), r = root();
  const by = Object.fromEntries(PHASE_ORDER.map(k => [k, 0]));
  const byLc = Object.fromEntries(LIFECYCLE_ORDER.map(k => [k, 0]));
  const lcSplit = Object.fromEntries(LIFECYCLE_ORDER.map(k => [k, { llm: 0, tools: 0 }]));
  const bySub = {}, allBySub = {}; // exclusive time per "phase:subgroup" (the winning op's subgroup)
  const addSub = (into, sg, ov) => { const o = sg.op ? m.opById.get(sg.op) : null; if (o && o.sub) { const k = `${sg.p}:${o.sub}`; into[k] = (into[k] || 0) + ov; } };
  for (const sg of r.segments) {
    if (sg.e <= a) continue;
    if (sg.s >= b) break;
    const ov = overlap(sg.s, sg.e, a, b);
    by[sg.p] += ov;
    addSub(bySub, sg, ov);
    const lc = lifecycleOf(sg);
    byLc[lc] = (byLc[lc] || 0) + ov;
    if (!lcSplit[lc]) lcSplit[lc] = { llm: 0, tools: 0 };
    lcSplit[lc][sg.p === 'llm' ? 'llm' : 'tools'] += ov;
  }
  const roles = Object.fromEntries(ROLE_ORDER.map(k => [k, 0]));
  for (const o of r.ops) { if (o.start >= b) break; const ov = overlap(o.start, o.end, a, b); if (!ov) continue; const role = roleOf(o.kind); if (role) roles[role] += ov; else if (o.phase === 'test') roles.single += ov; if (o.remote && (o.phase === 'test' || o.phase === 'build')) roles.remote += ov; }
  let agentMs = 0, agentLanes = 0;
  const ivs = [];
  for (const l of m.lanes.slice(1)) { let any = false; for (const iv of l.active || []) { const ov = overlap(iv.s, iv.e, a, b); if (ov) { agentMs += ov; any = true; ivs.push([Math.max(iv.s, a), Math.min(iv.e, b)]); } } if (any) agentLanes++; }
  ivs.sort((x, y) => x[0] - y[0]); let wall = 0, cs = 0, ce = -1; for (const [s, e] of ivs) { if (s > ce) { if (ce > cs) wall += ce - cs; cs = s; ce = e; } else if (e > ce) ce = e; } if (ce > cs) wall += ce - cs;
  const work = ['code', 'build', 'test', 'release', 'infra'].reduce((n, k) => n + by[k], 0);
  const allBy = Object.fromEntries(PHASE_ORDER.map(k => [k, 0])), subBy = Object.fromEntries(PHASE_ORDER.map(k => [k, 0]));
  const allByLc = Object.fromEntries(LIFECYCLE_ORDER.map(k => [k, 0]));
  const allLcSplit = Object.fromEntries(LIFECYCLE_ORDER.map(k => [k, { llm: 0, tools: 0 }]));
  for (const l of m.lanes) {
    for (const sg of l.segments) {
      if (sg.e <= a) continue;
      if (sg.s >= b) break;
      const ov = overlap(sg.s, sg.e, a, b);
      allBy[sg.p] += ov;
      addSub(allBySub, sg, ov);
      if (l !== r) subBy[sg.p] += ov;
      const lc = lifecycleOf(sg);
      allByLc[lc] = (allByLc[lc] || 0) + ov;
      if (!allLcSplit[lc]) allLcSplit[lc] = { llm: 0, tools: 0 };
      allLcSplit[lc][sg.p === 'llm' ? 'llm' : 'tools'] += ov;
    }
  }
  let raw = 0, bg = 0, bgOps = 0; for (const o of r.ops) { if (o.start >= b) break; const ov = overlap(o.start, o.end, a, b); if (o.background) { bg += ov; if (ov) bgOps++; } else raw += ov; }
  let inTurn = 0; for (const t of r.turns) inTurn += overlap(t.start, t.status === 'open' ? m.now : t.end, a, b);
  const workOf = o => ['code', 'build', 'test', 'release', 'infra'].reduce((n, k) => n + o[k], 0);
  return { duration: b - a, by, byLc, lcSplit, allByLc, allLcSplit, allBy, subBy, bySub, allBySub, roles, work, raw, bg, bgOps, inTurn, llm: by.llm, allWork: workOf(allBy), subWork: workOf(subBy), waiting: by.wait_user + by.wait_worker + by.idle, overhead: by.compaction, unknown: by.no_telemetry + by.unknown, agentMs, agentWall: wall, agentLanes };
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
  $('#navInsights').classList.toggle('active', state.page === 'insights'); $('#mobileInsights').classList.toggle('active', state.page === 'insights');
  $('#navSettings').classList.toggle('active', state.page === 'settings');
  $('#navSession').disabled = !m; $('#mobileSession').disabled = !m;
  $('#breadcrumb').innerHTML = `<span>Agent sessions</span><span class="slash">/</span><button data-action="sessions">Sessions</button>${state.page === 'session' && m ? `<span class="slash">/</span><span class="mono">${esc(m.id.slice(0, 8))}</span>` : ''}${state.page === 'insights' ? '<span class="slash">/</span><span>Insights</span>' : ''}${state.page === 'settings' ? '<span class="slash">/</span><span>Settings</span>' : ''}`;
  $('#focusCard').innerHTML = m ? `<button class="current-card" data-action="session"><span class="row"><i class="dot" style="background:${m.live ? '#5fe0a0' : 'var(--accent)'}"></i><span class="mono">${esc(m.id.slice(0, 8))}</span></span><strong>${esc(m.title)}</strong><span>${fmt(m.ended - m.started)} · ${m.lanes.length - 1} sub-agents</span></button>` : '<div class="current-card"><span>No session open</span></div>';
  document.title = `${state.page === 'session' && m ? m.title : state.page === 'insights' ? 'Insights' : state.page === 'settings' ? 'Settings' : 'Sessions'} · todobem`;
  liveLabel();
}
function liveLabel() {
  const m = current(), el = $('#liveLabel');
  if (!m || state.page !== 'session') { el.innerHTML = ''; return; }
  el.innerHTML = m.live ? `<span class="chip live"><i class="dot"></i>LIVE · refreshed ${ago(state.lastRefresh)}</span>` : `<span class="chip">${icon('check', true)}Session closed · ${ago(m.ended)}</span>`;
}
function setHash(h) { try { if (location.hash !== h) history.pushState(null, '', h); } catch (e) { } }
function go(page, id) {
  if (state.locked) return;
  const navigation = ++navigationRequest;
  ++sessionRequest; ++sessionsRequest; ++inspectorRequest;
  if ($('#inspector').open) $('#inspector').close();
  stopPoll();
  state.page = page;
  requestedSessionID = page === 'session' ? id || state.id : null;
  if (page === 'insights') {
    setHash('#insights');
    render();
    loadInsights();
    window.scrollTo(0, 0);
    return;
  }
  if (page === 'settings') {
    setHash('#settings');
    render();
    loadSettings();
    window.scrollTo(0, 0);
    return;
  }
  if (page === 'session' && id && id !== state.id) {
    setHash('#session/' + encodeURIComponent(id)); $('#main').innerHTML = '<div class="loading">Parsing session…</div>';
    loadSession(id).then(applied => { if (applied && navigation === navigationRequest) { render(); schedulePoll(); applyPendingFocus(); } }).catch(e => {
      if (navigation !== navigationRequest || e instanceof AuthError) return; // locked: the lock screen is already up
      console.error(e); $('#main').innerHTML = `<div class="empty"><h3>Could not load session</h3><p>${esc(e.message)}</p></div>`;
    });
    return;
  }
  if (page === 'session' && id) { setHash('#session/' + encodeURIComponent(id)); applyPendingFocus(); }
  if (page === 'sessions') {
    setHash('#sessions');
    loadSessions().then(applied => { if (applied && navigation === navigationRequest) render(); }).catch(e => {
      if (navigation === navigationRequest && !(e instanceof AuthError)) toast('Could not refresh sessions: ' + e.message);
    });
  }
  render(); schedulePoll(); window.scrollTo(0, 0);
}
// A live update rebuilds the whole page. Without this the reader loses their place: the window
// jumps, a focused control drops focus and every scrolled list snaps back to the top. The visible
// time window (and with it the timeline's L/R handles), the filters, the expanded lanes and the
// selection all live in `state` and survive on their own; only what the DOM itself owns has to be
// carried across.
function keepPlace(paint) {
  const y = window.scrollY || 0, x = window.scrollX || 0;
  const active = document.activeElement;
  const focusID = active && active.id ? active.id : '';
  const range = focusID && active.selectionStart != null ? [active.selectionStart, active.selectionEnd] : null;
  const scrolled = $$('.scroll-fade').map(el => [el.scrollTop || 0, el.scrollLeft || 0]);
  paint();
  const after = $$('.scroll-fade');
  if (after.length === scrolled.length) after.forEach((el, i) => { if (scrolled[i][0] || scrolled[i][1]) { el.scrollTop = scrolled[i][0]; el.scrollLeft = scrolled[i][1]; } });
  if (focusID) {
    const el = $('#' + focusID);
    if (el && el.focus) { try { el.focus({ preventScroll: true }); if (range && el.setSelectionRange) el.setSelectionRange(range[0], range[1]); } catch (e) { } }
  }
  if (window.scrollTo) window.scrollTo(x, y);
}
function render() { nav(); if (state.page === 'sessions') { $('#main').innerHTML = fleetPage(); renderFleetRows(); } else if (state.page === 'insights') { renderInsights(); return; } else if (state.page === 'settings') { renderSettings(); return; } else if (current()) { $('#main').innerHTML = sessionPage(); renderOverview(); renderTimeline(); renderLower(); renderSessionInsights(); } $$('[data-icon]').forEach(el => el.innerHTML = icon(el.dataset.icon)); }
// applyPendingFocus honours a `#session/<id>?focus=<a>-<b>` link (an Insights evidence row):
// the session opens and the timeline highlights that interval.
function applyPendingFocus() {
  const f = state.pendingFocus;
  if (!f || state.page !== 'session' || !current()) return;
  state.pendingFocus = null;
  focusInterval(f.a, f.b);
}
function toast(text) { const el = $('#toast'); el.textContent = text; el.classList.add('visible'); clearTimeout(toastTimer); toastTimer = setTimeout(() => el.classList.remove('visible'), 3500); }

/* ---------- polling ---------- */
function stopPoll() {
  if (state.insights) { clearTimeout(state.insights.timer); state.insights.timer = null; }
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
        keepPlace(render); toast(current().live ? 'Live update applied.' : 'Session updated.');
      } else { state.lastRefresh = Date.now(); liveLabel(); }
    } catch (e) { if (ownsPoll() && request === sessionRequest) console.warn(e); }
    finally { pollBusy = false; }
  };
  state.pollTimer = setInterval(poll, state.interval * 1000);
}

/* ---------- fleet page ---------- */
const FLEET_TEXT = {
  eyebrow: 'Agent sessions on this machine',
  title: 'Sessions',
  subtitle: 'Main threads of the configured Codex and Claude Code folders. Open one to see where the time went.',
  search: 'Search title, path, branch…',
  sort: { updated: 'Recently updated', size: 'Largest logs', agents: 'Most sub-agents', started: 'Started first' },
  columns: ['Session', 'Started', 'Updated', 'Elapsed', 'Sub-agents', 'Log size'],
  summary: { sessions: 'Sessions in view', agents: 'Sub-agent threads', logs: 'Session logs', recent: 'Written in the last 10 min' },
  active: 'active',
  awaiting: 'waiting',
  awaitingNote: 'The agent asked you a question and is still waiting for the answer. Open the session to read it.',
  elapsedNote: 'from file timestamps; open to parse',
  empty: { title: 'No sessions match.', body: 'Try another search, period, project or source.' },
};
// fleetPage: the heading, one bar with the shared period + project + source filter (filter.js)
// persistFleetPeriod / restoreFleetPeriod keep the sessions-list period across reloads (cookie).
// Only the period (kind + custom dates) is remembered; project, source and search reset per visit.
function persistFleetPeriod() {
  const f = state.fleetFilter;
  prefSet('todobem_fleet_period', JSON.stringify({ kind: f.kind, from: f.from, to: f.to }));
}
function restoreFleetPeriod() {
  try {
    const v = JSON.parse(prefGet('todobem_fleet_period') || 'null');
    if (v && FILTER_KINDS.includes(v.kind)) {
      state.fleetFilter.kind = v.kind;
      state.fleetFilter.from = v.from || '';
      state.fleetFilter.to = v.to || '';
    }
  } catch (e) { }
}
// next to the search and the sort, the summary tiles of the sessions in view, then the rows.
function fleetPage() {
  const T = FLEET_TEXT;
  const filter = filterBarHTML('fleet', state.fleetFilter, { projects: filterProjects(state.fleetFilter, state.sessions), sessions: state.sessions });
  const search = `<label class="search-box">${icon('search', true)}<input id="sessionSearch" type="search" value="${esc(state.search)}" placeholder="${esc(T.search)}" aria-label="Search sessions"></label>`;
  const sortOptions = Object.entries(T.sort).map(([k, label]) => `<option value="${k}" ${state.fleetSort === k ? 'selected' : ''}>${esc(label)}</option>`).join('');
  const sort = `<label class="sr-only" for="fleetSort">Sort sessions</label><select class="select" id="fleetSort">${sortOptions}</select>`;
  return `<section class="page-heading"><div><div class="eyebrow">${esc(T.eyebrow)}</div><h1>${esc(T.title)}</h1><p class="subtitle">${esc(T.subtitle)}</p></div><span class="chip" id="fleetCount">${state.sessions.length} sessions</span></section>
<section class="report-bar" aria-label="Sessions period, project, source, search and sort">${filter}<div class="bar-actions bar-fill">${search}${sort}</div></section>
<section class="fleet-summary" id="fleetSummary"></section>
<section class="card"><div id="fleetRows"></div></section>${footer()}`;
}
// fleetCompare orders the rows for the chosen sort.
function fleetCompare(a, b) {
  switch (state.fleetSort) {
    case 'size': return b.bytes - a.bytes;
    case 'agents': return b.agents - a.agents;
    case 'started': return a.started - b.started;
    default: return b.updated - a.updated;
  }
}
// sessionLink: the row's first cell — the source mark, the title, the last answer and the meta
// line (project, branch, model, harness version).
function sessionLink(s) {
  const desc = s.last_answer ? `<em class="desc" title="${esc(descriptionOf(s.last_answer, 400))}">${esc(descriptionOf(s.last_answer, 160))}</em>` : '';
  const meta = [shortPath(s.cwd), s.branch, s.model, s.cli ? `${sourceName(s)} ${s.cli}` : sourceName(s)].filter(Boolean).map(esc).join(' · ');
  return `<button class="session-link" data-action="session" data-id="${esc(s.id)}"><strong>${sourceMark(s)}${esc(s.title)}</strong>${desc}<span>${meta}</span></button>`;
}
// askChip: a question (or a plan awaiting approval) the harness recorded and nothing answered —
// a chip with a pulsing ? next to the "active" chip, and the time it has waited so far.
function askChip(s, now) {
  if (!s.question) {
    return '';
  }
  const T = FLEET_TEXT;
  return ` <span class="chip ask" title="${esc(T.awaitingNote)}"><i class="qmark" aria-hidden="true">?</i>${esc(T.awaiting)} ${fmt(now - s.question)}</span>`;
}
function renderFleetRows() {
  const T = FLEET_TEXT;
  const q = state.search.toLowerCase(), f = state.fleetFilter, now = Date.now();
  const matches = s => filterKeeps(f, s, now) && `${s.title} ${s.cwd} ${s.branch || ''} ${s.id} ${sourceName(s)}`.toLowerCase().includes(q);
  const rows = state.sessions.filter(matches).sort(fleetCompare);
  const recent = s => now - s.updated < 10 * 60e3;
  const count = $('#fleetCount');
  if (count) count.textContent = rows.length === state.sessions.length ? plural(rows.length, 'session') : `${rows.length} of ${plural(state.sessions.length, 'session')}`;
  const tiles = [
    [rows.length, T.summary.sessions],
    [sum(rows.map(s => s.agents)), T.summary.agents],
    [`${(sum(rows.map(s => s.bytes)) / 1e6).toFixed(0)} MB`, T.summary.logs],
    [rows.filter(recent).length, T.summary.recent],
  ];
  $('#fleetSummary').innerHTML = tiles.map(([v, label]) => `<div><strong class="num">${v}</strong><span>${esc(label)}</span></div>`).join('');
  if (!rows.length) {
    $('#fleetRows').innerHTML = `<div class="empty"><h3>${esc(T.empty.title)}</h3><p>${esc(T.empty.body)}</p></div>`;
    return;
  }
  const stat = s => s.totals ? `<span class="mono">${fmt(s.totals.elapsed_ms)}</span>` : `<span class="mono note" title="${esc(T.elapsedNote)}">≈ ${fmt(Math.max(0, s.updated - s.started))}</span>`;
  const activeChip = s => recent(s) ? ` <span class="chip live" style="padding:2px 6px"><i class="dot"></i>${esc(T.active)}</span>` : '';
  const tableRow = s => `<tr><td>${sessionLink(s)}</td><td class="mono">${stamp(s.started)}</td><td class="mono">${stamp(s.updated)}${activeChip(s)}${askChip(s, now)}</td><td>${stat(s)}</td><td class="mono">${s.agents}</td><td class="mono">${(s.bytes / 1e6).toFixed(1)} MB</td></tr>`;
  const tile = s => `<article class="session-tile">${sessionLink(s)}${askChip(s, now)}<div class="session-tile-metrics"><div><strong class="mono">${stamp(s.updated)}</strong><small>Updated</small></div><div><strong class="mono">${s.agents}</strong><small>Sub-agents</small></div><div><strong class="mono">${(s.bytes / 1e6).toFixed(0)} MB</strong><small>Log</small></div></div></article>`;
  const head = `<thead><tr>${T.columns.map(c => `<th>${esc(c)}</th>`).join('')}</tr></thead>`;
  $('#fleetRows').innerHTML = `<table class="fleet-table">${head}<tbody>${rows.map(tableRow).join('')}</tbody></table><div class="mobile-fleet">${rows.map(tile).join('')}</div>`;
}
function footer() { return `<footer class="page-footer"><span class="row">${icon('shield', true)}Local files only. Nothing leaves this machine.</span><span>Times shown in ${esc(TZ)} · todobem / 0.1</span></footer>`; }

/* ---------- session page ---------- */
function fmtTok(n) { n = Number(n) || 0; return n >= 1e9 ? (n / 1e9).toFixed(2) + ' B' : n >= 1e6 ? (n / 1e6).toFixed(1) + ' M' : n >= 1e3 ? (n / 1e3).toFixed(0) + ' k' : String(n); }
function metricsHTML() {
  const m = current(), t = windowStats(m.started, m.ended), tk = m.totals.tokens || {};
  const working = t.duration - t.by.wait_user;
  const comp = l => l.ops.reduce((acc, o) => o.phase === 'compaction' ? { n: acc.n + 1, ms: acc.ms + (o.end - o.start) } : acc, { n: 0, ms: 0 });
  const rootComp = comp(m.lanes[0]), subComp = m.lanes.slice(1).map(comp).reduce((a, b) => ({ n: a.n + b.n, ms: a.ms + b.ms }), { n: 0, ms: 0 });
  const A = t.allBy, S = t.subBy, sub = (rootV, subV) => `${MAIN_THREAD} ${fmt(rootV)} · sub-agents ${fmt(subV)}`;
  const cells = [
    ['Working time', fmt(working), `wall clock minus waiting for user input · ${fmt(t.inTurn)} inside turns`, 'code'],
    ['Tokens', fmtTok(tk.total), tk.total ? `in ${fmtTok(tk.input)} (cached ${fmtTok(tk.cached)}) · out ${fmtTok(tk.output)} · reasoning ${fmtTok(tk.reasoning)} · all lanes` : 'no token_count events', 'brain'],
    ['LLM', fmt(A.llm), `all lanes · ${sub(t.by.llm, S.llm)}`, 'brain'],
    ['Tools', fmt(t.allWork), `development, build, test, release, infra · all lanes · ${sub(t.work, t.subWork)}`, 'code'],
    ['Known waiting', fmt(t.by.wait_user + A.wait_worker), `user ${fmt(t.by.wait_user)} · workers/harness ${fmt(A.wait_worker)} (all lanes)`, 'wait'],
    ['Context compaction', fmt(rootComp.ms + subComp.ms), `${rootComp.n + subComp.n}× the harness re-summarized a context window (${MAIN_THREAD} ${rootComp.n}× ${fmt(rootComp.ms)} · sub-agents ${subComp.n}× ${fmt(subComp.ms)}) · ${fmt(A.compaction)} of it not overlapping other work${m.totals.background_ops ? ` · ${m.totals.background_ops} background proc. ${fmt(m.totals.background_ms)} not counted` : ''}`, 'loop'],
    ['No telemetry / unattributed', fmt(A.no_telemetry + A.unknown), `no events ${fmt(A.no_telemetry)} · unmatched events ${fmt(A.unknown)} · all lanes`, 'gap'],
    ['Sub-agent time', fmt(t.agentMs), `${t.agentLanes} lanes · ${fmt(t.agentWall)} wall · runs in parallel with the ${MAIN_THREAD}`, 'agents'],
  ];
  const lcTotals = m.totals.by_lifecycle || {};
  if (lcTotals.review || m.totals.reviews) cells.push(['Code review', fmt(lcTotals.review || 0), `lifecycle stage on the ${MAIN_THREAD}: turns where a review/cleanup skill was invoked (${m.totals.reviews || 0}× across all lanes), Codex review mode, PR/MR review verbs · sub-agent reviews are parallel time`, 'loop']);
  if (lcTotals.plan) cells.push(['Planning', fmt(lcTotals.plan), `lifecycle stage on the ${MAIN_THREAD}: plan-mode turns and the model output before each plan it wrote (update_plan)`, 'brain']);
  // collapsed (default): label + value per tile, four per row; expanded: the notes and the caveat
  const open = state.metricsOpen;
  return `<section class="metrics ${open ? 'open' : ''}" aria-label="Whole-session accounting"><div class="metrics-head"><span class="eyebrow">Whole-session totals</span><button class="text-btn small" data-action="metrics-toggle" aria-expanded="${open}">${open ? 'Hide details' : 'Details'}</button></div><div class="metrics-grid">${cells.map(([label, v, note, ic]) => `<div class="metric" title="${esc(note)}"><span class="metric-label">${icon(ic, true)}${label}</span><strong class="metric-value num">${v}</strong>${open ? `<span class="metric-note">${esc(note)}</span>` : ''}</div>`).join('')}</div>${open ? `<p class="metrics-note">LLM, Tools, waiting, compaction and telemetry sum agent time over all lanes (sub-agents run in parallel, so they can exceed the wall clock). Elapsed and Working time are wall clock of the ${MAIN_THREAD}. The SDLC ring is the ${MAIN_THREAD}'s lifecycle partition: each stage's share of the whole session, model time attributed to the stage it served.</p>` : ''}</section>`;
}
// lifecycleRingHTML draws the eight SDLC stages as a connected cycle: a ring of circles joined by
// arrows in lifecycle order, each circle carrying its share of the whole session (the main
// thread's by_lifecycle over elapsed — the same partition the lane rails and the breakdown use,
// so model time counts under the stage it served). A stage with no time is pale grey outlined in
// its stage colour; a stage with time is filled in that colour with the % inside. metricsHTML
// rebuilds on every session update, so the ring is redrawn then.
function lifecycleRingHTML(m) {
  const total = (m.totals && m.totals.elapsed_ms) || (m.ended - m.started) || 1;
  const lc = (m.totals && m.totals.by_lifecycle) || {};
  const stages = LIFECYCLE_ORDER.filter(k => LIFECYCLES[k].work); // the eight, in lifecycle order
  const abbr = { plan: 'Plan', requirements: 'Reqs', design: 'Design', implement: 'Impl', review: 'Review', test: 'Test', release: 'Rel', operate: 'Ops' };
  const cx = 128, cy = 106, R = 70, r = 18, N = stages.length; // labels sit 8 px clear of the discs; the viewBox leaves room for them
  const base = i => -Math.PI / 2 + i * 2 * Math.PI / N;
  const at = (a, rad) => [cx + rad * Math.cos(a), cy + rad * Math.sin(a)];
  const gap = (r + 7) / R;
  let arcs = '', nodes = '';
  for (let i = 0; i < N; i++) {
    const [sx, sy] = at(base(i) + gap, R), [ex, ey] = at(base(i + 1) - gap, R);
    arcs += `<path d="M${sx.toFixed(1)} ${sy.toFixed(1)} A ${R} ${R} 0 0 1 ${ex.toFixed(1)} ${ey.toFixed(1)}" fill="none" stroke="var(--line)" stroke-width="1.4" marker-end="url(#lcArrow)"/>`;
  }
  for (let i = 0; i < N; i++) {
    const k = stages[i], def = LIFECYCLES[k], ms = lc[k] || 0, pct = ms / total * 100, on = ms > 0;
    const [x, y] = at(base(i), R);
    const label = !on ? '0' : pct < 1 ? '<1%' : Math.round(pct) + '%';
    const [lx, ly] = at(base(i), R + r + 8);
    const anchor = lx < cx - 4 ? 'end' : lx > cx + 4 ? 'start' : 'middle';
    const dy = ly < cy - R + 2 ? -1 : ly > cy + R - 2 ? 8 : 3;
    const title = `${def.name} · ${on ? fmt(ms) + ' · ' + pct.toFixed(1) + '%' : 'not used'}`;
    // the disc fills from the bottom up in proportion to the stage's share, like a filling
    // glass: the level is that share, the pale remainder the session it is a share of
    let disc;
    if (!on) disc = `<circle cx="${x.toFixed(1)}" cy="${y.toFixed(1)}" r="${r}" fill="var(--raised)" stroke="${def.color}" stroke-width="1.8" stroke-dasharray="3 2"/>`;
    else if (pct >= 99.9) disc = `<circle cx="${x.toFixed(1)}" cy="${y.toFixed(1)}" r="${r}" fill="${def.color}"/>`;
    else {
      // the level rises with the share, like liquid in a glass: the filled part is the disc
      // below the waterline, so half the circle is exactly half the session
      const h = 2 * r * pct / 100, wy = y + r - h, dy = wy - y, dx = Math.sqrt(Math.max(0, r * r - dy * dy));
      disc = `<circle cx="${x.toFixed(1)}" cy="${y.toFixed(1)}" r="${r}" fill="${def.color}" fill-opacity=".18" stroke="${def.color}" stroke-width="1.5"/>`
        + `<path class="lc-fill" d="M${(x - dx).toFixed(1)} ${wy.toFixed(1)}A${r} ${r} 0 ${h > r ? 1 : 0} 0 ${(x + dx).toFixed(1)} ${wy.toFixed(1)}Z" fill="${def.color}"/>`;
    }
    nodes += `<g><title>${esc(title)}</title>${disc}` +
      `<text x="${x.toFixed(1)}" y="${(y + 3.5).toFixed(1)}" text-anchor="middle" class="lc-pct${on ? '' : ' off'}">${label}</text>` +
      `<text x="${lx.toFixed(1)}" y="${(ly + dy).toFixed(1)}" text-anchor="${anchor}" class="lc-name">${abbr[k]}</text></g>`;
  }
  return `<figure class="lifecycle-ring" aria-label="SDLC lifecycle: each stage's share of session time"><svg viewBox="0 0 256 212" preserveAspectRatio="xMidYMid meet" role="img"><defs><marker id="lcArrow" viewBox="0 0 10 10" refX="8.5" refY="5" markerWidth="5.5" markerHeight="5.5" orient="auto"><path d="M0 1L9 5L0 9z" fill="var(--line)"/></marker></defs><circle cx="${cx}" cy="${cy}" r="${R}" fill="none" stroke="var(--line-soft)" stroke-width="1"/>${arcs}${nodes}<text x="${cx}" y="${cy - 4}" text-anchor="middle" class="lc-hub">SDLC</text><text x="${cx}" y="${cy + 9}" text-anchor="middle" class="lc-hub-sub">lifecycle</text></svg><figcaption>Share of session by stage</figcaption></figure>`;
}
function sessionSummary() {
  const m = current(), r = root(), last = r.turns[r.turns.length - 1];
  const status = m.live ? { cls: 'live', text: 'In progress · turn open' } : last?.status === 'aborted' ? { cls: 'failed', text: 'Interrupted' } : last?.status === 'orphaned' ? { cls: '', text: 'Ended without closing the last turn' } : { cls: 'passed', text: 'Completed' };
  const firstMsg = firstRequest(r);
  const cells = [['Created', stampS(m.started)], ['First message', firstMsg ? stampS(firstMsg.t) + (firstMsg.t - m.started > 60e3 ? ` (+${fmt(firstMsg.t - m.started)})` : '') : '—'], [m.live ? 'Last event' : 'Ended', stampS(m.ended)], ['Duration', fmt(m.ended - m.started, true) + (m.live ? ' so far' : '')], ['Status', `<span class="chip ${status.cls}">${status.cls === 'live' ? '<i class="dot"></i>' : ''}${status.text}</span> <span class="mono note">${r.turns.length} turns · ${m.totals.user_messages} user messages</span>`]];
  return `<dl class="session-summary">${cells.map(([k, v]) => `<div><dt>${k}</dt><dd>${v}</dd></div>`).join('')}</dl>`;
}
// firstRequest: the first user message that started a turn — a slash command the harness
// answered by itself (/clear, /cost) is the user's input but not a request to the agent; it
// stays in the conversation panel and is the fallback when nothing started a turn.
function firstRequest(lane) {
  return lane.markers.find(k => k.kind === 'user_message' && k.turn) || lane.markers.find(k => k.kind === 'user_message');
}
// descriptionOf: the first paragraph of a recorded answer, markdown stripped, clipped, for the
// session list. It is the only description of a session that is on record (the final message of
// its last completed turn); nothing is ever generated for it. The session page shows the whole
// answer instead (the Last answer panel).
function descriptionOf(text, max) {
  const para = String(text || '').trim().split(/\n\s*\n/)[0] || '';
  const flat = stripMd(para);
  return flat.length > max ? flat.slice(0, max - 1).trimEnd() + '…' : flat;
}
function stripMd(t) { return (t || '').replace(/\[([^\]]+)\]\([^)]*\)/g, '$1').replace(/[*_`#>]+/g, '').replace(/\s+/g, ' ').trim(); }
async function copyText(t) {
  try { await navigator.clipboard.writeText(t); return true; }
  catch (e) {
    try {
      const ta = document.createElement('textarea');
      ta.value = t; ta.style.position = 'fixed'; ta.style.opacity = '0';
      document.body.appendChild(ta); ta.select();
      const ok = document.execCommand('copy'); ta.remove();
      return ok;
    } catch (_) { return false; }
  }
}
function lastState() {
  const m = current(), r = root();
  if (!r.segments.length) return '<p class="muted-note">No activity recorded.</p>';
  const fin = [...r.markers].reverse().find(k => k.kind === 'final_answer');
  let html = '';
  // The agent's final answer, verbatim and scrollable, with a one-click copy.
  if (fin) {
    html += `<div class="answer-head"><span class="eyebrow">Last answer · ${stamp(fin.t)}</span><div class="answer-actions"><button class="btn tiny" data-action="copy-answer">${icon('copy', true)}Copy</button><button class="btn tiny ghost" data-action="focus-at" data-t="${fin.t}">${icon('expand', true)}In timeline</button></div></div><div class="answer-block prose-block scroll-fade" tabindex="0">${esc(fin.text)}</div>`;
  } else {
    html += `<div class="answer-head"><span class="eyebrow">Last answer</span></div><p class="muted-note">No final answer recorded${m.live ? ' yet — the session is still in progress.' : ' (the last turn produced no final message).'}</p>`;
  }
  // For a live session, what is in progress right now (unique to this panel); nothing extra for
  // a closed one — the end time and turn count already live in the summary on the left.
  if (m.live) {
    const sg = r.segments[r.segments.length - 1];
    if (sg) html += `<div class="last-op-row"><div><span class="chip live" style="padding:1px 7px"><i class="dot"></i>live</span> <span class="mono note">${PHASES[sg.p].name} since ${stamp(sg.s, false)}</span></div></div>`;
  }
  return html;
}
// rolloutPath is the root session file on disk — which folder (and which copy) this session is read
// from — as the request card's last row, full width under both columns.
function rolloutPath() {
  const r = root();
  return `<dl class="session-summary rollout-path"><div><dt>Log file</dt><dd><span class="path" title="${esc(r.file || '')}">${esc(r.file || '—')}</span></dd></div></dl>`;
}
function sessionPage() {
  const m = current(), r = root(), firstUser = firstRequest(r);
  const status = m.live ? `<span class="chip live"><i class="dot"></i>Live · turn in progress</span>` : `<span class="chip">${icon('check', true)}Completed · ${r.turns.length} turns</span>`;
  // the phase legend in two rows by kind: what the agent did, then waiting, overhead and gaps
  const legend = kinds => PHASE_ORDER.filter(k => k !== 'idle' && kinds.includes(PHASES[k].kind)).map(k => `<span class="row">${swatch(k)}${PHASES[k].name}</span>`).join('');
  const railLegend = LIFECYCLE_ORDER.filter(k => LIFECYCLES[k].work).map(k => `<span class="row">${railSwatch(LIFECYCLES[k].color)}${LIFECYCLES[k].name}</span>`).join('');
  return `<section class="page-heading"><div><div class="eyebrow"><span class="source-tag source-${sourceOf(m)}">${esc(sourceName(m))}</span> Session ${esc(m.id.slice(0, 8))} · ${esc(m.model || '?')} · ${esc(sourceName(m))} ${esc(m.cli || '?')}</div><h1>${esc(m.title)}</h1><p class="subtitle">${icon('repo', true)}${esc(m.cwd)}${m.branch ? `<span>·</span>${esc(m.branch)}` : ''}<span>·</span>${stamp(m.started)} — ${stamp(m.ended)} ${esc(TZ)}</p></div><div class="heading-actions">${status}<label class="chip">Refresh <select class="select compact" id="intervalSelect">${[[30, '30s'], [60, '1 min'], [120, '2 min'], [300, '5 min']].map(([v, l]) => `<option value="${v}" ${state.interval === v ? 'selected' : ''}>${l}</option>`).join('')}</select></label><button class="btn" data-action="refresh">${icon('refresh', true)}Refresh now</button></div></section>
<section class="request-card" aria-label="Recorded user request"><div class="request-icon">${icon('target')}</div><div class="request-copy"><div class="answer-head"><span class="eyebrow">First user message · ${firstUser ? stamp(firstUser.t) : 'not recorded'}</span></div><p class="prose-block scroll-fade">${esc(firstUser?.text || '—')}</p>${sessionSummary()}</div><div class="recorded-state"><div id="lastRecorded">${lastState()}</div></div>${rolloutPath()}</section>
<div class="metrics-body"><div id="sessionMetrics">${metricsHTML()}</div>
<section class="card" aria-labelledby="siTitle"><div class="card-head"><div><h2 id="siTitle">${esc(INSIGHT_TEXT.sessionCard.title)}</h2><p>${esc(INSIGHT_TEXT.sessionCard.subtitle)}</p></div><span class="scope">${esc(INSIGHT_TEXT.page.title)}</span></div><div id="sessionInsights" class="session-insights"><p class="muted-note">${esc(INSIGHT_TEXT.states.loading)}</p></div></section>${lifecycleRingHTML(m)}</div>
<section class="card timeline-card" aria-labelledby="timelineTitle"><div class="card-head"><div><h2 id="timelineTitle">Session timeline</h2><p><span id="totalOps">${m.totals.ops}</span> operations · ${m.groups.length} retry groups · <span id="laneCount">${m.lanes.length - 1} sub-agent lanes</span> · ${m.totals.user_messages} user messages${m.totals.failed_ops ? ` · <span title="failed steps: tests, builds, releases, infra, edits and scripts with a non-zero exit">${m.totals.failed_ops} failed</span>` : ''}${m.totals.query_misses ? ` · <span title="reads, searches, listings and probes that answered with a non-zero exit; recorded as failed by the harness, not counted as failures">${m.totals.query_misses} query misses</span>` : ''}</p></div><div class="actions"><button class="btn ghost" data-action="fit" title="Show the entire session">${icon('expand', true)}Fit all</button><button class="btn ghost ${state.follow ? 'active' : ''}" data-action="follow" id="followBtn" aria-pressed="${state.follow}">${icon('latest', true)}Follow latest</button></div></div>
<div class="overview-section"><div class="overview-heading"><strong id="overviewDuration">${fmt(m.ended - m.started)} overview · ${MAIN_THREAD}${m.lanes.length > 1 ? ' · sub-agent activity' : ''} · errors below · SDLC stages at the foot</strong><span>Drag to select a window · handles resize it</span><span class="mono" id="overviewRange"></span></div><div id="overview" class="overview" aria-label="Session overview. Drag to select a time window."><svg id="overviewSvg" aria-hidden="true"></svg><div class="brush-shade" id="shadeLeft"></div><div class="brush-shade" id="shadeRight"></div><div class="brush" id="brush"><div class="brush-handle left" data-handle="start" tabindex="0" role="slider" aria-label="Visible window start"></div><div class="brush-handle right" data-handle="end" tabindex="0" role="slider" aria-label="Visible window end"></div></div></div><div class="overview-lc" id="overviewLc" aria-label="SDLC stage over time"></div><div class="overview-axis" id="overviewAxis"></div><div class="legend legend-phases" id="legend"><div class="legend-group"><span class="row legend-caption">Activity · fill</span>${legend(['model', 'work'])}</div><div class="legend-group">${legend(['wait', 'overhead', 'unknown'])}</div><div class="legend-group" id="legendLifecycle"><span class="row legend-caption">Lifecycle stage · band above each lane, strip under the overview</span>${railLegend}</div></div></div>
<div class="timeline-toolbar"><div class="zoom-group"><div class="zoom-presets" aria-label="Visible time window">${[[0, 'All'], [86400000, '24h'], [21600000, '6h'], [3600000, '1h'], [900000, '15m'], [300000, '5m']].map(([n, l]) => `<button data-action="preset" data-span="${n}">${l}</button>`).join('')}</div><div class="zoom-step" aria-label="Zoom controls"><button data-action="zoom" data-dir="-1" aria-label="Zoom out">−</button><span class="zoom-caption" id="zoomCaption"></span><button data-action="zoom" data-dir="1" aria-label="Zoom in">+</button></div></div><div class="toolbar-right"><label><input id="showGroups" type="checkbox" ${state.groups ? 'checked' : ''}>Retry groups</label><button class="btn small ghost" data-action="expand-all">Expand all lanes</button><button class="btn small ghost" data-action="collapse-all">Collapse</button></div></div>
<div class="range-bar"><span class="range-label" id="rangeLabel"></span><div class="nav-arrows"><button class="btn icon-only" data-action="pan" data-dir="-1" aria-label="Move to earlier time">${icon('left', true)}</button><button class="btn icon-only" data-action="pan" data-dir="1" aria-label="Move to later time">${icon('right', true)}</button></div></div>
<div class="timeline-plot" id="plot" tabindex="0" role="group" aria-label="Interactive session timeline"><svg id="chartSvg" aria-hidden="true"></svg><div class="agent-scroll" id="agentScroll" hidden><svg id="agentSvg" aria-hidden="true"></svg></div></div>

<div class="chart-footer"><span class="chart-note" id="chartNote"></span><span class="chart-shortcuts"><kbd>+</kbd> <kbd>−</kbd> zoom · <kbd>←</kbd> <kbd>→</kbd> pan · <kbd>Home</kbd> fit · drag to pan</span></div><div class="legend legend-marks" id="legendMarks"><span class="row"><svg width="12" height="12">${userGlyph(6, 0, MARKS.user_message.color, .85)}</svg>User message</span><span class="row"><svg width="12" height="12"><circle cx="6" cy="6" r="5" fill="#4aa65f"/></svg>Final answer</span><span class="row"><svg width="14" height="12"><rect x="0" y="5" width="14" height="3" rx="1.5" fill="#c9a15c"/></svg>Background process</span><span class="row"><svg width="12" height="12"><circle cx="6" cy="6" r="5.5" fill="#4aa65f"/><path d="M3 6l2 2 4-4" stroke="#0f1b23" stroke-width="1.6" fill="none"/></svg>Turn completed</span><span class="row"><svg width="12" height="12"><circle cx="6" cy="6" r="5.5" fill="#d76368"/><path d="M3.5 3.5l5 5m0-5l-5 5" stroke="#0f1b23" stroke-width="1.6"/></svg>Interrupted</span><span class="row"><svg width="12" height="12"><circle cx="6" cy="6" r="5" fill="#22313d" stroke="#7d8fa1" stroke-width="1.5"/><path d="M2.5 9.5l7-7" stroke="#7d8fa1" stroke-width="1.5"/></svg>Never closed</span><span class="row"><svg width="14" height="12"><rect x="0" y="1" width="14" height="5" rx="1" fill="${LIFECYCLES.implement.color}" opacity=".35"/><rect x="0" y="5" width="14" height="2" fill="${LIFECYCLES.implement.color}"/><rect x="0" y="8" width="14" height="4" rx="1" fill="#22313d"/></svg>Stage band (the SDLC stage the time served, above the raw fill; colours above)</span><span class="row"><svg width="12" height="12"><path d="M2 2l8 8m0-8l-8 8" stroke="#e5484d" stroke-width="2.4" stroke-linecap="round"/></svg>Tool failure (non-zero exit)</span><span class="row"><svg width="12" height="12"><path d="M2 2l8 8m0-8l-8 8" stroke="#f0a742" stroke-width="2.4" stroke-linecap="round"/></svg>LLM failure: invalid tool call / broken exec script</span></div></section>
<div class="analysis-grid"><section class="card" id="breakdownCard" aria-labelledby="breakdownTitle"><div class="card-head"><div><h2 id="breakdownTitle">Time in this window</h2><p id="breakdownScope"></p></div><span class="scope" id="breakdownScopeChip">Main thread · exclusive</span></div><div id="breakdownBody" class="breakdown-body"></div><div class="panel-foot">${icon('info', true)}Sub-agent time runs in parallel and is listed separately. Gaps are unknown, not idle.</div></section>
<section class="card fill" id="operationsCard" aria-labelledby="operationsTitle"><div class="card-head"><div><h2 id="operationsTitle">Operations in view</h2><p id="operationCount"></p></div><span class="scope">All lanes</span></div><div class="operation-tools"><select class="select" id="phaseSelect" aria-label="Filter by phase"><option value="all">All phases</option>${PHASE_ORDER.map(k => `<option value="${k}">${PHASES[k].name}</option>`).join('')}</select><select class="select" id="laneSelect" aria-label="Filter by lane"><option value="all">All lanes</option>${m.lanes.map(l => `<option value="${esc(l.id)}">${esc(l.path)}</option>`).join('')}</select><select class="select" id="sortSelect" aria-label="Sort operations"><option value="longest">Longest in window</option><option value="latest">Latest first</option><option value="earliest">Earliest first</option><option value="failed">Failed first</option></select><label class="row chip" style="cursor:pointer"><input type="checkbox" id="failedOnly" ${state.failedOnly ? 'checked' : ''}>Failures only</label></div><div id="roleFilter"></div><div class="operation-list scroll-fade" id="operationList"></div></section></div>
<div class="analysis-grid"><section class="card" aria-labelledby="convTitle"><div class="card-head"><div><h2 id="convTitle">Conversation</h2><p>User messages, questions and final answers, verbatim. Select one to jump there.</p></div><span class="scope">${m.totals.user_messages} inputs</span></div><div class="conv scroll-fade" id="conversation"></div></section>
<section class="card" aria-labelledby="agentsTitle"><div class="card-head"><div><h2 id="agentsTitle">Agents</h2><p>One lane per thread. Active time = the thread's own turns.</p></div><span class="scope">${m.lanes.length} lanes</span></div><div class="agents-wrap scroll-fade"><table class="agents-table" id="agentsTable"></table></div></section></div>
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
  for (const mk of r.markers) if (mk.kind === 'user_message') { const x = (mk.t - m.started) / span * W; html += `<rect x="${x.toFixed(1)}" y="0" width="1.5" height="6" fill="${MARKS.user_message.color}"/>`; }
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
  for (const l of m.lanes) {
    for (const o of l.ops) {
      if (!failure(o) || o.background) continue;
      const i = clamp(Math.floor((o.start - m.started) / bin), 0, N - 1);
      if (isLlmErr(o)) llm[i]++;
      else tool[i]++;
      if (names[i].length < 6) names[i].push(`${stamp(o.start, false)} ${isLlmErr(o) ? 'LLM' : 'exit ' + (o.exit ?? '?')} · ${o.title.slice(0, 60)}`);
    }
  }
  const y0 = H + SUB;
  html += `<rect x="0" y="${y0}" width="${W}" height="${ERR}" fill="#1a1f2a"/>`;
  for (let i = 0; i < N; i++) { const t = tool[i], u = llm[i]; if (!t && !u) continue; const ht = t ? Math.max(3, Math.min(ERR - 1, 3 + 2 * t)) : 0, hu = u ? Math.max(3, Math.min(ERR - 1, 3 + 2 * u)) : 0; const tip = `<title>${esc(`${t} tool failure${t === 1 ? '' : 's'}, ${u} LLM failure${u === 1 ? '' : 's'}\n` + names[i].join('\n'))}</title>`; if (t) html += `<rect x="${(i * 4).toFixed(1)}" y="${(y0 + ERR - ht).toFixed(1)}" width="4.05" height="${ht}" fill="#e05252" opacity="${(.6 + .4 * Math.min(1, t / 4)).toFixed(2)}">${tip}</rect>`; if (u) html += `<rect x="${(i * 4).toFixed(1)}" y="${(y0 + ERR - ht - hu).toFixed(1)}" width="4.05" height="${hu}" fill="#f0a742" opacity=".95">${tip}</rect>`; }
  el.setAttribute('viewBox', `0 0 ${W} ${H + SUB + ERR}`); el.setAttribute('preserveAspectRatio', 'none'); el.innerHTML = html;
  const lcEl = $('#overviewLc'); if (lcEl) lcEl.innerHTML = overviewLcHTML(m, r);
  $('#overviewAxis').innerHTML = [0, 1 / 3, 2 / 3, 1].map(n => `<span>${stamp(m.started + span * n)}</span>`).join('');
  updateBrush();
}
// overviewLcHTML builds the SDLC-stage band under the overview: the main thread's lifecycle
// partition across the whole session as one 20px strip. A work stage is drawn in its colour with
// its name (or first letter, where it fits) in white; user-input time (waiting for the user) is
// grey; other pass-through (model output, worker waits, gaps) is a dim neutral. HTML segments,
// not a stretched SVG, so the labels stay crisp. Rebuilt whenever renderOverview runs.
function overviewLcHTML(m, r) {
  const span = (m.ended - m.started) || 1;
  const runs = [];
  for (const sg of r.segments) { const lc = lifecycleOf(sg); const last = runs[runs.length - 1]; if (last && last.lc === lc && Math.abs(last.e - sg.s) < 2) last.e = sg.e; else runs.push({ lc, s: sg.s, e: sg.e }); }
  let out = '';
  for (const run of runs) {
    const left = (run.s - m.started) / span * 100, w = (run.e - run.s) / span * 100;
    if (w <= 0) continue;
    const def = LIFECYCLES[run.lc], work = def && def.work, user = run.lc === 'wait_user';
    const fill = work ? def.color : user ? '#8a97a2' : '#22323f';
    const name = work ? def.short : user ? 'user' : '';
    // pick the widest label that fits this run (≈12px per 1% of a ~1200px band); else a letter
    const text = name ? (w >= (name.length + 1) * 0.6 ? name : w >= 1.1 ? name[0] : '') : '';
    const cls = 'lc-band-seg' + (work ? ' work' : user ? ' user' : ' idle');
    const title = def ? `${def.name} · ${fmt(run.e - run.s)}` : '';
    out += `<div class="${cls}" style="left:${left.toFixed(3)}%;width:${w.toFixed(3)}%;${work ? 'background:' + fill : 'background:' + fill}" data-lc="${run.lc}" title="${esc(title)}">${text ? `<span>${esc(text)}</span>` : ''}</div>`;
  }
  return out;
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
    if (state.hiddenLanes.has(l.id) || (!laneInWindow(l) && state.lane !== l.id)) continue;
    rows.push({ kind: 'lane', lane: l, h: l.depth === 0 ? 58 : 50 });
    if (l.depth === 0) { rows.push({ kind: 'markers', lane: l, h: 24 }); if (state.groups && m.groups.length) rows.push({ kind: 'groups', h: 26 }); }
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
  const host = $('#plot'), W = Math.max(280, host.clientWidth), mobile = W < 560, L = mobile ? 88 : 210, R = mobile ? 10 : 20, P = W - L - R, span = state.b - state.a, x = t => L + (t - state.a) / span * P, top = 30;
  const rows = laneRows();
  // the root block (lane, markers, groups, its phase rows) is one fixed SVG; sub-agent rows are a
  // second SVG in a scrolling box capped at ten lanes, so a hundred workers do not push the page
  const split = rows.findIndex(r => r.lane && r.lane.depth > 0);
  const head = split < 0 ? rows : rows.slice(0, split), agents = split < 0 ? [] : rows.slice(split);
  let H = top + sum(head.map(r => r.h)) + 8;
  const HA = agents.length ? sum(agents.map(r => r.h)) + 4 : 0;
  geometry = { W, L, R, P, span, x, rows, top, H, mobile };
  const bw = 3; // px per bucket
  // defs are shared by both SVGs (url(#id) resolves document-wide); the clip is tall enough for either
  let html = `<defs><clipPath id="plotClip"><rect x="${L}" y="0" width="${P}" height="${Math.max(H, HA)}"/></clipPath><pattern id="gapHatch" width="6" height="6" patternUnits="userSpaceOnUse" patternTransform="rotate(35)"><rect width="6" height="6" fill="#22313d"/><path d="M0 0v6" stroke="#7d8fa1" stroke-width="1.3"/></pattern><pattern id="waitHatch" width="5" height="5" patternUnits="userSpaceOnUse" patternTransform="rotate(35)"><rect width="5" height="5" fill="#48aa8c"/><path d="M0 0v5" stroke="#236854" stroke-width="1"/></pattern></defs>`;
  const ticks = niceTick(span, P);
  const guides = (y1, y2) => {
    let g = '';
    for (let t = Math.ceil(state.a / ticks) * ticks; t <= state.b; t += ticks) g += `<line x1="${x(t)}" y1="${y1}" x2="${x(t)}" y2="${y2}" stroke="#2b3c48" stroke-width=".65"/>`;
    for (const tn of root().turns) if (tn.start >= state.a && tn.start <= state.b) g += `<line x1="${x(tn.start)}" y1="${y1}" x2="${x(tn.start)}" y2="${y2}" stroke="#ec838d" stroke-width="1" stroke-dasharray="2 4" opacity=".55"/>`;
    if (m.live && m.now >= state.a && m.now <= state.b) g += `<line x1="${x(m.now)}" x2="${x(m.now)}" y1="${y1}" y2="${y2}" stroke="#5fe0a0" stroke-width="1.2" opacity=".8"/>`;
    else if (state.b >= m.ended - 1 && !m.live) g += `<line x1="${x(m.ended) - 1}" x2="${x(m.ended) - 1}" y1="${y1}" y2="${y2}" stroke="#a5c5df" stroke-width="1.2"/>`;
    return g;
  };
  html += guides(26, H - 4);
  for (let t = Math.ceil(state.a / ticks) * ticks; t <= state.b; t += ticks) { const xx = x(t); if (xx < L + 12 || xx > W - R - 14) continue; const d = new Date(t), midnight = d.getHours() === 0 && d.getMinutes() === 0; const lbl = span > 86400e3 * 2 || midnight ? stamp(t) : ticks < 60e3 ? stampS(t).slice(-8) : stamp(t, false); html += `<text class="axis-label" x="${xx}" y="17" text-anchor="middle" ${midnight ? 'font-weight="700"' : ''}>${lbl}</text>`; }
  const drawRows = (list, y0) => {
  let html = '';
  let y = y0;
  const fillFor = p => p === 'no_telemetry' ? 'url(#gapHatch)' : p === 'wait_worker' ? 'url(#waitHatch)' : PHASES[p]?.color || '#888';
  const opacityFor = alphaOf;
  list.forEach((row, ri) => {
    const rh = row.h, bg = ri % 2 ? '#1c2a35' : '#20303c';
    html += `<rect x="0" y="${y}" width="${W}" height="${rh}" fill="${bg}" opacity=".75"/>`;
    if (row.kind === 'lane') {
      const l = row.lane, sel = state.lane === l.id, indent = Math.min(l.depth, 3) * (mobile ? 6 : 10), name = l.depth === 0 ? MAIN_THREAD : l.path.split('/').pop();
      const exp = state.expanded.has(l.id);
      // text budget: the label column minus the chevron gutter and, on desktop, the card glyph
      const textW = L - 24 - indent - (mobile ? 0 : 22);
      const nameChars = Math.floor(textW / 7.5), subChars = Math.floor(textW / 6.2);
      const caption = l.depth === 0
        ? (mobile ? `${l.turns.length}t · ${l.ops.length}` : trunc(`${l.turns.length} turns · ${l.model || ''}`, subChars))
        : mobile ? trunc(l.nickname || l.model || '', subChars) : trunc(agentCaption(l), subChars);
      const chevron = exp ? `m${8 + indent} ${y + rh / 2 - 3} 3.5 4 3.5-4` : `m${9 + indent} ${y + rh / 2 - 4} 4 4-4 4`;
      html += `<g data-lane="${esc(l.id)}" class="lane-head"><rect x="0" y="${y}" width="${L - 4}" height="${rh}" fill="transparent"/><path d="${chevron}" fill="none" stroke="#c7d5de" stroke-width="1.6"/>`;
      html += `<text class="lane-label ${sel ? 'selected' : ''}" x="${20 + indent}" y="${y + rh / 2 - 2}">${esc(trunc(name, nameChars))}</text>`;
      html += `<text class="lane-sub" x="${20 + indent}" y="${y + rh / 2 + 11}">${esc(caption)}</text>`;
      // the agent card (prompt, model, final answer) has its own glyph; on a phone the Agents table opens it
      if (!mobile) {
        const cx = L - 15, cy = y + rh / 2;
        html += `<g class="lane-card-btn" data-action="lane-card" data-lane="${esc(l.id)}"><rect x="${L - 26}" y="${y}" width="22" height="${rh}" fill="transparent"/><circle cx="${cx}" cy="${cy}" r="7" fill="none" stroke="#7d8fa1" stroke-width="1.3"/><text x="${cx}" y="${cy + 3.5}" text-anchor="middle" font-size="10" font-weight="700" fill="#c7d5de">i</text></g>`;
      }
      html += `</g>`;
      html += `<g clip-path="url(#plotClip)">`;
      if (l.depth > 0) { // lifetime box: from spawn (▶) to the last turn end / now
        const { t0, t1 } = laneSpan(l);
        if (t1 > state.a && t0 < state.b) {
          const xa = x(Math.max(t0, state.a)), xb = x(Math.min(t1, state.b));
          html += `<rect x="${xa.toFixed(1)}" y="${y + BAND - 1}" width="${Math.max(2, xb - xa).toFixed(1)}" height="${rh - BAND - 2}" rx="4" fill="#233443" stroke="#4f6d80" stroke-width="1"/>`;
          // idle gaps between turns: dashed centre line (waiting for the parent)
          const cy0 = y + BAND + 1 + (rh - BAND - 6) / 2;
          html += `<line x1="${xa.toFixed(1)}" x2="${xb.toFixed(1)}" y1="${cy0}" y2="${cy0}" stroke="#4f6d80" stroke-width="1" stroke-dasharray="2 3"/>`;
        }
        for (const iv of l.active || []) { if (iv.e <= state.a || iv.s >= state.b) continue; html += `<rect x="${x(Math.max(iv.s, state.a))}" y="${y + BAND}" width="${Math.max(1, x(Math.min(iv.e, state.b)) - x(Math.max(iv.s, state.a)))}" height="${rh - BAND - 4}" fill="#2e4655" rx="3"/>`; }
      }
      // Fill = the raw exclusive partition (teal is real model time): what ran. The stage band
      // above the fill = the lifecycle partition of the same time: which SDLC stage it served.
      const top0 = y + BAND + 1, fh = rh - BAND - 6; // fill box under the stage band
      const segsVis = l.segments.filter(sg => sg.e > state.a && sg.s < state.b && !(l.depth > 0 && sg.p === 'idle'));
      const { runs, per } = bucketRuns(segsVis, state.a, state.b, P, bw, it => it.p);
      const lastRun = runs[runs.length - 1];
      for (const run of runs) {
        const xa = L + run.start * bw, ww = Math.max(1, (run.end - run.start) * bw - (run.end - run.start > 1 ? .6 : 0)), p = run.key;
        const ta = state.a + run.start * per, tb = state.a + run.end * per;
        // the last block of a live lane with no telemetry yet: the agent is generating and nothing
        // has been recorded since the last event — an in-progress wait, not an orphaned gap
        const pending = l.live && p === 'no_telemetry' && run === lastRun;
        html += `<g class="mark" data-stage="1" data-lane="${esc(l.id)}" data-ta="${Math.round(ta)}" data-tb="${Math.round(tb)}" data-phase="${p}"${pending ? ' data-pending="1"' : ''}><rect x="${xa.toFixed(1)}" y="${top0}" width="${ww.toFixed(1)}" height="${fh}" rx="${ww > 4 ? 2 : 0}" fill="${fillFor(p)}"/>`;
        if (pending) {
          if (ww > 15) html += hourglass(xa + 9, top0 + fh / 2, '#dbe6ee');
          if (ww > 120) { const label = `No telemetry yet (in progress) · ${fmt(tb - ta)}`; html += `<text class="op-label light" x="${xa + 19}" y="${top0 + fh / 2 + 3.5}">${esc(label.slice(0, Math.floor((ww - 26) / 5.8)))}</text>`; }
        } else if (ww > 52 && p !== 'wait_user' && p !== 'idle' && p !== 'llm') { const label = `${PHASES[p].short} ${fmt(tb - ta)}`; html += `<text class="op-label ${p === 'code' || p === 'compaction' ? 'light' : ''}" x="${xa + 5}" y="${top0 + fh / 2 + 3.5}">${esc(label.slice(0, Math.floor((ww - 8) / 5.8)))}</text>`; }
        html += `</g>`;
      }
      // stage band: the SDLC stage the time served (the second partition), one tinted run per
      // stage with its baseline and label. Pass-through time (waits, compaction, telemetry gaps,
      // model output of a tool-less turn) is not a stage and leaves the band empty; the fill
      // below still shows what it was.
      const lcRuns = bucketRuns(segsVis, state.a, state.b, P, bw, it => lifecycleOf(it)).runs;
      let lastLabelEnd = -1;
      for (const run of lcRuns) {
        const def = LIFECYCLES[run.key];
        if (!def || !def.work) continue;
        const xa = L + run.start * bw;
        const ww = Math.max(1, (run.end - run.start) * bw - (run.end - run.start > 1 ? .6 : 0));
        const ta = state.a + run.start * per;
        const tb = state.a + run.end * per;
        const c = def.color, ly = y + BAND - 1;
        html += `<g class="mark" data-stage="1" data-band="1" data-lane="${esc(l.id)}" data-ta="${Math.round(ta)}" data-tb="${Math.round(tb)}" data-lc="${run.key}"><title>${def.name} · ${fmt(tb - ta)}</title><rect x="${xa.toFixed(1)}" y="${y + 2}" width="${ww.toFixed(1)}" height="${BAND - 4}" rx="2" fill="${c}" opacity=".22"/><rect x="${xa.toFixed(1)}" y="${ly - 2}" width="${ww.toFixed(1)}" height="2" fill="${c}"/>`;
        if (ww >= 14) {
          const label = `${def.short} ${fmt(tb - ta)}`;
          const lw = label.length * 6 + 8;
          if (ww >= lw && xa >= lastLabelEnd) {
            html += `<text x="${xa + 4}" y="${y + BAND - 8}" font-size="11" font-weight="700" fill="${c}">${esc(label)}</text>`;
            lastLabelEnd = xa + lw;
          }
        }
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
      if (l.depth > 0) for (const mk of m.agentMarks.get(l.id) || []) { if (mk.t < state.a || mk.t > state.b) continue; const xx = x(mk.t); if (mk.kind === 'agent_started') html += `<g class="marker" data-mk="${esc(`${mk.lane}:${mk.t}:${mk.kind}`)}"><path d="M${xx - 5} ${y + BAND}l10 ${(rh - BAND - 4) / 2}-10 ${(rh - BAND - 4) / 2}z" fill="#86d0b9" stroke="#0f1b23" stroke-width="1"/></g>`; else if (mk.kind === 'agent_completed') html += `<rect class="marker" data-mk="${esc(`${mk.lane}:${mk.t}:${mk.kind}`)}" x="${xx - 1}" y="${y + BAND}" width="2" height="${rh - BAND - 4}" fill="#86d0b9" opacity=".5"/>`; else if (mk.kind === 'agent_interacted') html += `<rect class="marker" data-mk="${esc(`${mk.lane}:${mk.t}:${mk.kind}`)}" x="${xx - 1}" y="${y + rh - 9}" width="2" height="6" fill="#c7d5de" opacity=".8"/>`; else if (mk.kind === 'agent_interrupted') html += `<path class="marker" data-mk="${esc(`${mk.lane}:${mk.t}:${mk.kind}`)}" d="M${xx - 4} ${y + 6}l8 8m0-8l-8 8" stroke="#d76368" stroke-width="2"/>`; }
      for (const o of l.ops) { if (!o.background || o.end <= state.a || o.start >= state.b) continue; const xa = x(Math.max(o.start, state.a)), ww = Math.max(2, x(Math.min(o.end, state.b)) - xa); html += `<g class="mark" data-op="${esc(o.id)}" data-lane="${esc(l.id)}" data-ta="${o.start}" data-tb="${o.end}" data-phase="${o.phase}"><rect x="${xa.toFixed(1)}" y="${y + rh - 5}" width="${ww.toFixed(1)}" height="3" rx="1.5" fill="#c9a15c" opacity=".9"/><rect x="${xa.toFixed(1)}" y="${y + rh - 8}" width="${ww.toFixed(1)}" height="8" fill="transparent"/></g>`; }
      // Tool failures (non-zero exit) render as a bold red ×. LLM failures (invalid tool call /
      // broken exec script) are shown separately as the orange × llm_error marker in the row above,
      // so they are skipped here to avoid a duplicate glyph.
      { let lastX = -99;
        const fy = y + rh - 4;
        for (const o of l.ops) {
          if (!failure(o) || o.background || o.start < state.a || o.start > state.b) continue;
          if (o.kind === 'exec-script-error' || baseKind(o.kind) === 'llm-invalid-args') continue;
          const xx = x(o.start);
          const cy = fy - 4;
          const hit = `<rect x="${(xx - 6).toFixed(1)}" y="${fy - 11}" width="12" height="13" fill="transparent"/>`;
          if (xx - lastX < 4) {
            html += `<rect class="mark" data-op="${esc(o.id)}" data-lane="${esc(l.id)}" data-ta="${o.start}" data-tb="${o.end}" data-phase="${o.phase}" x="${(xx - 1).toFixed(1)}" y="${fy - 8}" width="2" height="8" fill="#e5484d"/>`;
            continue;
          }
          lastX = xx;
          html += `<g class="mark" data-op="${esc(o.id)}" data-lane="${esc(l.id)}" data-ta="${o.start}" data-tb="${o.end}" data-phase="${o.phase}"><path d="M${(xx - 4).toFixed(1)} ${cy - 4}l8 8m0-8l-8 8" stroke="#0f1b23" stroke-width="3.6" stroke-linecap="round"/><path d="M${(xx - 4).toFixed(1)} ${cy - 4}l8 8m0-8l-8 8" stroke="#e5484d" stroke-width="2.2" stroke-linecap="round"/>${hit}</g>`;
        }
      }
      if (l.live) { const xx = x(Math.min(m.now, state.b)); if (m.now >= state.a) html += `<g class="marker" data-turn="${esc(l.id + ':' + (l.turns[l.turns.length - 1] || {}).id)}"><circle cx="${xx}" cy="${y + BAND + 1 + (rh - BAND - 6) / 2}" r="4" fill="#5fe0a0"><animate attributeName="r" values="3.5;6;3.5" dur="1.6s" repeatCount="indefinite"/></circle></g>`; }
      html += `</g>`;
    } else if (row.kind === 'markers') {
      html += `<text class="lane-sub" x="${mobile ? 14 : 20}" y="${y + 15}">markers</text><g clip-path="url(#plotClip)">`;
      const mks = row.lane.markers.filter(k => MARKS[k.kind] && !k.kind.startsWith('agent_') && k.t >= state.a && k.t <= state.b).sort((p, q) => p.t - q.t);
      const clusters = []; for (const mk of mks) { const xx = x(mk.t); const c = clusters[clusters.length - 1]; if (c && xx - c.x1 < 16 && xx - c.x0 < 48) { c.items.push(mk); c.x1 = xx; } else clusters.push({ x0: xx, x1: xx, items: [mk] }); }
      const glyph = (mk, xx) => { const c = MARKS[mk.kind].color; switch (MARKS[mk.kind].glyph) { case 'user': return userGlyph(xx, y + 4, c); case 'q': return `<circle cx="${xx}" cy="${y + 11}" r="6" fill="none" stroke="${c}" stroke-width="2"/><text x="${xx}" y="${y + 14.5}" text-anchor="middle" font-size="10" font-weight="700" fill="${c}">?</text>`; case 'check': return `<circle cx="${xx}" cy="${y + 11}" r="5.5" fill="${c}"/><path d="M${xx - 2.8} ${y + 11}l2 2 3.6-4" stroke="#0f1b23" stroke-width="1.6" fill="none"/>`; case 'x': return `<path d="M${xx - 4} ${y + 7}l8 8m0-8l-8 8" stroke="${c}" stroke-width="2"/>`; case 'diamond': return `<path d="M${xx} ${y + 6}l5 5-5 5-5-5z" fill="${c}" opacity=".9"/>`; case 'plan': return `<rect x="${xx - 4}" y="${y + 7}" width="8" height="8" rx="1.5" fill="${c}"/>`; case 'skill': return `<path d="M${xx - 4} ${y + 6}h8v11l-4 -3-4 3z" fill="${c}"/>`; case 'sys': return `<path d="M${xx} ${y + 5}l5 6-5 6-5-6z" fill="none" stroke="${c}" stroke-width="1.5"/>`; case 'result': return `<path d="M${xx + 4} ${y + 5}l-8 6 8 6z" fill="${c}"/>`; default: return `<rect x="${xx - 1}" y="${y + 6}" width="2" height="10" fill="${c}"/>`; } };
      for (const cl of clusters) {
        if (cl.items.length === 1) { const mk = cl.items[0]; html += `<g class="marker" data-mk="${esc(`${mk.lane}:${mk.t}:${mk.kind}`)}">${glyph(mk, cl.x0)}<rect x="${cl.x0 - 7}" y="${y}" width="14" height="${rh}" fill="transparent"/></g>`; continue; }
        // cluster pill: colour of the most important kind (user > question > final > other)
        const pri = ['user_message', 'question', 'final_answer', 'interrupted']; const lead = cl.items.slice().sort((p, q) => (pri.indexOf(p.kind) + 1 || 9) - (pri.indexOf(q.kind) + 1 || 9))[0]; const users = cl.items.filter(k => k.kind === 'user_message').length; const xm = (cl.x0 + cl.x1) / 2, wpill = Math.max(users ? 34 : 22, cl.x1 - cl.x0 + 14);
        html += `<g class="mark marker-cluster" data-stage="1" data-lane="${esc(row.lane.id)}" data-ta="${cl.items[0].t}" data-tb="${cl.items[cl.items.length - 1].t + 1}" data-phase="cluster"><rect x="${xm - wpill / 2}" y="${y + 4}" width="${wpill}" height="14" rx="7" fill="${MARKS[lead.kind].color}" opacity=".85"/>${users ? userGlyph(xm - wpill / 2 + 8, y + 5, '#0f1b23', .8) : ''}<text x="${users ? xm + 5 : xm}" y="${y + 14.5}" text-anchor="middle" font-size="10.5" font-weight="700" fill="#0f1b23">${users ? users + (cl.items.length > users ? '+' + (cl.items.length - users) : '') : cl.items.length}</text></g>`;
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
      html += `<g data-phase-row="${p}" data-lane="${esc(l.id)}"><rect x="0" y="${y}" width="${L - 4}" height="${rh}" fill="transparent"/><rect x="${indent}" y="${y + rh / 2 - 4}" width="8" height="8" rx="2.5" fill="${PHASES[p].color}"/><text class="lane-label ${sel ? 'selected' : ''}" x="${indent + 14}" y="${y + rh / 2 + 4}" style="font-size:12px">${esc(mobile ? PHASES[p].short : PHASES[p].name)}</text></g><g clip-path="url(#plotClip)">`;
      const segs = l.segments.filter(sg => sg.p === p && sg.e > state.a && sg.s < state.b);
      if (span / P < 800) { // fine zoom: draw each segment (≈ op) individually
        for (const sg of segs) {
          const xa = x(Math.max(sg.s, state.a));
          const ww = Math.max(1.2, x(Math.min(sg.e, state.b)) - xa);
          const o = sg.op ? m.opById.get(sg.op) : null;
          const stroke = o && failure(o) ? 'stroke="#ff8a8a" stroke-width="1.2"' : '';
          const selected = sg.op && sg.op === state.selected ? 'stroke="#eef6fa" stroke-width="2"' : '';
          const label = ww > 40 && o ? `<text class="op-label ${p === 'llm' || p === 'code' ? 'light' : ''}" x="${xa + 4}" y="${y + rh / 2 + 3.5}">${esc(o.title.slice(0, Math.floor((ww - 8) / 5.6)))}</text>` : '';
          html += `<g class="mark" data-op="${esc(sg.op || '')}" data-lane="${esc(l.id)}" data-ta="${sg.s}" data-tb="${sg.e}" data-phase="${p}"><rect x="${xa.toFixed(1)}" y="${y + 4}" width="${ww.toFixed(1)}" height="${rh - 8}" rx="${ww > 4 ? 2 : 0}" fill="${fillFor(p)}" ${stroke} ${selected}/>${label}</g>`;
        }
      } else {
        const { runs, per } = bucketRuns(segs, state.a, state.b, P, bw, () => p);
        for (const run of runs) { const xa = L + run.start * bw, ww = Math.max(1, (run.end - run.start) * bw - .6); html += `<g class="mark" data-stage="1" data-lane="${esc(l.id)}" data-ta="${Math.round(state.a + run.start * per)}" data-tb="${Math.round(state.a + run.end * per)}" data-phase="${p}"><rect x="${xa.toFixed(1)}" y="${y + 4}" width="${ww.toFixed(1)}" height="${rh - 8}" rx="1.5" fill="${fillFor(p)}" opacity="${(.4 + .6 * run.cov).toFixed(2)}"/></g>`; }
      }
      html += `</g>`;
    }
    y += rh;
  });
  return html;
  };
  html += drawRows(head, top);
  let htmlA = agents.length ? guides(0, HA) + drawRows(agents, 0) : '';
  // connectors: spawn time from the parent row to the child row; a child in the scrolling block
  // whose parent is the root gets the line split at the boundary between the two SVGs
  if (!mobile) {
    const laneY = (list, y0) => { let yr = y0; const out = new Map(); for (const row of list) { if (row.kind === 'lane') out.set(row.lane.id, { y: yr, h: row.h }); yr += row.h; } return out; };
    const headY = laneY(head, top), agentY = laneY(agents, 0);
    for (const l of m.lanes.slice(1)) {
      const mk = (m.agentMarks.get(l.id) || []).find(k => k.kind === 'agent_started');
      const c = agentY.get(l.id);
      if (!mk || !c || mk.t < state.a || mk.t > state.b) continue;
      const xx = x(mk.t), line = (y1, y2) => `<line x1="${xx}" y1="${y1}" x2="${xx}" y2="${y2}" stroke="#86d0b9" stroke-width="1" stroke-dasharray="2 3" opacity=".7"/>`;
      const pa = agentY.get(l.parent), ph = headY.get(l.parent);
      if (pa) htmlA += line(pa.y + pa.h - 6, c.y + 6);
      else if (ph) { html += line(ph.y + ph.h - 6, H - 4); htmlA += line(0, c.y + 6); }
    }
  }
  // focus band: a bright, briefly-pulsing column marking where the object a "Show in timeline"
  // click located sits within the full range, so it is obvious what was selected.
  if (state.focus && state.focus.b > state.a && state.focus.a < state.b) {
    const xa = x(Math.max(state.focus.a, state.a)), xb = x(Math.min(state.focus.b, state.b)), ww = Math.max(3, xb - xa);
    const dur = state.focus.b - state.focus.a, label = dur > 0 ? 'focused · ' + fmt(dur, true) : 'here', tab = Math.min(140, Math.max(ww, label.length * 6 + 12));
    const band = (y0, y1) => `<g pointer-events="none"><rect class="focus-band" x="${xa.toFixed(1)}" y="${y0}" width="${ww.toFixed(1)}" height="${(y1 - y0).toFixed(1)}" rx="2"/><line class="focus-edge" x1="${xa.toFixed(1)}" y1="${y0}" x2="${xa.toFixed(1)}" y2="${y1}"/><line class="focus-edge" x1="${(xa + ww).toFixed(1)}" y1="${y0}" x2="${(xa + ww).toFixed(1)}" y2="${y1}"/></g>`;
    html += band(top, H - 4).replace('</g>', `<rect class="focus-tab" x="${xa.toFixed(1)}" y="${top}" width="${tab.toFixed(1)}" height="14" rx="2"/><text class="focus-tab-text" x="${(xa + 5).toFixed(1)}" y="${top + 10.5}">${esc(label)}</text></g>`);
    if (agents.length) htmlA += band(0, HA);
  }
  const svg = $('#chartSvg'); svg.setAttribute('viewBox', `0 0 ${W} ${H}`); svg.style.height = H + 'px'; svg.innerHTML = html;
  const box = $('#agentScroll'), svgA = $('#agentSvg');
  box.hidden = !agents.length;
  svgA.setAttribute('viewBox', `0 0 ${W} ${Math.max(HA, 1)}`); svgA.style.height = HA + 'px'; svgA.innerHTML = htmlA;
  $('#rangeLabel').textContent = spanLabel(state.a, state.b) + ' ' + TZ; $('#zoomCaption').textContent = fmt(span);
  const agentsTotal = m.lanes.length - 1;
  const agentsShown = rows.filter(r => r.kind === 'lane' && r.lane.depth > 0 && laneInWindow(r.lane)).length;
  if ($('#laneCount')) $('#laneCount').textContent = `${agentsTotal} sub-agent lanes` + (agentsShown < agentsTotal ? ` · ${agentsShown} in this window` : '');
  $('#chartNote').innerHTML = icon('info', true) + (span / P >= 800 ? `${fmt(bucketRuns([], state.a, state.b, P, bw, () => 0).per)} per column: fill = dominant phase (teal is LLM time), band above = the lifecycle stage the time served; in expanded rows bar height = coverage. Select a block to frame it (again to zoom in); a turn's end glyph highlights the turn.` : `Fine zoom: expanded rows show individual operations. Select one to inspect the source event.`);
  $$('[data-action="preset"]').forEach(el => { const v = Number(el.dataset.span) || (m.ended - m.started); el.classList.toggle('active', Math.abs(span - v) < 1000); el.disabled = Number(el.dataset.span) > m.ended - m.started; });
  $('[data-action="zoom"][data-dir="-1"]').disabled = span >= m.ended - m.started - 500; $('[data-action="zoom"][data-dir="1"]').disabled = span <= 60500;
  $('[data-action="pan"][data-dir="-1"]').disabled = state.a <= m.started + 1; $('[data-action="pan"][data-dir="1"]').disabled = state.b >= m.ended - 1;
  $('#followBtn').classList.toggle('active', state.follow); $('#followBtn').setAttribute('aria-pressed', state.follow);
  updateBrush();
}
function setWindow(a, b, { manual = true, lower = true } = {}) { const m = current(); const span = clamp(b - a, 60000, m.ended - m.started); a = clamp(a, m.started, m.ended - span); state.a = a; state.b = a + span; state.listShown = LIST_CHUNK; if (manual) state.focus = null; if (manual && state.b < m.ended - 1000) state.follow = false; hideTooltip(); renderTimeline(); if (lower) renderLower(); }
function chooseSpan(span, center = (state.a + state.b) / 2) { const m = current(); span = Math.min(span, m.ended - m.started); setWindow(center - span / 2, center + span / 2); }
function zoom(dir, anchor = .5) { const m = current(), cur = state.b - state.a, total = m.ended - m.started, levels = [60e3, 300e3, 900e3, 3600e3, 10800e3, 21600e3, 43200e3, 86400e3, 172800e3, total].filter(n => n <= total).sort((a, b) => a - b), next = dir > 0 ? [...levels].reverse().find(n => n < cur - 1) : levels.find(n => n > cur + 1); if (!next) return; const t = state.a + cur * anchor; setWindow(t - next * anchor, t + next * (1 - anchor)); }
// zoomToBlock frames a coarse block (a run of columns, a stage-band run) with a small margin; a
// block that already fills the view zooms in on its centre instead, so a second click goes deeper.
function zoomToBlock(ta, tb) {
  const framed = Math.max(120e3, (tb - ta) * 1.3);
  const cur = state.b - state.a;
  chooseSpan(framed < cur * .9 ? framed : Math.max(120e3, cur / 3), (ta + tb) / 2);
}
function pan(dir) { const span = state.b - state.a; setWindow(state.a + dir * span * .6, state.b + dir * span * .6); }
// revealPlot brings the timeline into view only when it is entirely off-screen. If any part is
// already visible it does nothing, so focusing an interval while looking at the timeline never
// scrolls the page out from under the user (the zoom + highlight band already show the result).
function revealPlot() {
  const el = $('#plot'); if (!el) return;
  const r = el.getBoundingClientRect();
  if (r.bottom <= 0 || r.top >= window.innerHeight) el.scrollIntoView({ block: 'start', behavior: 'smooth' });
}
function scrollToTimeline() {
  const el = $('.timeline-card') || $('#plot');
  if (el) el.scrollIntoView({ block: 'start', behavior: 'smooth' });
}
// Show an object in the timeline: select the WHOLE range (brush handles to both ends of the
// session), scroll the range bar fully into view, and highlight where the object sits in it.
// focusInterval highlights [a, b] for a few seconds. An interval already in view keeps the
// window (a click on a turn glyph must not throw the view away); one partly in view is framed;
// one entirely elsewhere is shown against the whole session for orientation.
function focusInterval(a, b) {
  if ($('#inspector').open) $('#inspector').close();
  const m = current();
  state.focus = null; // setWindow(manual) clears focus; set it after so the band survives
  const inView = a >= state.a && b <= state.b;
  const overlaps = b > state.a && a < state.b;
  if (!inView && overlaps) setWindow((a + b) / 2 - Math.max(120e3, (b - a) * 1.3) / 2, (a + b) / 2 + Math.max(120e3, (b - a) * 1.3) / 2, { manual: false });
  else if (!inView) setWindow(m.started, m.ended, { manual: false });
  state.focus = { a, b };
  renderTimeline();
  scrollToTimeline();
  clearTimeout(focusTimer);
  focusTimer = setTimeout(() => { if (state.focus) { state.focus = null; renderTimeline(); } }, 6000);
}

/* ---------- lower panels ---------- */
function renderLower() {
  if (state.page !== 'session' || !$('#breakdownBody')) return;
  const t = windowStats(state.a, state.b);
  // count = the records a click on the row lists (operations, or gaps for interval phases);
  // a row with neither time nor records in the window is not shown
  // share = the row's part of its section's total (the same denominator as the bar), so the
  // rows of a section add up to 100 %; the two sub-agent rows are not a partition and show none
  const pct = (n, total) => (total ? n / total * 100 : 0).toFixed(1) + '%';
  const rows = (entries, total) => entries.filter(([, n, , , , count]) => n > 0 || count == null || count > 0).map(([k, n, def, sel, data, count, sub, cls]) => `<button class="breakdown-item${cls ? ' ' + cls : ''}${sel ? ' active' : ''}" ${data} title="${esc(def.name)}: ${fmt(n, true)}${sub ? ' · ' + esc(sub) : ' of tool time'}${count == null ? '' : ` · ${plural(count, INTERVAL_PHASES.has(k) ? 'interval' : 'operation')} in the list · ${pct(n, total)} of this section`}"><span class="name">${swatchFor(def)}<span class="label">${def.name}</span>${sub ? `<small class="row-sub">${esc(sub)}</small>` : ''}</span><span class="bar-track"><span class="bar-fill" style="width:${total ? clamp(n / total * 100, 0, 100) : 0}%;background:${def.color}"></span></span><span class="count num">${count == null ? '' : count}</span><span class="share num">${count == null ? '' : pct(n, total)}</span><span class="time num">${fmt(n)}</span></button>`).join('');
  const inWindow = opsInWindow(state.a, state.b);
  const countOf = (phase, role) => INTERVAL_PHASES.has(phase) ? intervalRecords(phase).length : inWindow.filter(o => opMatches(o, phase, role, 'all', 'all')).length;
  const countOfLc = lc => INTERVAL_PHASES.has(lc) ? intervalRecords(lc).length : inWindow.filter(o => opMatches(o, 'all', 'all', lc, 'all')).length;
  const countOfSub = (phase, sub) => inWindow.filter(o => opMatches(o, phase, 'all', 'all', sub)).length;
  if ($('#breakdownScopeChip')) $('#breakdownScopeChip').textContent = state.allLanes ? 'All lanes · agent time' : 'Main thread · exclusive';
  $('#breakdownScope').textContent = `${fmt(t.duration)} selected · ${fmt(t.inTurn)} inside turns · sub-agents ${fmt(t.agentMs)} in parallel`;
  const testTotal = Math.max(1, ROLE_ORDER.filter(k => k !== 'remote').reduce((n, k) => n + t.roles[k], 0));
  const src = state.allLanes ? t.allBy : t.by;
  const denom = state.allLanes ? Math.max(1, PHASE_ORDER.reduce((n, k) => n + (k === 'idle' ? 0 : src[k]), 0)) : state.inTurn ? Math.max(1, t.inTurn) : t.duration;
  const byTime = (a, b) => b[1] - a[1] || (b[5] || 0) - (a[5] || 0);
  const sorters = { longest: byTime, records: (a, b) => (b[5] || 0) - (a[5] || 0) || byTime(a, b) };
  const order = arr => arr.slice().sort(sorters[state.breakdownSort] || byTime);
  const phaseRows = PHASE_ORDER.filter(k => k !== 'idle' && !(state.inTurn && k === 'wait_user'));
  // Lifecycle rows: the same time keyed by SDLC stage, each with its LLM / tools split so the
  // two partitions are never read as comparable per key (a review hour includes its model time,
  // the Development row never does). A work stage with no time in the window is still listed when it
  // has no built-in detector, so the empty slot is visible rather than silently missing.
  const lcSrc = state.allLanes ? t.allByLc : t.byLc;
  const lcSplitSrc = state.allLanes ? t.allLcSplit : t.lcSplit;
  const lcRows = LIFECYCLE_ORDER.filter(k => k !== 'idle' && !(state.inTurn && k === 'wait_user'));
  const lcEntries = lcRows.map(k => {
    const n = lcSrc[k] || 0;
    const sp = lcSplitSrc[k] || { llm: 0, tools: 0 };
    const work = LIFECYCLES[k].work;
    const sub = work && n ? `model ${fmt(sp.llm)} · tools ${fmt(sp.tools)}` : '';
    const sel = INTERVAL_PHASES.has(k) ? state.phase === k && state.lifecycle === 'all' : state.lifecycle === k;
    return [k, n, LIFECYCLES[k], sel, `data-action="filter-lifecycle" data-lc="${k}"`, countOfLc(k), sub];
  });
  // Only SDLC stages are stages. Waiting, compaction, telemetry gaps, unknown commands and the
  // model output of a turn without tool calls happened inside or between them; they keep their
  // own name in the data (the partition still sums to elapsed) and sit under "Outside stages".
  const stageEntries = lcEntries.filter(e => LIFECYCLES[e[0]].work);
  const outsideEntries = lcEntries.filter(e => !LIFECYCLES[e[0]].work);
  const outsideRows = rows(order(outsideEntries), denom);
  const lcSection = `<div class="mini-heading"><h3>Lifecycle stage</h3><span class="mini-note">same time, by SDLC stage</span></div>` + rows(order(stageEntries), denom)
    + (outsideRows ? `<div class="mini-heading"><h3>Outside stages</h3><span class="mini-note">inside or between the stages above · same total</span></div>` + outsideRows : '')
    + `<div class="mini-heading"><h3>Activity</h3><span class="mini-note">what each tool call was</span></div>`;
  // a phase with sub-rows lists them right under it, by time, hidden when empty
  const subSrc = state.allLanes ? t.allBySub : t.bySub;
  const subEntries = k => Object.keys(SUBGROUPS[k] || {}).map(sub => [`${k}:${sub}`, subSrc[`${k}:${sub}`] || 0, { name: SUBGROUPS[k][sub], color: PHASES[k].color }, state.phase === k && state.sub === sub, `data-action="filter-sub" data-sub-phase="${k}" data-sub="${sub}"`, countOfSub(k, sub), '', 'sub']).sort(byTime);
  const roleRows = rows(order(ROLE_ORDER.map(k => [k, t.roles[k], ROLES[k], state.role === k, `data-action="filter-role" data-role="${k}"`, countOf('all', k)])), testTotal);
  const roleSection = roleRows ? `<div class="mini-heading"><h3>Attempts &amp; retries</h3><span class="mini-note">test · build · release · infra · op time, ${MAIN_THREAD}</span></div>` + roleRows : '';
  const sortOptions = [['longest', 'by time (%)'], ['records', 'by records']].map(([v, l]) => `<option value="${v}" ${state.breakdownSort === v ? 'selected' : ''}>${l}</option>`).join('');
  const tools = `<div class="mini-heading breakdown-tools">`
    + `<label class="row"><input type="checkbox" id="inTurnToggle" ${state.inTurn ? 'checked' : ''}>Inside turns only</label>`
    + `<label class="row"><input type="checkbox" id="allLanesToggle" ${state.allLanes ? 'checked' : ''}>Include sub-agents</label>`
    + `<span class="row note">sort <select class="select compact" id="breakdownSort">${sortOptions}</select></span>`
    + `<span class="mini-note">${fmt(t.raw)} op time in ${fmt(t.inTurn)}</span></div>`;
  const phaseEntries = order(phaseRows.map(k => [k, src[k], PHASES[k], state.phase === k, `data-action="filter" data-phase="${k}"`, countOf(k, 'all'), PHASES[k].kind === 'work' ? 'tool calls only' : ''])).flatMap(e => [e, ...subEntries(e[0])]);
  $('#breakdownBody').innerHTML = tools + lcSection + rows(phaseEntries, denom) + roleSection + (t.bg ? `<div class="mini-heading"><h3>Background processes</h3><span class="mini-note">${t.bgOps} · not in totals</span></div>${rows([['bg', t.bg, { name: 'Outlived their turn (servers, watchers)', color: '#8a6d3b' }, state.role === 'background', 'data-action="filter-role" data-role="background"', countOf('all', 'background')]], Math.max(t.bg, t.duration))}` : '') + (t.agentMs ? `<div class="mini-heading"><h3>Sub-agents in window</h3><span class="mini-note">${t.agentLanes} lanes</span></div>${rows([['agents', t.agentMs, { name: 'Agent time (sum)', color: '#86d0b9' }, false, 'data-action="noop"', null], ['wall', t.agentWall, { name: 'Wall clock (union)', color: '#5f8f86' }, false, 'data-action="noop"', null]], Math.max(t.agentMs, 1))}` : '');
  renderOperations(); renderConversation(); renderAgents();
}
// Interval phases have no operations behind them: waiting for the user (root) or the parent
// (sub-agent) and missing telemetry are gaps in the partition. Their records are the gaps.
const INTERVAL_PHASES = new Set(['wait_user', 'idle', 'no_telemetry']);
// opMatches is the operations-list predicate for one phase / role choice; the failures-only
// filter applies with it, so the breakdown counts match what a click on that row lists.
function opMatches(o, phase, role, lifecycle = state.lifecycle, sub = state.sub) {
  if (state.failedOnly && !failure(o)) return false;
  if (phase !== 'all' && o.phase !== phase) return false;
  if (sub !== 'all' && o.sub !== sub) return false;
  if (lifecycle !== 'all' && lifecycleOf(o) !== lifecycle) return false;
  if (role === 'all') return true;
  if (role === 'single') return o.phase === 'test' && !roleOf(o.kind);
  if (role === 'remote') return !!o.remote;
  if (role === 'background') return !!o.background;
  return roleOf(o.kind) === role;
}
// intervalRecords lists the gaps of one interval phase inside the window (lane filter applied),
// one record per gap: contiguous segments of the phase are one gap.
function intervalRecords(phase) {
  const m = current();
  const out = [];
  for (const l of m.lanes) {
    if (state.lane !== 'all' && state.lane !== l.id) continue;
    let last = null;
    for (const sg of l.segments) {
      if (sg.s >= state.b) break;
      if (sg.p !== phase || sg.e <= state.a) continue;
      if (last && sg.s <= last.end) {
        last.end = Math.max(last.end, sg.e);
        continue;
      }
      last = { id: `${l.id}:${phase}:${sg.s}`, interval: true, lane: l.id, phase, start: sg.s, end: sg.e };
      out.push(last);
    }
  }
  return out;
}
// recordsFor is what the operations list shows for a phase / role choice, unsorted.
function recordsFor(phase, role) {
  if (INTERVAL_PHASES.has(phase)) return intervalRecords(phase);
  return opsInWindow(state.a, state.b).filter(o => opMatches(o, phase, role));
}
function filteredOps() {
  const a = state.a, b = state.b;
  const dur = o => overlap(o.start, o.end, a, b);
  const ops = recordsFor(state.phase, state.role);
  ops.sort((p, q) => state.sort === 'latest' ? q.start - p.start : state.sort === 'earliest' ? p.start - q.start : state.sort === 'failed' ? (failure(q) - failure(p)) || dur(q) - dur(p) : dur(q) - dur(p) || p.start - q.start);
  return ops;
}
// listRows folds the members of a collapsed run (identical consecutive operations in one lane)
// into one row carrying their count and summed time; the rows keep the list's sort order.
function listRows(ops) {
  const a = state.a, b = state.b;
  const rows = [], runs = new Map();
  for (const o of ops) {
    if (!o.run || state.openRuns.has(o.run)) { rows.push(o); continue; }
    let r = runs.get(o.run);
    if (!r) { r = { run: o.run, lane: o.lane, phase: o.phase, kind: o.kind, title: o.title, status: 'completed', start: o.start, end: o.end, members: [], ms: 0 }; runs.set(o.run, r); rows.push(r); }
    r.members.push(o);
    r.ms += overlap(o.start, o.end, a, b);
    r.start = Math.min(r.start, o.start);
    r.end = Math.max(r.end, o.end);
    if (failure(o)) r.status = 'failed';
  }
  const dur = o => o.members ? o.ms : overlap(o.start, o.end, a, b);
  rows.sort((p, q) => state.sort === 'latest' ? q.start - p.start : state.sort === 'earliest' ? p.start - q.start : state.sort === 'failed' ? (failure(q) - failure(p)) || dur(q) - dur(p) : dur(q) - dur(p) || p.start - q.start);
  return rows;
}
function renderOperations() {
  const m = current(), ops = filteredOps(), rows = listRows(ops);
  const list = $('#operationList');
  const items = rows.slice(0, state.listShown);
  const noun = INTERVAL_PHASES.has(state.phase) ? 'interval' : 'operation';
  const folded = rows.filter(r => r.members).length;
  $('#phaseSelect').value = state.phase; $('#sortSelect').value = state.sort; $('#laneSelect').value = state.lane;
  $('#operationCount').textContent = `${plural(ops.length, noun)}${state.failedOnly ? ' · failures only' : ''}${folded ? ` · ${rows.length} rows, ${folded} run${folded > 1 ? 's' : ''} of identical calls folded` : ''} · durations clipped to the window`;
  const chips = [];
  if (state.role !== 'all') chips.push(`<button class="chip" data-action="clear-role">${ROLES[state.role].name} ${icon('close', true)}</button>`);
  if (state.sub !== 'all') chips.push(`<button class="chip" data-action="clear-sub">${swatchFor(PHASES[state.phase] || PHASES.unknown)}${esc(subgroupName(state.phase, state.sub))} ${icon('close', true)}</button>`);
  if (state.lifecycle !== 'all') chips.push(`<button class="chip" data-action="clear-lifecycle">${swatchFor(LIFECYCLES[state.lifecycle] || LIFECYCLES.unknown)}${LIFECYCLES[state.lifecycle]?.name || state.lifecycle} ${icon('close', true)}</button>`);
  $('#roleFilter').innerHTML = chips.length ? `<div style="padding:0 22px 10px;display:flex;gap:8px;flex-wrap:wrap">${chips.join('')}</div>` : '';
  const intervalItem = (o, i) => {
    const l = m.laneById.get(o.lane);
    const who = l && l.depth > 0 ? ' · ' + esc(l.path.split('/').pop()) : '';
    return `<button class="operation" data-action="inspect-interval" data-lane="${esc(o.lane)}" data-phase="${o.phase}" data-ta="${o.start}" data-tb="${o.end}"><span class="op-number">${pad(i + 1)}</span><i class="color-square" style="background:${PHASES[o.phase].color}"></i><span class="operation-copy"><strong>${PHASES[o.phase].name}</strong><small>${stampS(o.start)} → ${stampS(o.end)} · ${fmt(o.end - o.start, true)}${who}</small></span><span class="op-time">${fmt(overlap(o.start, o.end, state.a, state.b), true)}</span>${icon('right', true)}</button>`;
  };
  const runItem = (r, i) => {
    const l = m.laneById.get(r.lane);
    const who = l && l.depth > 0 ? ' · ' + esc(l.path.split('/').pop()) : '';
    const failed = r.members.filter(failure).length;
    return `<button class="operation run" data-action="run-toggle" data-run="${esc(r.run)}"><span class="op-number">${pad(i + 1)}</span><i class="color-square" style="background:${PHASES[r.phase].color}"></i><span class="operation-copy"><strong>${esc(r.title)} <span class="run-count">×${r.members.length}</span></strong><small>${stampS(r.start)} → ${stampS(r.end)} · ${r.members.length} identical calls in a row · ${esc(baseKind(r.kind))}${failed ? ` · ${failed} failed` : ''}${who} · select to unfold</small></span><span class="op-time">${fmt(r.ms, true)}</span>${icon('right', true)}</button>`;
  };
  const opItem = (o, i) => { const l = m.laneById.get(o.lane); const role = roleOf(o.kind); return `<button class="operation" data-action="inspect" data-id="${esc(o.id)}"><span class="op-number">${pad(i + 1)}</span><i class="color-square" style="background:${PHASES[o.phase].color}"></i><span class="operation-copy"><strong>${esc(o.title)}</strong><small>${stampS(o.start)} · ${esc(baseKind(o.kind))}${o.phase === 'llm' ? ' · ' + esc(modelOf(o)) + (effortOf(o) ? ' / ' + esc(effortOf(o)) : '') : ''}${role ? ' · ' + esc(ROLES[role]?.name || role) : ''}${o.group ? ' · ' + esc(o.group) + (o.attempt ? ' #' + o.attempt : '') : ''} · ${esc(o.status)}${o.exit != null && o.exit !== 0 ? ' exit ' + o.exit : ''}${o.status === 'failed' && (o.kind === 'exec-script-error' || baseKind(o.kind) === 'llm-invalid-args') ? ' · LLM failure' : ''}${o.query_miss ? ' · query miss, not a failure' : ''}${o.remote ? ' · remote' : ''}${o.queued ? ' · after poll loop' : ''}${o.background ? ' · background (outlived its turn)' : ''}${l && l.depth > 0 ? ' · ' + esc(l.path.split('/').pop()) : ''}${o.run ? ` · <a class="run-fold" data-action="run-toggle" data-run="${esc(o.run)}">fold run</a>` : ''}</small></span><span class="op-time">${fmt(overlap(o.start, o.end, state.a, state.b), true)}</span>${icon('right', true)}</button>`; };
  const tail = rows.length > items.length ? `<div class="list-more" id="listMore">${items.length} of ${rows.length} · scroll for more</div>` : '';
  const top = list.scrollTop;
  list.innerHTML = items.length ? items.map((o, i) => o.interval ? intervalItem(o, i) : o.members ? runItem(o, i) : opItem(o, i)).join('') + tail : `<div class="empty"><h3>No matching ${noun}s in this window.</h3><p>Change the filters or move the window.</p><button class="text-btn" data-action="clear-filters">Show everything</button></div>`;
  list.scrollTop = top;
  // a list that does not overflow cannot be scrolled for more: fill it until it does
  if (rows.length > items.length && list.scrollHeight <= list.clientHeight) {
    state.listShown += LIST_CHUNK;
    renderOperations();
  }
}
// showMoreOperations renders the next chunk when the list is scrolled near its end.
function showMoreOperations() {
  const list = $('#operationList');
  if (!list || !$('#listMore') || list.scrollTop + list.clientHeight < list.scrollHeight - 240) return;
  state.listShown += LIST_CHUNK;
  renderOperations();
}
function renderConversation() {
  const r = root(); const items = r.markers.filter(k => ['user_message', 'question', 'final_answer', 'system_message'].includes(k.kind));
  const cls = k => k.kind === 'user_message' ? 'user' : k.kind === 'question' ? 'question' : k.kind === 'system_message' ? 'system' : 'final';
  const label = k => k.kind === 'user_message' ? 'User' : k.kind === 'question' ? 'Agent asks' : k.kind === 'system_message' ? 'Harness' : 'Final answer';
  $('#conversation').innerHTML = items.length ? items.map((k, i) => { const key = `${k.t}:${k.kind}`, open = state.convOpen.has(key), inWin = k.t >= state.a && k.t <= state.b; return `<div class="conv-item ${cls(k)}" style="${inWin ? '' : 'opacity:.55'}"><button class="conv-time" data-action="jump" data-t="${k.t}" title="Jump to this moment"><b>${label(k)}</b>${stamp(k.t)}</button><div><div class="conv-text ${open ? 'open' : ''}">${esc(k.text)}</div>${k.text.length > 380 ? `<button class="text-btn" data-action="conv-toggle" data-key="${esc(key)}" style="margin-top:4px">${open ? 'Show less' : 'Show all'}</button>` : ''}</div></div>`; }).join('') : `<div class="empty"><p>No user messages recorded.</p></div>`;
}
function renderAgents() {
  const m = current();
  const row = l => {
    const active = sum((l.active || []).map(iv => iv.e - iv.s));
    const by = l.by_phase || {};
    const work = ['code', 'build', 'test', 'release', 'infra'].reduce((n, k) => n + (by[k] || 0), 0);
    const total = Math.max(1, l.ended - l.started);
    const { t0 } = laneSpan(l);
    const st = [[PHASES.code.color, work], [PHASES.llm.color, by.llm || 0], [PHASES.wait_worker.color, by.wait_worker || 0], [PHASES.wait_user.color, (by.wait_user || 0) + (by.idle || 0)]];
    const name = `<button class="text-btn lane-name" data-action="lane-card" data-lane="${esc(l.id)}" title="Open the agent card">${esc(l.depth === 0 ? MAIN_THREAD : l.path.split('/').pop())}</button>`;
    const meta = `<div class="lane-meta">${esc([l.role || (l.depth === 0 ? MOTHER_AGENT : 'sub-agent'), agentCaption(l, true)].filter(Boolean).join(' · '))}</div>`;
    const stack = `<div class="stack" aria-hidden="true">${st.map(([c, n]) => `<span style="width:${n / total * 100}%;background:${c}"></span>`).join('')}</div>`;
    const cells = [stamp(t0, false) + `<br><span class="note">${stamp(l.ended, false)}</span>`, l.depth === 0 ? fmt(l.ended - l.started) : fmt(active), l.turns.length, l.ops.length, fmt(work), l.tokens ? fmtTok(l.tokens.total) : '—', l.live ? '<span class="chip live" style="padding:2px 6px">live</span>' : ''];
    return `<tr class="lane-row" data-action="lane-focus" data-lane="${esc(l.id)}"><td><div style="padding-left:${Math.min(l.depth, 3) * 14}px">${name}${meta}${stack}</div></td>${cells.map(c => `<td class="mono">${c}</td>`).join('')}</tr>`;
  };
  $('#agentsTable').innerHTML = `<thead><tr><th>Lane</th><th>Start / end</th><th>Active</th><th>Turns</th><th>Ops</th><th>Tool work</th><th>Tokens</th><th></th></tr></thead><tbody>${m.lanes.map(row).join('')}</tbody>`;
}
// filterPhase / filterRole are the breakdown's quick filters. Only one is active at a time: a
// role already implies its phase (retries ⊂ test/build/release, fix ⊂ development, …), so combining
// them is either redundant or empty.
function filterPhase(phase) {
  state.phase = phase;
  state.sub = 'all';
  if (phase !== 'all') {
    state.role = 'all';
    state.lifecycle = 'all';
  }
  state.listShown = LIST_CHUNK;
  renderTimeline();
  renderLower();
}
function filterRole(role) {
  state.role = role;
  state.sub = 'all';
  if (role !== 'all') {
    state.phase = 'all';
    state.lifecycle = 'all';
  }
  state.listShown = LIST_CHUNK;
  renderTimeline();
  renderLower();
}
function filterLifecycle(lc) {
  // an interval stage (waiting, idle, no telemetry) is its activity phase: the gaps themselves
  if (INTERVAL_PHASES.has(lc)) {
    filterPhase(state.phase === lc ? 'all' : lc);
    return;
  }
  state.lifecycle = state.lifecycle === lc ? 'all' : lc;
  if (state.lifecycle !== 'all') {
    state.phase = 'all';
    state.sub = 'all';
    state.role = 'all';
  }
  state.listShown = LIST_CHUNK;
  renderTimeline();
  renderLower();
}
// filterSub narrows the operations list to one sub-row of a phase (a subgroup of its kinds); a
// second click on the same sub-row clears both.
function filterSub(phase, sub) {
  const same = state.phase === phase && state.sub === sub;
  state.phase = same ? 'all' : phase;
  state.sub = same ? 'all' : sub;
  state.role = 'all';
  state.lifecycle = 'all';
  state.listShown = LIST_CHUNK;
  renderTimeline();
  renderLower();
}

async function guide() {
  const dlg = $('#guide');
  dlg.innerHTML = `<div class="dialog-head"><h2 id="guideTitle">Reading the timeline</h2><button class="btn icon-only ghost" data-action="close-guide" aria-label="Close" autofocus>${icon('close')}</button></div><div class="dialog-body"><section class="guide-section"><h3>One row per agent</h3><p>The ${MAIN_THREAD} (the ${MOTHER_AGENT}'s) is the first lane; every sub-agent thread is its own lane, indented by depth, with a ▶ at spawn, ticks at each message from the parent, and a bar at each completed turn. A dashed line connects spawn time to the parent. Only lanes alive inside the visible window are drawn. Click a lane to expand it into phase rows (and, at fine zoom, individual operations); the ⓘ at the right of its name opens the agent card: nickname, model, the prompt it was spawned with and its final answer.</p></section><section class="guide-section"><h3>Stage band above, raw fill below</h3><p>A lane's fill is the real exclusive time split: teal is LLM time (the model generating — reasoning, answers and the code of every patch), colours are tool phases, pink is waiting for the user, hatched is missing telemetry. Development is every tool call around the code short of building, testing and shipping: reading and searching sources, edits, local git, formatting, lookups and probes. The model's time writing a patch is LLM, not Development: the activity split never moves model output into a tool phase. Above the fill, the <em>stage band</em> names the SDLC stage the same time served (the second partition, below): Implementation, Testing, Code review … in the stage colours, with the model / tools split in its tooltip. The band answers <em>why</em>, the fill answers <em>what</em>; waiting, compaction and telemetry gaps are not stages and leave the band empty. Turn ends carry ✓ (completed), ✕ (interrupted) or ⊘ (never closed); a pulsing dot means the turn is still open. Dashed pink verticals are turn starts.</p></section><section class="guide-section"><h3>What is inferred and what is not</h3><p>Phases come from a rule table over the command text (below). Turn boundaries give waiting-for-user time; a turn that never closed becomes "No telemetry". Retry groups only join operations with an identical normalized command. A failure is a test, build, release, infra step, edit or script that exited non-zero; a read, search, listing or probe that exited non-zero — or a CI status wait such as <code>gh pr checks</code> whose exit says the checks are pending or failing — is a <em>query miss</em> (the exit was its answer) — kept verbatim with its exit code, not counted. Token totals sum each thread's per-call usage, so a resumed thread whose counter restarted still adds up. Nothing is derived from how long something took, and no "goal reached" score exists — read the conversation panel.</p></section><section class="guide-section"><h3>Parallel time</h3><p>Session totals are the ${MAIN_THREAD}'s exclusive wall clock. Sub-agent time is summed separately and its union shown as "wall".</p></section><section class="guide-section"><h3>Code review</h3><p>A bookmark glyph marks where a review or cleanup skill (for example <code>code-review-cc</code> or <code>simplify</code>) was <em>actually invoked</em> — detected from the harness's skill-instruction injection, not from the words in a prompt. The stage band above the fill takes the Code review colour for each turn a review skill ran in — and for the sub-agent turns spawned inside it, which work for that review — and the whole-session card totals the ${MAIN_THREAD}'s wall clock; it is a stage, not an activity, so the fill still shows the real LLM/development/wait split above it. Turns that reuse the skill later without re-invoking it are not counted. Detection has two sources: a built-in set of tools and skills, and an optional user overlay (project commands and review-skill names); both are listed below.</p></section><section class="guide-section"><h3>Keyboard</h3><p><kbd>+</kbd>/<kbd>−</kbd> zoom, <kbd>←</kbd>/<kbd>→</kbd> pan, <kbd>Home</kbd> fit, <kbd>Esc</kbd> close. Drag the plot to pan; drag on the overview to select.</p></section><section class="guide-section"><h3>Lifecycle stages (SDLC)</h3><p>Every millisecond of a lane also belongs to exactly one <em>lifecycle stage</em>: the stage of the software lifecycle the time served. It is a second partition next to the activity phases (same total), never a replacement: a code review still runs tests, and those minutes stay Testing in the activity list while they are Code review here. Each stage row shows its model / tools split because the two partitions attribute model output differently. Every assignment comes from a literal, harness-level signal — a collaboration mode, a skill the harness actually injected, an agent's spawn role, an edited path, a command kind — never from words in a message and never from a duration. Requirements and design have no built-in detector: nothing in a Codex rollout or a Claude Code log marks them; add skill names, agent roles or document paths in your overlay. Model output takes the stage of the nearest tool call in its turn — the next one (the call it prepared), else the previous one (the answer that reported on it); a compaction or a telemetry gap in between is skipped over, while a wait for a worker or the user ends that attribution. Only a turn that made no tool call at all keeps its model output as <em>Model output, no tool call</em>: nothing in it says which stage it served, and only a harness signal (plan mode, a skill, a plan written with <code>update_plan</code> or <code>ExitPlanMode</code>) could. The stage list holds the SDLC stages only; waiting, compaction, telemetry gaps, unknown commands and that model output are not stages — they happened inside or between them and sit under <em>Outside stages</em> with the same denominator, so the two lists still add up to the whole. A turn is read as a group before any of its calls is decided: every code, build, test and infrastructure call between the turn's first and last change (an edit, a patch, a written file) is implementation — a test run between two edits is the loop, not QA — while a test after the last change is the verification pass, and a call before the turn's first plan that changed nothing is planning. A skill pins its stage from the moment it was invoked to the end of the turn (Codex injects it at the turn start, so the whole turn; Claude Code's Skill tool call may come mid-turn). A sub-agent's turn is a tool call of the parent turn it ran inside, so it takes that turn's stage when it has no signal of its own; the inspector names the origin.</p><div style="overflow:auto;max-height:340px"><table class="rules-table" id="lifecycleTable"></table></div></section><section class="guide-section"><h3>Classification rules</h3><p id="rulesNote">loading…</p><div style="overflow:auto;max-height:340px"><table class="rules-table" id="rulesTable"></table></div></section><section class="guide-section"><h3>Sub-rows of Development, Waiting for workers and Unknown</h3><p>Three phases lump different work, so the breakdown lists them with sub-rows: what the tool calls read, searched, edited, versioned, fetched or ran. A sub-row is a fixed grouping of the kinds below (a function of the phase and the kind, never of a duration); its time is the exclusive partition of those calls, its count the calls in the window. Select one to list exactly those operations.</p><div style="overflow:auto;max-height:340px"><table class="rules-table" id="subgroupTable"></table></div></section>${insightsGuideHTML()}</div>`;
  dlg.showModal();
  try {
    const d = await api('/api/rules');
    const nUser = d.rules.length - (d.builtin_rules ?? d.rules.length);
    const reviewNote = (d.review_skills && d.review_skills.length) ? ` Review skills (name matches, one per invocation): ${d.review_skills.map(esc).join(', ')}.` : '';
    $('#rulesNote').innerHTML = `${d.rules.length} rules (${d.builtin_rules ?? d.rules.length} built-in${nUser > 0 ? `, ${nUser} from your overlay` : ''}). A command takes the highest-priority phase among its segments (release &gt; test &gt; build &gt; workers &gt; infra &gt; development). Unmatched commands stay Unknown. Add project-specific commands (optionally pinned to a lifecycle stage) and the skill names, agent roles and document paths that pin a stage in a user rules file (<code>--rules</code>, <code>$TODOBEM_RULES</code>, or <code>~/.todobem/rules.json</code>).${reviewNote}`;
    const builtin = d.builtin_rules ?? d.rules.length;
    $('#lifecycleTable').innerHTML = lifecycleGuideRows(d.lifecycle || {});
    $('#subgroupTable').innerHTML = `<thead><tr><th>Phase</th><th>Sub-row</th><th>Kinds</th></tr></thead><tbody>${(d.subgroups || []).map(r => `<tr><td><span class="row"><i class="color-square" style="background:${PHASES[r.phase]?.color}"></i>${esc(PHASES[r.phase]?.name || r.phase)}</span></td><td>${esc(subgroupName(r.phase, r.subgroup))}</td><td class="note">${esc(r.kinds)}</td></tr>`).join('')}</tbody>`;
    $('#rulesTable').innerHTML = `<thead><tr><th>Match</th><th>Phase</th><th>Kind</th><th>Note</th></tr></thead><tbody>${d.rules.map((r, i) => `<tr><td><code>${esc(r.match)}</code>${i >= builtin ? ' <span class="chip" style="padding:1px 5px">user</span>' : ''}</td><td><span class="row"><i class="color-square" style="background:${PHASES[r.phase]?.color}"></i>${esc(r.phase)}</span></td><td>${esc(r.kind)}</td><td class="note">${esc(r.note || '')}</td></tr>`).join('')}</tbody>`;
  } catch (e) { $('#rulesNote').textContent = 'rules unavailable'; }
}

// lifecycleGuideRows renders the lifecycle table for the guide from /api/rules: for each stage
// its rule sources (built-in and overlay), so the second partition is as inspectable as the first.
function lifecycleGuideRows(lc) {
  const defaults = lc.defaults || {};
  const pins = lc.pins || {};
  const matchers = lc.matchers || {};
  const list = (obj, stage) => (obj && obj[stage] ? obj[stage] : []);
  const sources = {
    plan: ['plan mode — Codex collaboration mode, Claude Code permission mode (whole turn)', 'a plan written with update_plan or ExitPlanMode: the model output before it, and every tool call before the turn\'s first plan that changed nothing', 'sub-agent turns that ran inside a plan-mode turn (inherited)'],
    requirements: ['no built-in detector'],
    design: ['no built-in detector'],
    implement: ['default for ' + Object.keys(defaults).filter(p => defaults[p] === 'implement').sort().join(', ') + ' commands', 'every code, build, test and infra call between the turn\'s first and last change (an edit, patch, written file, in-place sed, formatter, file copy or move): a test between two edits is the loop, not QA', 'operations commands before this lane\'s first release'],
    review: ['skill invoked in the turn: from the moment it was invoked to the turn\'s end (Codex injects it at the turn start, so the whole turn; Claude Code\'s Skill tool mid-turn); not inside a plan-mode turn, which stays planning', 'Codex review mode (whole turn)', 'sub-agent turns that ran inside such a turn or run (inherited)'],
    test: ['default for test commands'],
    release: ['default for release commands'],
    operate: ['after this lane\'s first release only'],
  };
  const pinned = stage => Object.keys(pins).filter(k => pins[k] === stage).sort();
  const rows = LIFECYCLE_ORDER.filter(k => LIFECYCLES[k].work).map(stage => {
    const parts = [...(sources[stage] || [])];
    if (pinned(stage).length) parts.push('command kinds: ' + pinned(stage).join(', '));
    for (const [what, label] of [['skills', 'skill names'], ['roles', 'agent roles'], ['paths', 'edited paths']]) {
      const pats = list(matchers[what], stage);
      if (pats.length) parts.push(`${label}: ${pats.map(x => `<code>${esc(x)}</code>`).join(', ')}`);
    }
    return `<tr><td><span class="row">${railSwatch(LIFECYCLES[stage].color)}${LIFECYCLES[stage].name}</span></td><td>${parts.join('<br>')}</td></tr>`;
  });
  return `<thead><tr><th>Stage</th><th>Assigned from</th></tr></thead><tbody>${rows.join('')}<tr><td><span class="row"><i class="color-square" style="background:${LIFECYCLES.llm.color}"></i>${LIFECYCLES.llm.name}</span></td><td>not a stage: model output of a turn that made no tool call at all (a text-only answer), listed under "Outside stages"</td></tr><tr><td>Waiting, idle, compaction, no telemetry, unknown</td><td>not stages: they pass through under their activity name</td></tr></tbody>`;
}

/* ---------- tooltip ---------- */
function hideTooltip() { $('#tooltip').style.display = 'none'; }
function tooltip(event) {
  if (event.pointerType === 'touch' || plotDrag || overviewDrag || $('#inspector').open || $('#guide').open) return;
  const target = event.target.closest('[data-turn],[data-stage],[data-op],[data-group],[data-mk],.lane-head'); if (!target) { hideTooltip(); return; }
  const m = current(), tip = $('#tooltip'); let body = '';
  if (target.classList.contains('lane-head')) {
    const l = m.laneById.get(target.dataset.lane);
    if (!l) return;
    const { t0, t1 } = laneSpan(l);
    const active = sum((l.active || []).map(iv => iv.e - iv.s));
    body = `<strong>${esc(l.path)}</strong><span>${esc(agentCaption(l, true) || MAIN_THREAD)}</span><span>${spanLabel(t0, t1)} · ${l.depth ? 'active ' + fmt(active, true) : fmt(t1 - t0, true)} · ${l.turns.length} turns · ${l.ops.length} ops</span><span>Select the row to expand phase rows; the ⓘ opens the agent card (prompt, final answer).</span>`;
  }
  else  if (target.dataset.turn) { const [lane, tid] = target.dataset.turn.split(':'); const l = m.laneById.get(lane); const tn = l?.turns.find(t => t.id === tid); if (!tn) return; const n = l.ops.filter(o => o.turn === tid).length; const names = { completed: 'Turn completed', aborted: 'Turn interrupted', orphaned: 'Turn never closed (process ended?)', open: 'Turn in progress' }; body = `<strong>${names[tn.status] || tn.status} · ${stamp(tn.end, false)}</strong><span>${spanLabel(tn.start, tn.end)} · ${fmt(tn.end - tn.start, true)} · ${n} operations${tn.model ? ' · ' + esc(tn.model) + (tn.effort ? ' / ' + esc(tn.effort) : '') : ''}</span>${tn.final ? `<span>${esc(stripMd(tn.final).slice(0, 200))}</span>` : ''}`; }
  else if (target.dataset.mk) { const [lane, t, kind] = target.dataset.mk.split(':'); const l = m.laneById.get(lane); const mk = l?.markers.find(k => String(k.t) === t && k.kind === kind); if (!mk) return; body = `<strong>${esc(MARKS[kind]?.name || kind)}</strong><span>${stampS(mk.t)}</span><span>${esc((mk.text || '').slice(0, 220))}</span>`; }
  else if (target.dataset.group) { const g = m.groupById.get(target.dataset.group); body = `<strong>${esc(g.id)} · ${g.attempts} attempts · ${g.failed} failed</strong><span>${spanLabel(g.start, g.end)}</span><span>${esc(g.title.slice(0, 160))}</span><span>Same normalized command. Select to focus.</span>`; }
  else if (target.dataset.op) { const o = m.opById.get(target.dataset.op); if (!o) return; body = `<strong>${esc(o.title.slice(0, 160))}</strong><span>${spanLabel(o.start, o.end)} · ${fmt(o.end - o.start, true)}</span><span>${PHASES[o.phase].name} · ${esc(baseKind(o.kind))}${o.phase === 'llm' ? ' · ' + esc(modelOf(o)) : ''} · ${esc(o.status)}${o.group ? ' · ' + esc(o.group) : ''}${o.background ? ' · background process (outlived its turn; not in totals)' : ''}</span><span>Select to inspect the source event.</span>`; }
  else if (target.dataset.band) {
    const ta = Number(target.dataset.ta), tb = Number(target.dataset.tb), lc = target.dataset.lc, l = m.laneById.get(target.dataset.lane);
    const def = LIFECYCLES[lc] || LIFECYCLES.unknown;
    // the honesty mechanism: the stage's model / tools split, because the stage band attributes
    // model output to the stage it served while the fill keeps it as LLM
    let tools = 0, model = 0, calls = 0;
    for (const sg of l ? l.segments : []) {
      if (sg.e <= ta) continue;
      if (sg.s >= tb) break;
      if (lifecycleOf(sg) !== lc) continue;
      const ov = overlap(sg.s, sg.e, ta, tb);
      if (sg.p === 'llm') model += ov; else tools += ov;
    }
    for (const o of l ? l.ops : []) { if (o.phase !== 'llm' && !o.background && o.end > ta && o.start < tb && lifecycleOf(o) === lc) calls++; }
    body = `<strong>${esc(def.name)} · ${fmt(tb - ta)}</strong><span>${spanLabel(ta, tb)}</span><span>model ${fmt(model, true)} · tools ${fmt(tools, true)} · ${calls} tool call${calls === 1 ? '' : 's'}</span><span>The stage this time served; the fill below shows what ran. Select to frame it.</span>`;
  }
  else if (target.dataset.phase === 'cluster') { const ta = Number(target.dataset.ta), tb = Number(target.dataset.tb), l = m.laneById.get(target.dataset.lane); const items = l.markers.filter(k => k.t >= ta && k.t < tb && MARKS[k.kind] && !k.kind.startsWith('agent_')); body = `<strong>${items.length} markers · ${spanLabel(ta, tb)}</strong>` + items.slice(0, 8).map(k => `<span>${stamp(k.t, false)} ${esc(MARKS[k.kind].name)}: ${esc((k.text || '').slice(0, 70))}</span>`).join('') + (items.length > 8 ? `<span>…</span>` : '') + `<span>Select to zoom in.</span>`; }
  else if (target.dataset.pending) { const ta = Number(target.dataset.ta), tb = Number(target.dataset.tb); body = `<strong>No telemetry yet (in progress) · ${fmt(tb - ta)}</strong><span>${spanLabel(ta, tb)}</span><span>The turn is still open: the agent is generating and nothing has been recorded since the last event.</span><span>It resolves to a phase as soon as the next event lands.</span>`; }
  else { const ta = Number(target.dataset.ta), tb = Number(target.dataset.tb), p = target.dataset.phase, l = m.laneById.get(target.dataset.lane); const ops = l ? l.ops.filter(o => o.end > ta && o.start < tb && (target.dataset.phaseRow ? o.phase === p : true)).length : 0; body = `<strong>${PHASES[p].name} · ${fmt(tb - ta)}</strong><span>${spanLabel(ta, tb)}</span><span>${esc(l ? l.path : '')} · ${ops} operations inside</span><span>Select to zoom in.</span>`; }
  tip.innerHTML = body; tip.style.display = 'block'; const r = tip.getBoundingClientRect(); tip.style.left = clamp(event.clientX + 13, 8, window.innerWidth - r.width - 8) + 'px'; tip.style.top = clamp(event.clientY + 17, 8, window.innerHeight - r.height - 8) + 'px';
}

/* ---------- events ---------- */
document.addEventListener('click', e => {
  const el = e.target.closest('[data-action]'); if (!el || el.disabled) return; const a = el.dataset.action, m = current();
  if (a === 'insights' || a.startsWith('ins-')) return insightsAction(a, el);
  if (a === 'settings' || a.startsWith('set-')) return settingsAction(a, el);
  if (a === 'filter-apply') { // custom dates of the shared filter: the Insights report, or the session list
    if (el.dataset.prefix === 'ins') { if (filterApply('ins', insightsState().params)) loadInsights(); }
    else if (filterApply('fleet', state.fleetFilter)) { persistFleetPeriod(); render(); }
    return;
  }
  if (a === 'sessions') return go('sessions'); if (a === 'session') return go('session', el.dataset.id || state.id);
  if (a === 'lock') return lockNow();
  if (a === 'guide') return guide(); if (a === 'close-guide') return $('#guide').close(); if (a === 'close-inspector') return $('#inspector').close();
  if (a === 'refresh') {
    const navigation = navigationRequest;
    loadSession(state.id, { keepWindow: true, refresh: true }).then(applied => { if (applied) { keepPlace(render); toast('Re-parsed from the session files.'); } }).catch(e => {
      if (navigation === navigationRequest) toast('Could not refresh session: ' + e.message);
    });
    return;
  }
  if (a === 'copy-answer') {
    const fin = [...root().markers].reverse().find(k => k.kind === 'final_answer');
    if (!fin) return toast('No final answer to copy.');
    copyText(fin.text).then(ok => toast(ok ? 'Answer copied to clipboard.' : 'Copy failed — select the text manually.'));
    return;
  }
  if (a === 'fit') return setWindow(m.started, m.ended);
  if (a === 'preset') { const v = Number(el.dataset.span); if (!v) return setWindow(m.started, m.ended); if (state.follow || state.b >= m.ended - 1000) return setWindow(m.ended - v, m.ended, { manual: false }); return chooseSpan(v); }
  if (a === 'zoom') return zoom(Number(el.dataset.dir)); if (a === 'pan') return pan(Number(el.dataset.dir));
  if (a === 'follow') { state.follow = !state.follow; if (state.follow) { const span = Math.min(state.b - state.a, 6 * 3600e3); setWindow(m.ended - span, m.ended, { manual: false }); toast(m.live ? 'Following the live edge.' : 'At the latest event. Session is not live.'); } else renderTimeline(); return; }
  if (a === 'expand-all') { m.lanes.forEach(l => state.expanded.add(l.id)); renderTimeline(); return; }
  if (a === 'collapse-all') { state.expanded.clear(); renderTimeline(); return; }
  if (a === 'filter') { filterPhase(state.phase === el.dataset.phase ? 'all' : el.dataset.phase); return; }
  if (a === 'filter-role') { filterRole(state.role === el.dataset.role ? 'all' : el.dataset.role); return; }
  if (a === 'filter-lifecycle') { filterLifecycle(el.dataset.lc); return; }
  if (a === 'filter-sub') { filterSub(el.dataset.subPhase, el.dataset.sub); return; }
  if (a === 'clear-sub') { filterSub(state.phase, state.sub); return; }
  if (a === 'clear-role') { filterRole('all'); return; }
  if (a === 'clear-lifecycle') { filterLifecycle('all'); return; }
  if (a === 'clear-filters') { state.phase = 'all'; state.sub = 'all'; state.role = 'all'; state.lifecycle = 'all'; state.lane = 'all'; renderTimeline(); renderLower(); return; }
  if (a === 'inspect') return inspect(el.dataset.id);
  if (a === 'run-toggle') { const id = el.dataset.run; state.openRuns.has(id) ? state.openRuns.delete(id) : state.openRuns.add(id); renderOperations(); return; }
  if (a === 'inspect-interval') {
    const ta = Number(el.dataset.ta), tb = Number(el.dataset.tb);
    if (el.dataset.phase === 'no_telemetry') return focusInterval(ta, tb);
    return inspectWait(el.dataset.lane, ta, tb);
  }
  if (a === 'focus-op') { const o = m.opById.get(el.dataset.id); if (o) focusInterval(o.start, o.end); return; }
  if (a === 'focus-at') { const t = Number(el.dataset.t); focusInterval(t, t); return; }
  if (a === 'focus-group') { const g = m.groupById.get(el.dataset.group); if (g) focusInterval(g.start, g.end); return; }
  if (a === 'jump') { const t = Number(el.dataset.t); if ($('#inspector').open) $('#inspector').close(); chooseSpan(Math.min(state.b - state.a, 3600e3), t); revealPlot(); return; }
  if (a === 'conv-toggle') { const k = el.dataset.key; state.convOpen.has(k) ? state.convOpen.delete(k) : state.convOpen.add(k); renderConversation(); return; }
  if (a === 'lane-focus') { state.lane = state.lane === el.dataset.lane ? 'all' : el.dataset.lane; state.listShown = LIST_CHUNK; renderTimeline(); renderOperations(); renderAgents(); return; }
  if (a === 'lane-card') return inspectLane(el.dataset.lane);
  if (a === 'metrics-toggle') { state.metricsOpen = !state.metricsOpen; $('#sessionMetrics').innerHTML = metricsHTML(); return; }
  if (a === 'zoom-lane') {
    const l = m.laneById.get(el.dataset.lane);
    if (!l) return;
    if ($('#inspector').open) $('#inspector').close();
    const { t0, t1 } = laneSpan(l);
    const pad = Math.max(30e3, (t1 - t0) * .1);
    setWindow(t0 - pad, t1 + pad);
    revealPlot();
    return;
  }
});
document.addEventListener('submit', e => {
  if (e.target && e.target.id === 'lockForm') {
    e.preventDefault();
    unlock($('#lockToken') ? $('#lockToken').value : '').catch(err => renderLock(err.message));
  }
  if (e.target && e.target.id === 'settingsForm') {
    e.preventDefault();
    saveSettings();
  }
});
document.addEventListener('change', e => { const id = e.target.id, v = e.target.type === 'checkbox' ? e.target.checked : e.target.value; if (id === 'showGroups') { state.groups = e.target.checked; renderTimeline(); } else if (id === 'inTurnToggle') { state.inTurn = e.target.checked; renderLower(); } else if (id === 'allLanesToggle') { state.allLanes = e.target.checked; renderLower(); } else if (id === 'failedOnly') { state.failedOnly = e.target.checked; state.listShown = LIST_CHUNK; renderOperations(); } else if (id === 'breakdownSort') { state.breakdownSort = v; renderLower(); } else if (id === 'phaseSelect') filterPhase(v); else if (id === 'laneSelect') { state.lane = v; state.listShown = LIST_CHUNK; renderTimeline(); renderOperations(); renderAgents(); } else if (id === 'sortSelect') { state.sort = v; state.listShown = LIST_CHUNK; renderOperations(); } else if (id === 'fleetSort') { state.fleetSort = v; renderFleetRows(); } else if (id === 'intervalSelect') { state.interval = Number(v); schedulePoll(); toast(`Refreshing every ${v}s.`); } else if (id.startsWith('ins')) insightsChange(id, v); else if (filterField('fleet', id)) { const fld = filterField('fleet', id); const refresh = filterChange(state.fleetFilter, fld, v); if (fld === 'Period') persistFleetPeriod(); if (refresh) render(); } });
document.addEventListener('input', e => {
  if (e.target.id === 'sessionSearch') { state.search = e.target.value; renderFleetRows(); }
  else if (e.target.dataset && e.target.dataset.homeIndex !== undefined) settingsInput(e.target.dataset.source, Number(e.target.dataset.homeIndex), e.target.value);
});
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
  if (Date.now() - lastDragTime < 180) return; const plot = e.target.closest('#plot'); if (!plot || e.target.closest('[data-action]')) return;
  const head = e.target.closest('.lane-head'), prow = e.target.closest('[data-phase-row]'), op = e.target.closest('[data-op]'), stage = e.target.closest('[data-stage]'), group = e.target.closest('[data-group]'), mk = e.target.closest('[data-mk]');
  if (head) { const id = head.dataset.lane; state.expanded.has(id) ? state.expanded.delete(id) : state.expanded.add(id); renderTimeline(); }
  else if (prow) { filterPhase(state.phase === prow.dataset.phaseRow ? 'all' : prow.dataset.phaseRow); }
  else if (e.target.closest('[data-turn]')) { const [lane, tid] = e.target.closest('[data-turn]').dataset.turn.split(':'); const tn = current().laneById.get(lane)?.turns.find(t => t.id === tid); if (tn) focusInterval(tn.start, tn.end); }
  else if (mk) inspectMarker(mk.dataset.mk);
  else if (op && op.dataset.op) inspect(op.dataset.op);
  else if (group) { const g = current().groupById.get(group.dataset.group); if (g) focusInterval(g.start, g.end); }
  else if (stage && (stage.dataset.phase === 'wait_user' || stage.dataset.phase === 'idle') && stage.dataset.lane) { inspectWait(stage.dataset.lane, Number(stage.dataset.ta), Number(stage.dataset.tb)); }
  else if (stage) zoomToBlock(Number(stage.dataset.ta), Number(stage.dataset.tb));
});
document.addEventListener('keydown', e => {
  if (e.target.id !== 'plot') return; if (['+', '=', '-', '_', 'ArrowLeft', 'ArrowRight', 'Home'].includes(e.key)) e.preventDefault();
  if (e.key === '+' || e.key === '=') zoom(1); else if (e.key === '-' || e.key === '_') zoom(-1); else if (e.key === 'ArrowLeft') pan(-1); else if (e.key === 'ArrowRight') pan(1); else if (e.key === 'Home') setWindow(current().started, current().ended);
});
document.addEventListener('wheel', e => { if (!e.target.closest('#plot') || e.ctrlKey || e.metaKey || Math.abs(e.deltaX) < Math.abs(e.deltaY) || Math.abs(e.deltaX) < 2 || !geometry) return; e.preventDefault(); const delta = e.deltaX / geometry.P * (state.b - state.a); setWindow(state.a + delta, state.b + delta, { lower: false }); clearTimeout(tipTimer); tipTimer = setTimeout(renderLower, 250); }, { passive: false });
['inspector', 'guide'].forEach(id => { const d = $('#' + id); d.addEventListener('click', e => { if (e.target === d) d.close(); }); });
$('#inspector').addEventListener('close', () => { if (!$('#inspector').open) ++inspectorRequest; });
window.addEventListener('resize', () => { clearTimeout(resizeTimer); resizeTimer = setTimeout(() => { if (state.page === 'session') renderTimeline(); }, 80); });
document.addEventListener('scroll', e => { if (e.target && e.target.id === 'operationList') showMoreOperations(); }, true);
window.addEventListener('popstate', route);
window.addEventListener('hashchange', route);
document.addEventListener('visibilitychange', () => { if (!document.hidden && state.page === 'session') schedulePoll(); });
function route() {
  const h = location.hash;
  if (h.startsWith('#token=')) {
    // a login link pasted into a tab that already runs the app: a hash change, not a load
    const tok = tokenFromHash();
    if (tok) {
      state.locked = true;
      unlock(tok).catch(e => renderLock(e.message));
    }
    return;
  }
  if (h === '#insights' || h.startsWith('#insights?')) {
    if (state.page !== 'insights') go('insights');
    return;
  }
  if (h === '#settings') {
    if (state.page !== 'settings') go('settings');
    return;
  }
  if (h.startsWith('#session/')) {
    let id;
    let rest = h.slice('#session/'.length);
    const q = rest.indexOf('?');
    if (q >= 0) {
      const m = /(?:^|&)focus=(\d+)-(\d+)/.exec(rest.slice(q + 1));
      if (m) state.pendingFocus = { a: Number(m[1]), b: Number(m[2]) };
      rest = rest.slice(0, q);
    }
    try { id = decodeURIComponent(rest); } catch (e) { go('sessions'); return; }
    if (id !== requestedSessionID || state.page !== 'session') go('session', id);
    else applyPendingFocus();
  } else if (state.page !== 'sessions') go('sessions');
}
window.todobem = { state, current, windowStats, setWindow, go };

/* ---------- lite authentication ---------- */
// The API answers 401 until the browser holds a session; a one-time token (`todobem token`)
// opens one. The lock screen replaces the page; nothing of a session is rendered while locked.
// lock(reason): a 401 arrived (reason says whether this browser had a session that ended) —
// everything of the current session is dropped and the lock screen replaces the page.
function lock(reason) {
  const hadSession = !!(state.model || state.sessions.length);
  if (state.locked) return;
  state.locked = true;
  stopPoll();
  state.model = null;
  state.id = null;
  state.sessions = [];
  requestedSessionID = null;
  $('#navCount').textContent = '';
  $('#lockBtn').hidden = true;
  if ($('#inspector').open) $('#inspector').close();
  if ($('#guide').open) $('#guide').close();
  renderLock(reason || (hadSession ? 'Your session has expired or was revoked (the server refused the last request).' : ''));
  nav();
}
const LOGIN_COMMAND = './todobem token -ttl 30d';
function renderLock(message) {
  $('#main').innerHTML = `<section class="lock" aria-labelledby="lockTitle"><div class="lock-card"><div class="eyebrow">Locked</div><h1 id="lockTitle">Enter a login token</h1><p>Sessions can contain sensitive material, so the viewer stays locked until you prove you are its owner. Run the following command in the project root on the machine that serves todobem:</p><pre class="lock-cmd">${LOGIN_COMMAND}</pre><p>then open the link it prints, or paste the token here. The token works once, within 5 minutes; the browser then stays logged in for the given time.</p><form id="lockForm" class="lock-form"><input id="lockToken" class="lock-input" type="text" autocomplete="off" spellcheck="false" placeholder="token" aria-label="Login token"><button class="btn primary" type="submit">Unlock</button></form><p class="lock-note" id="lockNote">${esc(message)}</p></div></section>`;
  const input = $('#lockToken');
  if (input && input.focus) input.focus();
}
async function unlock(token) {
  const t = String(token || '').trim();
  if (!t) return false;
  const r = await apiPost('/api/login', { token: t });
  if (r.ok) {
    state.locked = false;
    await boot();
    return true;
  }
  const reason = r.payload && r.payload.reason;
  const why = reason === 'expired' ? `That token has expired (they last 5 minutes). Run ${LOGIN_COMMAND} again.` : reason === 'used' ? `That token was already used. Run ${LOGIN_COMMAND} for a fresh one.` : r.status === 401 ? `Not a valid token. Run ${LOGIN_COMMAND} and use the link or the token it prints.` : `Login failed (${r.status}).`;
  if (state.locked) renderLock(why);
  else toast(why);
  return false;
}
async function lockNow() {
  await apiPost('/api/logout');
  lock();
  toast(`Locked. Run ${LOGIN_COMMAND} to open it again.`);
}
// tokenFromHash: the CLI's link carries the token in the fragment; it is redeemed once and
// scrubbed from the URL and the history entry so it never lingers in a tab or a bookmark.
function tokenFromHash() {
  const m = /^#token=([A-Za-z0-9]+)$/.exec(location.hash || '');
  if (!m) return '';
  try { history.replaceState(null, '', '#sessions'); } catch (e) { location.hash = '#sessions'; }
  return m[1];
}
async function boot() {
  try {
    const st = await api('/api/auth');
    state.authEnabled = !!st.enabled;
    $('#lockBtn').hidden = !state.authEnabled;
  } catch (e) { if (!(e instanceof AuthError)) console.warn(e); }
  restoreFleetPeriod();
  const applied = await loadSessions();
  if (!applied) return;
  if (location.hash.startsWith('#session/') || location.hash.startsWith('#insights') || location.hash === '#settings') route();
  else go('sessions');
}
(async () => {
  const tok = tokenFromHash();
  if (tok) {
    state.locked = true;
    await unlock(tok);
    return;
  }
  await boot();
})().catch(e => {
  if (e instanceof AuthError) return;
  $('#main').innerHTML = `<div class="empty"><h3>Cannot reach the todobem server</h3><p>${esc(e.message)}</p></div>`;
});
