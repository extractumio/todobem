'use strict';
// The period + project filter shared by the Sessions list and the Insights report: a select of
// periods (with date fields and Apply for custom dates) and a select of projects with their
// session counts in the period. A page keeps a filter value in its state and hands it here to
// render, to update from a control, and to test sessions against. This file defines functions
// and the text table only; $, esc, state, MONTHS, plural, shortPath, … are app.js globals.
//
// A filter value: { kind, from, to, cwd, sources } — kind one of FILTER_KINDS (a page may add
// its own, Insights adds 'session'), from/to date-input values (YYYY-MM-DD) used when kind is
// 'custom', cwd the project ('' = all), sources the session sources in view ({codex: true,
// claude: false}; a source not named is in view). Control ids are <prefix>Period,
// <prefix>Project, <prefix>From, <prefix>To and <prefix>Src<Source> (the source checkboxes,
// rendered only when the list holds more than one source); the Apply button is
// data-action="filter-apply" data-prefix="<prefix>".
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

const FILTER_TEXT = {
  period: 'Period',
  project: 'Project',
  allProjects: 'All projects',
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
  return { kind, from: '', to: '', cwd, sources: {} };
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

// filterKeeps: whether a session summary is inside the filter (source, project and period).
function filterKeeps(f, s, now = Date.now()) {
  if (!sourceOn(f, sourceOf(s))) return false;
  if (f.cwd && s.cwd !== f.cwd) return false;
  const { from, to } = filterRange(f, now);
  return s.updated >= from && s.updated <= to;
}

// filterProjects lists the projects of the sessions (of the sources in view) with their
// counts, most sessions in the period first: [[cwd, { all, period }], …].
function filterProjects(f, sessions, now = Date.now()) {
  const { from, to } = filterRange(f, now);
  const counts = new Map();
  for (const s of sessions) {
    if (!sourceOn(f, sourceOf(s))) continue;
    if (!counts.has(s.cwd)) counts.set(s.cwd, { all: 0, period: 0 });
    const c = counts.get(s.cwd);
    c.all++;
    if (s.updated >= from && s.updated <= to) c.period++;
  }
  return [...counts.entries()].sort((a, b) => b[1].period - a[1].period || b[1].all - a[1].all || a[0].localeCompare(b[0]));
}

// filterSources counts the sessions of each source inside the period and the project (what a
// source's checkbox adds to or removes from the view): { codex: n, claude: n }.
function filterSources(f, sessions, now = Date.now()) {
  const { from, to } = filterRange(f, now);
  const counts = {};
  for (const s of sessions) {
    if (f.cwd && s.cwd !== f.cwd) continue;
    if (s.updated < from || s.updated > to) continue;
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
  const periodSelect = `<label class="ctl">${esc(T.period)}<select class="select" id="${prefix}Period" aria-label="${esc(T.period)}">${kinds.map(([k, label]) => `<option value="${esc(k)}" ${k === f.kind ? 'selected' : ''}>${esc(label)}</option>`).join('')}</select></label>`;
  const dates = f.kind === 'custom'
    ? `<label class="ctl">${esc(T.from)}<input type="date" id="${prefix}From" value="${esc(f.from)}"></label><label class="ctl">${esc(T.to)}<input type="date" id="${prefix}To" value="${esc(f.to)}"></label><button class="btn small" data-action="filter-apply" data-prefix="${prefix}">${esc(T.apply)}</button>`
    : '';
  const shown = projects.filter(([cwd, c]) => c.period > 0 || cwd === f.cwd);
  const projectSelect = project
    ? `<label class="ctl">${esc(T.project)}<select class="select" id="${prefix}Project" aria-label="${esc(T.project)}"><option value="" ${f.cwd ? '' : 'selected'}>${esc(T.allProjects)}</option>${shown.map(([cwd, c]) => `<option value="${esc(cwd)}" ${cwd === f.cwd ? 'selected' : ''}>${esc(shortPath(cwd))} (${c.period})</option>`).join('')}</select></label>`
    : '';
  return periodSelect + dates + between + projectSelect + sourceTogglesHTML(prefix, f, sessions);
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
  const fields = ['Period', 'Project', 'From', 'To', ...SOURCE_ORDER.map(k => SOURCES[k].field)];
  return fields.includes(field) ? field : '';
}
