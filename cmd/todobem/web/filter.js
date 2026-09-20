'use strict';
// The period + project filter shared by the Sessions list and the Insights report: a select of
// periods (with date fields and Apply for custom dates) and a select of projects with their
// session counts in the period. A page keeps a filter value in its state and hands it here to
// render, to update from a control, and to test sessions against. This file defines functions
// and the text table only; $, esc, state, MONTHS, plural, shortPath, … are app.js globals.
//
// A filter value: { kind, from, to, cwd, host, sources } — kind one of FILTER_KINDS (a page may
// add its own, Insights adds 'session'), from/to date-input values (YYYY-MM-DD) used when kind
// is 'custom', cwd the project ('' = all), host the machine ('' = all, LOCAL_HOST = this one,
// else a paired agent's name), sources the session sources in view ({codex: true, claude:
// false}; a source not named is in view). Control ids are <prefix>Period, <prefix>Project,
// <prefix>Host, <prefix>From, <prefix>To and <prefix>Src<Source> (the source checkboxes,
// rendered only when the list holds more than one source; the Host select likewise only when
// the list holds more than one host); the Apply button is data-action="filter-apply"
// data-prefix="<prefix>".
//
// A session is inside a period by its last update, the way the report selects sessions
// (insights.Params.InPeriod), so the counts next to the projects match both pages.

// SOURCES names the session-log formats todobem reads (model.Session.source) with the colour
// of their mark in the session list; the colours live in app.css as --src-<source>.
const SOURCES = {
  codex: { name: 'Codex', field: 'SrcCodex' },
  claude: { name: 'Claude Code', field: 'SrcClaude' },
};
const SOURCE_ORDER = Object.keys(SOURCES);
// sourceOf: a session's source; a row from an older server without the field is Codex.
const sourceOf = s => (s && SOURCES[s.source]) ? s.source : 'codex';
// sourceName: the format's name for a row ("Claude Code", "Codex").
const sourceName = s => SOURCES[sourceOf(s)].name;
// sourceMark: the coloured square that marks a session's source, with its name as the tooltip.
const sourceMark = s => `<i class="source-mark source-${sourceOf(s)}" title="${esc(sourceName(s))}" aria-label="${esc(sourceName(s))}"></i>`;

// Hosts: a session read by a paired agent carries its name in `host` (and as the `@host` suffix
// of its id); a session of this machine has none. LOCAL_HOST is the filter value that names
// this machine (the server's insights.LocalHost).
const LOCAL_HOST = '.';
const hostOf = s => (s && s.host) || '';
// hostsPresent lists the hosts the session list holds: '' (this machine) first when present,
// then the agents by name.
function hostsPresent(sessions) {
  const seen = new Set(sessions.map(hostOf));
  const names = [...seen].filter(Boolean).sort();
  return seen.has('') ? ['', ...names] : names;
}
// hostOn: whether a filter keeps sessions of a host.
function hostOn(f, host) {
  if (!f.host) return true;
  return f.host === LOCAL_HOST ? host === '' : f.host === host;
}
// hostHue: every paired agent gets its own hue, 60° apart around the colour wheel in the order
// of the hosts present (alphabetical, so a host keeps its colour between renders); the wheel
// starts away from red, which would read as an alarm. The seventh host wraps around.
function hostHue(host) {
  const hosts = hostsPresent(state.sessions || []).filter(Boolean);
  const i = Math.max(0, hosts.indexOf(host));
  return (200 + i * 60) % 360;
}
// hostBadge: the chip that names a paired agent, framed in its hue.
const hostBadge = host => `<span class="host-mark" style="--host-hue:${hostHue(host)}" title="${esc(FILTER_TEXT.hostNote(host))}">${esc(host)}</span>`;
// hostMark: the chip on a list row; nothing for this machine.
const hostMark = s => hostOf(s) ? hostBadge(hostOf(s)) : '';

const FILTER_TEXT = {
  period: 'Period',
  project: 'Project',
  allProjects: 'All projects',
  host: 'Host',
  allHosts: 'All hosts',
  thisMachine: 'This machine',
  hostsCount: n => `${n} hosts`,
  hostNote: host => `Read on ${host} by its todobem agent`,
  projectFilter: 'Type to filter by name or host',
  noMatch: 'No project matches',
  sources: 'Sources',
  from: 'From',
  to: 'To',
  apply: 'Apply',
  periods: {
    '1d': 'Last 24 hours',
    '7d': 'Last 7 days',
    '30d': 'Last 30 days',
    '90d': 'Last 90 days',
    all: 'All time',
    custom: 'Custom dates…',
  },
};
const FILTER_KINDS = ['1d', '7d', '30d', '90d', 'all', 'custom'];
const FILTER_DAYS = { '1d': 1, '7d': 7, '30d': 30, '90d': 90 };
const DAY_MS = 24 * 3600e3;

function newFilter(kind = '30d', cwd = '') {
  return { kind, from: '', to: '', cwd, host: '', sources: {} };
}

// sourceOn: whether a filter keeps sessions of a source (on unless switched off).
function sourceOn(f, source) {
  return !f.sources || f.sources[source] !== false;
}

// sourcesPresent lists the sources the session list holds, in SOURCE_ORDER.
function sourcesPresent(sessions) {
  const seen = new Set(sessions.map(sourceOf));
  return SOURCE_ORDER.filter(k => seen.has(k));
}

// dayStart / dayEnd turn a date input value (YYYY-MM-DD) into local-time ms.
function dayStart(v) {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(v || '');
  if (!m) return 0;
  return new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]), 0, 0, 0, 0).getTime();
}

function dayEnd(v) {
  const s = dayStart(v);
  return s ? s + DAY_MS - 1 : 0;
}

function dateInputValue(t) {
  const d = new Date(t);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
}

function dayLabel(t) {
  const d = new Date(t);
  return `${d.getDate()} ${MONTHS[d.getMonth()]} ${d.getFullYear()}`;
}

// filterRange is the period's [from, to] in ms: the last N days up to now, everything, or the
// custom dates (inclusive). A kind this file does not know (Insights' 'session') is unbounded.
function filterRange(f, now = Date.now()) {
  if (FILTER_DAYS[f.kind]) return { from: now - FILTER_DAYS[f.kind] * DAY_MS, to: now };
  if (f.kind === 'custom') return { from: dayStart(f.from), to: dayEnd(f.to) || now };
  return { from: 0, to: now };
}

// inView: whether a session is inside the filter's facets — source, host, project and period —
// leaving out the facet named by `except` (a facet's own counts are taken with that facet open,
// so every option shows what choosing it would keep).
function inView(f, s, now, except = '') {
  if (except !== 'source' && !sourceOn(f, sourceOf(s))) return false;
  if (except !== 'host' && !hostOn(f, hostOf(s))) return false;
  if (except !== 'cwd' && f.cwd && s.cwd !== f.cwd) return false;
  if (except !== 'period') {
    const { from, to } = filterRange(f, now);
    if (s.updated < from || s.updated > to) return false;
  }
  return true;
}

// filterKeeps: whether a session summary is inside the filter.
function filterKeeps(f, s, now = Date.now()) {
  return inView(f, s, now);
}

// filterProjects lists the projects of the sessions (of the sources and hosts in view) with
// their counts, most sessions in the period first: [[cwd, { all, period, hosts }], …]. hosts
// holds the distinct hosts the path was seen on: the same path on several machines is one
// project entry (the log records a path, not a repository), and the Host select tells them apart.
function filterProjects(f, sessions, now = Date.now()) {
  const counts = new Map();
  const anyTime = { ...f, kind: 'all' }; // `all` counts the project's sessions of every period
  for (const s of sessions) {
    if (!inView(anyTime, s, now, 'cwd')) continue;
    if (!counts.has(s.cwd)) counts.set(s.cwd, { all: 0, period: 0, hosts: new Set() });
    const c = counts.get(s.cwd);
    c.all++;
    c.hosts.add(hostOf(s));
    if (inView(f, s, now, 'cwd')) c.period++;
  }
  return [...counts.entries()].sort((a, b) => b[1].period - a[1].period || b[1].all - a[1].all || a[0].localeCompare(b[0]));
}

// filterHosts counts the sessions of each host inside the other facets: [[host, n], …] in
// hostsPresent order ('' is this machine).
function filterHosts(f, sessions, now = Date.now()) {
  const counts = new Map(hostsPresent(sessions).map(h => [h, 0]));
  for (const s of sessions) {
    if (inView(f, s, now, 'host')) counts.set(hostOf(s), counts.get(hostOf(s)) + 1);
  }
  return [...counts.entries()];
}

// projectName is a project's label in the select: its last two path segments, no ellipsis and
// no leading slash (every entry is a path, the cut needs no mark).
function projectName(cwd) {
  const parts = (cwd || '').split('/').filter(Boolean);
  return parts.length ? parts.slice(-2).join('/') : cwd;
}

// projectHost names the host of a project seen on exactly one paired agent; '' for a project
// of this machine or one seen on several hosts (the entry says how many).
function projectHost(c) {
  if (c.hosts.size !== 1) return '';
  const [host] = c.hosts;
  return host || '';
}

// selectHTML renders one labelled select of the filter bar: options are [value, label] pairs,
// or [value, label, { host, name }] for an entry the page-drawn list shows as a host badge
// (its own column, so names line up) before `name`; the native control keeps the full label.
// filter, when given, is the placeholder of the search field the drawn list puts above the
// rows; primary marks the control the bar leads with (a tint of its own).
function selectHTML(id, label, options, selected, { filter = '', primary = false } = {}) {
  const opts = options.map(([v, text, extra]) => {
    const data = extra && extra.host ? ` data-host="${esc(extra.host)}" data-name="${esc(extra.name)}"` : '';
    return `<option value="${esc(v)}" ${v === selected ? 'selected' : ''}${data}>${esc(text)}</option>`;
  }).join('');
  const search = filter ? ` data-filter="${esc(filter)}"` : '';
  return `<label class="ctl${primary ? ' primary' : ''}">${esc(label)}<select class="select" id="${id}" aria-label="${esc(label)}"${search}>${opts}</select></label>`;
}

// hostSelectHTML renders the Host select when the list holds more than one host.
function hostSelectHTML(prefix, f, sessions, now = Date.now()) {
  const hosts = filterHosts(f, sessions, now);
  if (hosts.length < 2) return '';
  const T = FILTER_TEXT;
  const options = [['', T.allHosts], ...hosts.map(([h, n]) => [h === '' ? LOCAL_HOST : h, `${h === '' ? T.thisMachine : h} (${n})`])];
  return selectHTML(prefix + 'Host', T.host, options, f.host || '');
}

// filterSources counts the sessions of each source inside the period and the project (what a
// source's checkbox adds to or removes from the view): { codex: n, claude: n }.
function filterSources(f, sessions, now = Date.now()) {
  const counts = {};
  for (const s of sessions) {
    if (!inView(f, s, now, 'source')) continue;
    const k = sourceOf(s);
    counts[k] = (counts[k] || 0) + 1;
  }
  return counts;
}

// sourceTogglesHTML renders one checkbox per source present in the list, each with its mark
// and its count in the period — the legend is the control. One source alone needs no toggle.
function sourceTogglesHTML(prefix, f, sessions, now = Date.now()) {
  const present = sourcesPresent(sessions);
  if (present.length < 2) return '';
  const counts = filterSources(f, sessions, now);
  const boxes = present.map(k => {
    const src = SOURCES[k];
    const on = sourceOn(f, k);
    return `<label class="chip toggle source-toggle ${on ? 'on' : ''}"><input type="checkbox" id="${prefix}${src.field}" ${on ? 'checked' : ''} aria-label="${esc(src.name)}"><i class="source-mark source-${k}"></i>${esc(src.name)}<span class="count">${counts[k] || 0}</span></label>`;
  });
  return `<div class="ctl source-toggles" role="group" aria-label="${esc(FILTER_TEXT.sources)}">${boxes.join('')}</div>`;
}

// filterBarHTML renders the controls: the period select (kinds, plus a page's own labelled
// extras), the date fields when the period is custom, then `between` (a page's own control that
// belongs after the period, Insights' session picker), then the project select unless the page
// hides it (`project: false`), then the source toggles when `sessions` holds several sources.
function filterBarHTML(prefix, f, { extraPeriods = {}, between = '', project = true, projects = [], sessions = [] } = {}) {
  const T = FILTER_TEXT;
  const kinds = [...FILTER_KINDS.map(k => [k, T.periods[k]]), ...Object.entries(extraPeriods)];
  const periodSelect = selectHTML(prefix + 'Period', T.period, kinds, f.kind);
  const dates = f.kind === 'custom'
    ? `<label class="ctl">${esc(T.from)}<input type="date" id="${prefix}From" value="${esc(f.from)}"></label><label class="ctl">${esc(T.to)}<input type="date" id="${prefix}To" value="${esc(f.to)}"></label><button class="btn small" data-action="filter-apply" data-prefix="${prefix}">${esc(T.apply)}</button>`
    : '';
  // the select lists the projects by name (filterProjects orders them by sessions, which the
  // pages that pick a default keep), so a project is found where the eye expects it
  const shown = projects.filter(([cwd, c]) => c.period > 0 || cwd === f.cwd).sort((a, b) => projectName(a[0]).localeCompare(projectName(b[0]), undefined, { sensitivity: 'base' }) || a[0].localeCompare(b[0]));
  const projectName_ = ([cwd, c]) => `${projectName(cwd)} (${c.period})${c.hosts.size > 1 ? ' · ' + T.hostsCount(c.hosts.size) : ''}`;
  const projectOption = entry => {
    const host = projectHost(entry[1]);
    const name = projectName_(entry);
    return [entry[0], host ? `${host} · ${name}` : name, { host, name }];
  };
  const projectSelect = project
    ? selectHTML(prefix + 'Project', T.project, [['', T.allProjects], ...shown.map(projectOption)], f.cwd || '', { filter: T.projectFilter, primary: true })
    : '';
  const hostSelect = project ? hostSelectHTML(prefix, f, sessions) : '';
  return periodSelect + dates + between + projectSelect + hostSelect + sourceTogglesHTML(prefix, f, sessions);
}

// filterChange updates f from a control (field: Period | Project | From | To) and says whether
// the page should refresh: a new period or project does, a date only once Apply is pressed
// (filterApply). Switching to custom dates starts from the last 30 days.
function filterChange(f, field, value) {
  if (field === 'Period') {
    f.kind = value;
    if (value === 'custom') {
      const now = Date.now();
      f.from = f.from || dateInputValue(now - 30 * DAY_MS);
      f.to = f.to || dateInputValue(now);
    }
    return true;
  }
  if (field === 'Project') {
    f.cwd = value;
    return true;
  }
  if (field === 'Host') {
    f.host = value;
    return true;
  }
  const source = SOURCE_ORDER.find(k => SOURCES[k].field === field);
  if (source) {
    if (!f.sources) f.sources = {};
    f.sources[source] = value === true || value === 'true' || value === 'on';
    return true;
  }
  if (field === 'From' || field === 'To') {
    f[field.toLowerCase()] = value;
    return false;
  }
  return false;
}

// filterApply takes the date fields as they stand (the inputs may hold a value no change event
// delivered yet) and says whether the custom period is complete.
function filterApply(prefix, f) {
  const from = $('#' + prefix + 'From'), to = $('#' + prefix + 'To');
  if (from && from.value) f.from = from.value;
  if (to && to.value) f.to = to.value;
  f.kind = 'custom';
  return !!(dayStart(f.from) && dayStart(f.to));
}

// filterField: the field a control id addresses for a prefix ('' when it is not a filter control).
function filterField(prefix, id) {
  if (!id || !id.startsWith(prefix)) return '';
  const field = id.slice(prefix.length);
  const fields = ['Period', 'Project', 'Host', 'From', 'To', ...SOURCE_ORDER.map(k => SOURCES[k].field)];
  return fields.includes(field) ? field : '';
}

// A filter as an address. filterToLink writes a filter value as one base64url word for a URL
// (#insights?f=…, #sessions?f=…), so a set of filters opens by its address; the payload is
// compact JSON of the fields that are not the default (kind, custom dates, project, host, the
// sources switched off) and a page's own extras when set (Insights' session, the list's search
// and sort). filterFromLink reads one back: every field validated, an unknown or malformed field
// dropped, an extra handed back as text for the page to validate, null when the word is not a
// filter at all. kinds names the periods the reading page accepts.
const LINK_KINDS = kinds => kinds || FILTER_KINDS;
function filterToLink(f) {
  const payload = { kind: f.kind };
  if (f.kind === 'custom') {
    payload.from = f.from || '';
    payload.to = f.to || '';
  }
  if (f.cwd) payload.cwd = f.cwd;
  if (f.host) payload.host = f.host;
  const off = SOURCE_ORDER.filter(k => !sourceOn(f, k));
  if (off.length) payload.off = off;
  if (f.kind === 'session' && f.session) payload.session = f.session; // the session is that period's parameter, nothing else's
  if (f.search) payload.search = f.search;
  if (f.sort) payload.sort = f.sort;
  const bytes = new TextEncoder().encode(JSON.stringify(payload));
  let binary = '';
  for (const b of bytes) binary += String.fromCharCode(b);
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}
function filterFromLink(word, kinds) {
  let payload;
  try {
    const b64 = String(word || '').replace(/-/g, '+').replace(/_/g, '/');
    const binary = atob(b64 + '='.repeat((4 - b64.length % 4) % 4));
    const bytes = Uint8Array.from(binary, c => c.charCodeAt(0));
    payload = JSON.parse(new TextDecoder().decode(bytes));
  } catch (e) {
    return null;
  }
  if (!payload || typeof payload !== 'object' || !LINK_KINDS(kinds).includes(payload.kind)) return null;
  const date = v => /^\d{4}-\d{2}-\d{2}$/.test(v || '') ? v : '';
  const text = v => typeof v === 'string' ? v : '';
  const f = newFilter(payload.kind, text(payload.cwd));
  f.from = date(payload.from);
  f.to = date(payload.to);
  f.host = text(payload.host);
  for (const k of Array.isArray(payload.off) ? payload.off : []) if (SOURCES[k]) f.sources[k] = false;
  if (text(payload.session)) f.session = payload.session;
  if (text(payload.search)) f.search = payload.search;
  if (text(payload.sort)) f.sort = payload.sort;
  return f;
}
