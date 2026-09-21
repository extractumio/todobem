'use strict';
// The Settings page: the folders todobem reads sessions from, one section per source (Codex
// homes, Claude Code homes), saved to ~/.todobem/settings.json through /api/settings and
// applied live. This file defines functions and the text table only; every other name it uses
// ($, esc, icon, state, api, apiPost, toast, loadSessions, plural, SOURCES, SOURCE_ORDER, …) is
// an app.js or filter.js global resolved at call time, so load order does not matter.
//
// The page keeps a draft (the folder lists as edited) next to what the server last reported;
// typing only updates the draft and the Save button, add/remove re-render the rows, Save posts
// the draft as JSON and replaces both with the server's answer. A source with no folders is
// off; at least one folder overall is needed (the server refuses an empty save).

const SETTINGS_TEXT = {
  page: {
    eyebrow: 'todobem on this machine',
    title: 'Settings',
    subtitle: 'Where todobem looks for sessions. Each source has its own folders; a source with no folder is off.',
  },
  sources: {
    codex: {
      title: 'Codex session folders',
      subtitle: 'Each entry is a Codex home: the folder that contains sessions/ (and archived_sessions/). Sessions of every folder appear in one list; a rollout present in two folders is shown once, from the first.',
      placeholder: '~/.codex',
      noSessionsDir: 'no sessions/ folder here — point at a Codex home, the folder that contains sessions/',
      off: 'No Codex folder: Codex sessions are not read.',
    },
    claude: {
      title: 'Claude Code session folders',
      subtitle: 'Each entry is a Claude Code home: the folder that contains projects/. Sessions of every folder appear in one list; a session present in two folders is shown once, from the first.',
      placeholder: '~/.claude',
      noSessionsDir: 'no projects/ folder here — point at a Claude Code home, the folder that contains projects/',
      off: 'No Claude Code folder: Claude Code sessions are not read.',
    },
  },
  homes: {
    add: 'Add folder',
    remove: 'Remove',
    save: 'Save',
    saved: 'Saved',
    reload: 'Reload',
    savedTo: path => `Saved to ${path}. Without the file todobem reads ~/.codex and ~/.claude.`,
    pinned: 'Set by -codex / -claude on the command line for this run. Start todobem without them to manage the folders here.',
    unsaved: 'not saved yet',
    status: {
      ok: n => plural(n, 'session'),
      elsewhere: n => `${n} more also in an earlier folder, shown from there`,
      missing: 'folder not found — an unmounted drive is fine, its sessions appear when it is back',
    },
    note: 'A folder on a slow or removable volume slows every scan (the list is rescanned every 20 s). Only the session logs under each home are read; nothing is written there.',
  },
  states: {
    loading: 'Loading settings…',
    error: 'Could not load the settings.',
    saving: 'Saving…',
    savedToast: n => `Settings saved · ${plural(n, 'folder')} · sessions rescanned.`,
    saveFailed: why => `Could not save: ${why}`,
  },
  // The Servers section: the paired agents (todobem -agent on other machines) this hub pulls
  // sessions from. Bearers are never shown; the agents file holds them (docs/AGENT-MODE.md §5).
  servers: {
    title: 'Paired servers',
    subtitle: 'Machines running todobem as an agent. This hub dials them — and nothing else — for their session lists, models and facts; their sessions appear in the list with a host chip and under the Host filter.',
    none: 'No server paired. Start `todobem -agent` on a machine and paste its pairing string here, or run `todobem hub add` on this one.',
    columns: ['Server', 'Address', 'Status', 'Build', 'Sessions', 'Facts', 'Bearer until'],
    add: 'Add server',
    adding: 'Pairing…',
    pairing: 'Pairing string (todobem-agent://…)',
    name: 'Name (default: its hostname)',
    addr: 'Address host:port (default: from the string)',
    poll: 'Poll now',
    doctor: 'Doctor',
    rotate: 'Rotate key',
    remove: 'Remove',
    removeAsk: name => `Remove ${name} from this hub? Its sessions leave the list; the agent keeps running.`,
    rotateAsk: name => `Rotate the key on ${name}? The hub receives a new bearer; the old one dies on its first use.`,
    doctorTitle: name => `Doctor · ${name}`,
    close: 'Close',
    status: {
      ok: t => `ok · ${ago(t)}`,
      never: 'not reached yet',
      unreachable: (t, why) => `unreachable since ${stamp(t)} · ${why}`,
      protocol: 'incompatible: the agent does not speak protocol v1',
      models: 'build differs: models unavailable until one side is upgraded',
      facts: 'build differs: facts unavailable',
      expiring: days => `bearer expires in ${days} d — rotate`,
    },
    savedIn: path => `Recorded in ${path} (bearers inside; mode 0600). The command line does the same: todobem hub add | list | remove | doctor | rotate.`,
    failed: why => `Could not do that: ${why}`,
    fleet: n => `${n} paired`,
  },
};

function serversState() {
  const st = settingsState();
  if (!st.servers) st.servers = { data: null, error: '', busy: '', pairing: '', name: '', addr: '', doctor: null };
  return st.servers;
}

async function loadServers() {
  const sv = serversState();
  const navigation = navigationRequest;
  try {
    const data = await api('/api/fleet');
    if (navigation !== navigationRequest) return;
    sv.data = data;
    sv.error = '';
  } catch (e) {
    if (navigation !== navigationRequest || e instanceof AuthError) return;
    sv.data = sv.data || { agents: [] };
    sv.error = e.message;
  }
  renderSettings();
}

// serversInput: a keystroke in one of the add-server fields updates the draft only.
function serversInput(field, value) {
  const sv = serversState();
  if (field in sv) sv[field] = value;
}

// serversAction runs one of the Servers section's actions; every one is a JSON POST answered
// with the fresh status of every agent.
async function serversAction(a, el) {
  const sv = serversState();
  const T = SETTINGS_TEXT.servers;
  const name = el && el.dataset ? el.dataset.name : '';
  if (a === 'set-srv-doctor-close') {
    sv.doctor = null;
    renderSettings();
    return;
  }
  if (a === 'set-srv-doctor') {
    sv.busy = 'doctor:' + name;
    renderSettings();
    try {
      const rep = await api('/api/fleet/doctor?name=' + encodeURIComponent(name));
      sv.doctor = { name, text: JSON.stringify(rep, null, 2) };
      sv.error = '';
    } catch (e) {
      if (e instanceof AuthError) return;
      sv.error = T.failed(e.message);
    }
    sv.busy = '';
    renderSettings();
    return;
  }
  let path = '', body = {};
  if (a === 'set-srv-add') {
    if (!sv.pairing.trim()) return;
    path = '/api/fleet/add';
    body = { pairing: sv.pairing.trim(), name: sv.name.trim(), addr: sv.addr.trim() };
  } else if (a === 'set-srv-remove') {
    if (!confirm(T.removeAsk(name))) return;
    path = '/api/fleet/remove';
    body = { name };
  } else if (a === 'set-srv-rotate') {
    if (!confirm(T.rotateAsk(name))) return;
    path = '/api/fleet/rotate';
    body = { name };
  } else if (a === 'set-srv-poll') {
    path = '/api/fleet/poll';
    body = { name };
  } else return;
  sv.busy = a + ':' + name;
  renderSettings();
  let r;
  try {
    r = await apiPost(path, body);
  } catch (e) {
    sv.busy = '';
    if (e instanceof AuthError) return;
    sv.error = T.failed(e.message);
    renderSettings();
    return;
  }
  sv.busy = '';
  if (r.status === 401) {
    lock();
    return;
  }
  if (!r.ok) {
    sv.error = T.failed(postFailure(r));
    renderSettings();
    return;
  }
  sv.error = '';
  sv.data = r.payload; // every action answers with the fresh status of every agent
  if (a === 'set-srv-add') {
    sv.pairing = '';
    sv.name = '';
    sv.addr = '';
  }
  renderSettings();
  refreshSessionList(); // the session list gains or loses a host, or gets fresh rows
}

// serverStatusHTML is the Status cell: reachability first, then compatibility, then the bearer.
function serverStatusHTML(a) {
  const T = SETTINGS_TEXT.servers.status;
  const notes = [];
  if (a.since) notes.push(['warn', T.unreachable(a.since, a.error || '')]);
  else if (a.last_ok) notes.push(['', T.ok(a.last_ok)]);
  else notes.push(['', T.never]);
  if (a.protocol < 0) notes.push(['warn', T.protocol]);
  else if (a.reached && !a.models_ok) notes.push(['warn', T.models]);
  if (a.reached && a.models_ok && !a.facts_ok) notes.push(['warn', T.facts]);
  const days = Math.floor((a.expires - Date.now()) / DAY_MS);
  if (a.expires && days < 30) notes.push(['warn', T.expiring(Math.max(0, days))]);
  return notes.map(([cls, text]) => `<span class="home-status ${cls}">${esc(text)}</span>`).join('<br>');
}

function serversSectionHTML(st) {
  const T = SETTINGS_TEXT.servers;
  const sv = serversState();
  const agents = (sv.data && sv.data.agents) || [];
  const busy = sv.busy;
  const row = a => `<tr><td><strong>${esc(a.name)}</strong></td><td class="mono">${esc(a.addr)}</td><td>${serverStatusHTML(a)}</td><td class="mono">${esc(a.version || '—')}</td><td class="mono">${a.sessions}</td><td class="mono">${a.digested}/${a.sessions}</td><td class="mono">${a.expires ? dayLabel(a.expires) : '—'}</td><td class="server-actions"><button class="btn small ghost" data-action="set-srv-poll" data-name="${esc(a.name)}" ${busy ? 'disabled' : ''}>${esc(T.poll)}</button><button class="btn small ghost" data-action="set-srv-doctor" data-name="${esc(a.name)}" ${busy ? 'disabled' : ''}>${esc(T.doctor)}</button><button class="btn small ghost" data-action="set-srv-rotate" data-name="${esc(a.name)}" ${busy ? 'disabled' : ''}>${esc(T.rotate)}</button><button class="btn small ghost" data-action="set-srv-remove" data-name="${esc(a.name)}" ${busy ? 'disabled' : ''}>${icon('close', true)}${esc(T.remove)}</button></td></tr>`;
  const table = agents.length
    ? `<table class="fleet-table servers-table"><thead><tr>${T.columns.map(c => `<th>${esc(c)}</th>`).join('')}<th></th></tr></thead><tbody>${agents.map(row).join('')}</tbody></table>`
    : `<p class="muted-note">${icon('info', true)}${esc(T.none)}</p>`;
  const adding = busy.startsWith('set-srv-add');
  const form = `<div class="home-rows server-add"><input class="lock-input" type="text" data-server="pairing" value="${esc(sv.pairing)}" placeholder="${esc(T.pairing)}" aria-label="${esc(T.pairing)}" autocomplete="off" spellcheck="false"><div class="home-row"><input class="lock-input" type="text" data-server="name" value="${esc(sv.name)}" placeholder="${esc(T.name)}" aria-label="${esc(T.name)}" autocomplete="off" spellcheck="false"><input class="lock-input" type="text" data-server="addr" value="${esc(sv.addr)}" placeholder="${esc(T.addr)}" aria-label="${esc(T.addr)}" autocomplete="off" spellcheck="false"></div><div class="settings-actions"><button class="btn primary" type="button" data-action="set-srv-add" ${busy ? 'disabled' : ''}>${icon('check', true)}${esc(adding ? T.adding : T.add)}</button></div></div>`;
  const doctor = sv.doctor ? `<details class="json-details" open><summary>${esc(T.doctorTitle(sv.doctor.name))} <button class="btn small ghost" type="button" data-action="set-srv-doctor-close">${esc(T.close)}</button></summary><pre class="event-log">${esc(sv.doctor.text)}</pre></details>` : '';
  const error = sv.error ? `<p class="lock-note" role="alert">${esc(sv.error)}</p>` : '';
  const note = sv.data && sv.data.path ? `<p class="muted-note">${icon('info', true)}${esc(T.savedIn(sv.data.path))}</p>` : '';
  return `<section class="card settings-source" aria-labelledby="set-servers"><div class="card-head"><div><h2 id="set-servers">${esc(T.title)}${agents.length ? ` <span class="count">${esc(T.fleet(agents.length))}</span>` : ''}</h2><p>${esc(T.subtitle)}</p></div></div><div class="settings-body">${error}${table}${doctor}${form}${note}</div></section>`;
}

function settingsState() {
  if (!state.settings) state.settings = { loading: false, error: '', data: null, draft: {}, saving: false, request: 0 };
  return state.settings;
}

// draftOf: the server's answer as a draft, one list per source.
function draftOf(data) {
  const draft = {};
  for (const src of data.sources) draft[src.name] = src.homes.map(h => h.path);
  for (const k of SOURCE_ORDER) if (!draft[k]) draft[k] = [];
  return draft;
}

// draftFolders: the non-blank entries of a source's draft list.
function draftFolders(st, source) {
  return (st.draft[source] || []).map(v => v.trim()).filter(Boolean);
}

function draftCount(st) {
  return SOURCE_ORDER.reduce((n, k) => n + draftFolders(st, k).length, 0);
}

// settingsDirty: the draft differs from what the server has, blank rows aside (an added row that
// was never typed in is not a change).
function settingsDirty(st) {
  if (!st.data) return false;
  const saved = draftOf(st.data);
  return SOURCE_ORDER.some(k => {
    const a = saved[k], b = draftFolders(st, k);
    return a.length !== b.length || a.some((v, i) => v !== b[i]);
  });
}

async function loadSettings() {
  const st = settingsState();
  const navigation = navigationRequest;
  const request = ++st.request;
  st.loading = true;
  st.error = '';
  renderSettings();
  loadServers();
  try {
    const data = await api('/api/settings');
    if (request !== st.request) return;
    st.loading = false;
    if (navigation !== navigationRequest) return;
    st.data = data;
    st.draft = draftOf(data);
  } catch (e) {
    if (request !== st.request) return;
    st.loading = false;
    if (navigation !== navigationRequest || e instanceof AuthError) return;
    st.error = e.message;
  }
  renderSettings();
}

async function saveSettings() {
  const st = settingsState();
  if (!st.data || st.data.pinned || st.saving) return;
  const navigation = navigationRequest;
  const request = ++st.request;
  st.loading = false;
  st.saving = true;
  renderSettings();
  let r;
  try {
    r = await apiPost('/api/settings', { codex_homes: st.draft.codex || [], claude_homes: st.draft.claude || [] });
  } catch (e) {
    st.saving = false;
    if (request !== st.request) {
      if (navigation === navigationRequest) renderSettings();
      return;
    }
    if (navigation !== navigationRequest) return;
    if (e instanceof AuthError) return;
    st.error = SETTINGS_TEXT.states.saveFailed(e.message);
    renderSettings();
    return;
  }
  st.saving = false;
  if (request !== st.request) {
    if (navigation === navigationRequest) renderSettings();
    return;
  }
  if (navigation !== navigationRequest) return;
  if (r.status === 401) {
    lock();
    return;
  }
  if (!r.ok) {
    st.error = SETTINGS_TEXT.states.saveFailed(postFailure(r));
    renderSettings();
    return;
  }
  st.error = '';
  st.data = r.payload;
  st.draft = draftOf(r.payload);
  renderSettings();
  toast(SETTINGS_TEXT.states.savedToast(draftCount(st)));
  refreshSessionList(); // the session list is the new folders' now
}

// postFailure: why a JSON POST failed, as the page tells it (the server's error line or the status).
function postFailure(r) {
  return r.payload && r.payload.error ? r.payload.error : (r.text || '').trim() || `HTTP ${r.status}`;
}

function settingsAction(a, el) {
  const st = settingsState();
  if (a === 'settings') return go('settings');
  if (a.startsWith('set-srv-')) return serversAction(a, el);
  if (a === 'set-add') {
    const source = el.dataset.source;
    if (!st.draft[source]) st.draft[source] = [];
    st.draft[source].push('');
    renderSettings();
    const inputs = $$(`[data-source="${source}"][data-home-index]`);
    const last = inputs[inputs.length - 1];
    if (last && last.focus) last.focus();
    return;
  }
  if (a === 'set-remove') {
    const list = st.draft[el.dataset.source];
    if (list) list.splice(Number(el.dataset.index), 1);
    renderSettings();
    return;
  }
  if (a === 'set-save') return saveSettings();
  if (a === 'set-reload') return loadSettings();
}

// settingsInput: a keystroke in a folder field updates the draft and the Save button only; the
// rows are not re-rendered (the field would lose focus).
function settingsInput(source, index, value) {
  const st = settingsState();
  const list = st.draft[source];
  if (!list || index < 0 || index >= list.length) return;
  list[index] = value;
  const save = $('#setSave');
  if (save) save.disabled = !settingsDirty(st) || st.saving;
}

/* ---------- rendering ---------- */
function renderSettings() {
  if (state.page !== 'settings') return;
  $('#main').innerHTML = settingsPage();
  $$('[data-icon]').forEach(el => el.innerHTML = icon(el.dataset.icon));
}

function homeStatusHTML(st, source, value) {
  const T = SETTINGS_TEXT.homes;
  const section = st.data && st.data.sources.find(s => s.name === source);
  const saved = section && section.homes.find(h => h.path === value.trim());
  if (!saved) return `<span class="home-status">${esc(T.unsaved)}</span>`;
  let status;
  if (saved.status === 'ok') status = T.status.ok(saved.sessions) + (saved.elsewhere ? ` · ${T.status.elsewhere(saved.elsewhere)}` : '');
  else if (saved.status === 'no_sessions_dir') status = SETTINGS_TEXT.sources[source].noSessionsDir;
  else status = T.status[saved.status] || saved.status;
  return `<span class="home-status ${saved.status === 'ok' ? '' : 'warn'}"><span class="mono">${esc(saved.resolved)}</span> · ${esc(status)}</span>`;
}

// sourceSectionHTML: one source's card — its rows, its Add button, or the note that it is off.
function sourceSectionHTML(st, source) {
  const T = SETTINGS_TEXT;
  const S = T.sources[source];
  const pinned = st.data.pinned;
  const list = st.draft[source] || [];
  const canRemove = !pinned && draftCount(st) > 1;
  const rows = list.map((value, i) => `<div class="home-row"><input class="lock-input" type="text" data-source="${source}" data-home-index="${i}" value="${esc(value)}" placeholder="${esc(S.placeholder)}" aria-label="${esc(S.title)} ${i + 1}" autocomplete="off" spellcheck="false" ${pinned ? 'disabled' : ''}><button class="btn small ghost" type="button" data-action="set-remove" data-source="${source}" data-index="${i}" aria-label="${esc(T.homes.remove)} ${esc(S.title.toLowerCase())} ${i + 1}" ${canRemove ? '' : 'disabled'}>${icon('close', true)}${esc(T.homes.remove)}</button>${homeStatusHTML(st, source, value)}</div>`).join('');
  const off = list.length ? '' : `<p class="muted-note">${icon('info', true)}${esc(S.off)}</p>`;
  const add = pinned ? '' : `<div class="settings-actions"><button class="btn ghost" type="button" data-action="set-add" data-source="${source}">${esc(T.homes.add)}</button></div>`;
  return `<section class="card settings-source" aria-labelledby="set-${source}"><div class="card-head"><div><h2 id="set-${source}"><i class="source-mark source-${source}" aria-hidden="true"></i>${esc(S.title)}</h2><p>${esc(S.subtitle)}</p></div></div><div class="settings-body"><div class="home-rows">${rows}</div>${off}${add}</div></section>`;
}

function settingsPage() {
  const st = settingsState();
  const T = SETTINGS_TEXT;
  const head = `<section class="page-heading"><div><div class="eyebrow">${esc(T.page.eyebrow)}</div><h1>${esc(T.page.title)}</h1><p class="subtitle">${esc(T.page.subtitle)}</p></div></section>`;
  let body;
  if (st.loading && !st.data) {
    body = `<div class="loading">${esc(T.states.loading)}</div>`;
  } else if (!st.data) {
    body = `<div class="empty"><h3>${esc(T.states.error)}</h3><p>${esc(st.error)}</p><p><button class="btn" data-action="set-reload">${icon('refresh', true)}${esc(T.homes.reload)}</button></p></div>`;
  } else {
    const pinned = st.data.pinned;
    const dirty = settingsDirty(st);
    const sections = SOURCE_ORDER.map(k => sourceSectionHTML(st, k)).join('');
    const foot = pinned
      ? `<p class="muted-note">${icon('info', true)}${esc(T.homes.pinned)}</p>`
      : `<div class="settings-actions"><button class="btn primary" type="submit" id="setSave" data-action="set-save" ${!dirty || st.saving ? 'disabled' : ''}>${icon('check', true)}${esc(st.saving ? T.states.saving : dirty ? T.homes.save : T.homes.saved)}</button></div><p class="muted-note">${icon('info', true)}${esc(T.homes.savedTo(st.data.path))}</p>`;
    body = `<form id="settingsForm" class="settings-form">${st.error ? `<p class="lock-note" role="alert">${esc(st.error)}</p>` : ''}${sections}<section class="card"><div class="settings-body settings-foot">${foot}<p class="muted-note">${esc(T.homes.note)}</p></div></section></form>${serversSectionHTML(st)}`;
  }
  return head + body + footer();
}
