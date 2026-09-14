'use strict';
// The Insights page: a period report over the sessions of one project (docs/INSIGHTS-SPEC.md
// §7). This file defines functions and the text table only; every other name it uses ($, esc,
// fmt, fmtTok, stamp, icon, state, api, go, toast, focusInterval, loadSessions, MONTHS, …) is an
// app.js global resolved at call time, so load order does not matter.
//
// Every string a reader sees lives in INSIGHT_TEXT (plain English, rule 10 of the spec): short
// sentences, common words, numbers with units, "you" and "the agent".

const INSIGHT_GROUPS = ['you_and_the_agent', 'sub_agents', 'failures_and_retries', 'long_tool_runs', 'context_size', 'models_and_effort', 'not_measured'];
// Each group carries one of the timeline's phase colours (app.css variables) so a reader can tell
// the groups apart on a long page: failures are the error red, long runs the Testing yellow, …
const INSIGHT_TONES = { you_and_the_agent: 'wait_user', sub_agents: 'wait_worker', failures_and_retries: 'error', long_tool_runs: 'test', context_size: 'compaction', models_and_effort: 'llm', not_measured: 'unknown' };
// ERROR_RED is the timeline's red for failed tool calls and interrupted turns (app.js markers).
const ERROR_RED = '#d76368';
function toneColor(id) {
  const key = INSIGHT_TONES[id] || 'unknown';
  if (key === 'error') return ERROR_RED;
  const phase = typeof PHASES === 'object' && PHASES[key];
  return (phase && phase.color) || '#98a4ad';
}
const toneStyle = id => `--tone:${toneColor(id)}`;

const INSIGHT_TEXT = {
  page: {
    eyebrow: 'Codex insights on this machine',
    title: 'Insights',
    subtitle: 'Where your sessions lose time and tokens, with the evidence.',
  },
  controls: {
    period: 'Period',
    project: 'Project',
    allProjects: 'All projects',
    session: 'Session',
    from: 'From',
    to: 'To',
    apply: 'Apply',
    orderBy: 'Order by',
    time: 'Time',
    tokens: 'Tokens',
    regenerate: 'Regenerate',
    analyze: 'Analyze',
    cancel: 'Cancel',
    includeLive: 'Include live sessions',
  },
  periods: {
    '7d': 'Last 7 days',
    '30d': 'Last 30 days',
    '90d': 'Last 90 days',
    all: 'All time',
    custom: 'Custom dates…',
    session: 'One session…',
  },
  report: {
    reportFor: 'Report for',
    sessions: n => plural(n, 'closed session'),
    generated: 'generated',
    stale: 'The sessions on disk changed since this report.',
    pending: n => `${plural(n, 'session')} not analyzed yet.`,
    analyzing: (done, total) => `Analyzing ${done} of ${total} sessions…`,
    analyzed: (done, errors) => errors ? `Analyzed ${done} sessions, ${errors} could not be read.` : `Analyzed ${done} sessions.`,
    basedOn: (used, all) => `Based on ${used} of ${all} sessions.`,
    liveExcluded: n => `${plural(n, 'live session')} left out. A live session changes on every refresh.`,
  },
  strip: {
    sessions: 'Sessions in the period',
    inTurn: 'Agent time in turns',
    waiting: 'Waiting for you',
    tokens: 'Tokens not from cache + output',
    tokensNote: (cached, reasoning) => `cached input ${cached} · reasoning ${reasoning}`,
    top: 'Top findings',
  },
  groups: {
    you_and_the_agent: { name: 'You and the agent', question: 'How fast do you reply, and what does waiting cost?' },
    sub_agents: { name: 'Sub-agents', question: 'Do sub-agents run in parallel, and what does starting them cost?' },
    failures_and_retries: { name: 'Failures and retries', question: 'What breaks, and how often?' },
    long_tool_runs: { name: 'Long tool runs', question: 'Which commands take the longest, and what keeps running?' },
    context_size: { name: 'Context size', question: 'How big does the context get, and what does compaction cost?' },
    models_and_effort: { name: 'Models and effort', question: 'Which model and effort does each stage use?' },
    not_measured: { name: 'Not measured', question: 'What could we not see?' },
  },
  states: {
    loading: 'Building the report…',
    empty: (from, to) => `No closed sessions between ${from} and ${to} in this project. Change the period or the project.`,
    emptyAll: 'No closed sessions in this period. Change the period.',
    few: n => `Only ${plural(n, 'closed session')} in this period. Findings across sessions need 3 or more. The report shows what each session has.`,
    emptyGroup: 'Nothing found in this period.',
    error: 'Could not build the report.',
    noData: (n, reason) => `No data in ${plural(n, 'session')}${reason ? ` (${reason})` : ''}.`,
    noDataItems: n => `${plural(n, 'item')} had no usage record.`,
    inSessions: (n, of) => `In ${n} of ${of} sessions.`,
    projectsWithMore: 'Projects with 3 or more sessions in this period:',
  },
  card: {
    info: 'For information',
    noDataTitle: 'Signals that could not be measured',
    spread: 'How it is spread',
    todo: 'What to do',
    where: 'Where',
    showAll: n => `Show all ${n}`,
    showFewer: 'Show fewer',
    how: 'How this is computed',
    session: 'Session',
    agent: 'Agent',
    when: 'When',
    duration: 'Duration',
    tokens: 'Tokens',
    note: 'Detail',
    root: 'main thread',
    sub: 'sub-agents',
  },
  sessionCard: {
    title: 'Session insights',
    subtitle: 'The biggest findings of this session.',
    none: 'No findings in this session.',
    project: 'See the report for this project',
  },
  guide: {
    title: 'How insights are computed',
    intro: 'Every card comes from one rule. A rule looks at the structure of the session: turn boundaries, waits, retry groups, compaction events, token records. No rule guesses a cause from a duration. A card shows what the pattern cost, how it is spread, what to do, and where it happened. It never shows an estimate of savings.',
    period: 'The report covers the closed sessions whose last activity falls inside the period. Live sessions are left out. Findings across sessions need 3 or more sessions in one project.',
    axis: 'Cards and groups are ordered by time, or by tokens not served from cache plus output tokens. Cached input and reasoning tokens are shown but never weighted.',
    signal: 'Signal',
    exposure: 'What is measured',
  },
  rules: {
    D1: {
      title: 'The agent waited for your answer',
      signal: 'A wait for you right after a turn in which the agent asked a question.',
      measured: 'The length of each such wait.',
      happened: c => `The agent asked you a question ${c.exposure.count} ${c.exposure.count === 1 ? 'time' : 'times'} and waited ${fmt(c.exposure.time_ms)} in total${shareText(c)}. ${inSessions(c)}`,
      todo: 'Reply sooner. Or write the default answers into the instructions file (AGENTS.md) so the agent does not need to ask. Or use plan mode at the start.',
    },
    D2: {
      title: 'Time to your reply',
      signal: 'A wait for you shorter than 4 hours, ended by a turn you started.',
      measured: 'The length of each wait: total, median and buckets.',
      happened: c => {
        const s = c.stats || {};
        const parts = [`You replied ${c.exposure.count} times. The agent waited ${fmt(c.exposure.time_ms)} in total${shareText(c)}.`];
        if (s.median) parts.push(`Half of your replies came within ${fmt(s.median, true)}.`);
        if (s.p90) parts.push(`One in ten took more than ${fmt(s.p90, true)}.`);
        parts.push(inSessions(c));
        return parts.join(' ');
      },
      todo: 'Check the session more often, or answer several questions at once. Give the agent follow-up work (goal loop, followup_task) so it keeps working while you are away.',
    },
    D2b: {
      title: 'Long breaks (4 hours or more)',
      signal: 'A wait for you of 4 hours or more, ended by a turn you started.',
      measured: 'The length of each break. Shown for the full picture, never ranked.',
      happened: c => `${c.exposure.count} ${c.exposure.count === 1 ? 'break' : 'breaks'} of 4 hours or more, ${fmt(c.exposure.time_ms)} in total${shareText(c)}. The agent had no work during this time. This is not a mistake. ${inSessions(c)}`,
      todo: 'Give the agent follow-up work before you leave. Or end the session and start a new one later. After a break of 4 hours or more, the model re-reads almost all of its context without cache.',
    },
    D3: {
      title: 'Turns you stopped',
      signal: 'A turn with status aborted.',
      measured: 'The time inside each stopped turn and its tokens.',
      happened: c => `You stopped ${c.exposure.count} ${c.exposure.count === 1 ? 'turn' : 'turns'}. The agent had worked ${fmt(c.exposure.time_ms)} inside them${shareText(c)}. ${inSessions(c)}`,
      todo: 'Before a long task, ask for a plan first (plan mode). Ask the agent to report at checkpoints. Tell it when to stop.',
    },
    D9: {
      title: 'Long tool runs',
      signal: 'A test, build, release or infra command whose shape ran two or more times in the period.',
      measured: 'The time of each run, by command shape.',
      happened: c => {
        const top = c.distribution && c.distribution[0];
        const first = top ? ` The longest shape: \`${shapeText(top.label)}\`, ${top.n} runs, ${fmt(top.time_ms)}.` : '';
        return `Commands that ran two or more times took ${fmt(c.exposure.time_ms)} in total${shareText(c)}.${first} ${inSessions(c)}`;
      },
      todo: 'Try a faster or incremental version. Or start it early and let the agent do other work while it runs.',
    },
    D12: {
      title: 'Processes left running',
      signal: 'A command that kept running after its turn ended (a server, a watcher).',
      measured: 'How long each kept running.',
      happened: c => `A server or watcher kept running after its turn ended ${c.exposure.count} ${c.exposure.count === 1 ? 'time' : 'times'}, ${fmt(c.exposure.time_ms)} in total. ${inSessions(c)}`,
      todo: 'Stop it, or run it under the harness process manager.',
    },
    D13: {
      title: 'Commands no rule matched',
      signal: 'A command the classifier could not match to any rule (phase unknown).',
      measured: 'The time of each such command, by its first word. The time with no telemetry is a number on the card.',
      happened: c => {
        const s = c.stats || {};
        const top = c.distribution && c.distribution[0];
        const parts = [`${fmt(s.unknown_ms || c.exposure.time_ms)} went to commands that matched no rule.`];
        if (top) parts.push(`The most common: \`${top.label}\`, ${top.n} ${top.n === 1 ? 'time' : 'times'}.`);
        if (s.no_telemetry_ms) parts.push(`${fmt(s.no_telemetry_ms)} had no telemetry at all.`);
        parts.push(inSessions(c));
        return parts.join(' ');
      },
      todo: 'Add rules for these commands in the rules file (~/.todobem/rules.json). After a rule change, all cached sessions are analyzed again.',
    },
    D14: {
      title: 'Invalid tool calls',
      signal: 'The model sent a tool call the harness could not parse. Shown from three occurrences.',
      measured: 'The count.',
      happened: c => `The model sent ${c.exposure.count} tool calls with invalid arguments. ${inSessions(c)}`,
      todo: 'This is a model error, not yours. If it repeats with one tool, check that tool\'s description in your setup.',
    },
    D15: {
      title: 'Edits that failed',
      signal: 'A failed edit, patch, script or shell command. Reads and searches that found nothing do not count.',
      measured: 'The count, by kind.',
      happened: c => {
        const top = c.distribution && c.distribution[0];
        const kind = top ? ` The most common kind: ${top.label}, ${top.n} ${top.n === 1 ? 'time' : 'times'}.` : '';
        return `${c.exposure.count} edits, patches or scripts failed.${kind} ${inSessions(c)}`;
      },
      todo: 'Ask the agent to read the file right before it edits. Keep patches small.',
    },
    M1: {
      title: 'Model time by model, effort and stage',
      signal: 'The model-output segments of the main thread\'s turns, with the model and effort of each turn.',
      measured: 'That time, by model and effort. The split by stage comes from the stage each call served.',
      happened: c => {
        const s = c.stats || {};
        const stages = Object.keys(s).filter(k => k.startsWith('stage_') && s[k] > 0).sort((a, b) => s[b] - s[a]).slice(0, 4).map(k => `${k.slice(6)} ${fmt(s[k])}`);
        const by = stages.length ? ` By stage: ${stages.join(', ')}.` : '';
        return `The model spent ${fmt(c.exposure.time_ms)} generating on the main thread${shareText(c)}.${by} ${inSessions(c)}`;
      },
      todo: 'You can set the effort per stage and a model per agent role in the harness config.',
    },
    T2: {
      title: 'Context size',
      signal: 'The largest input of one model call in each turn: the context it reached.',
      measured: 'The tokens of each turn, by context size.',
      happened: c => {
        const rows = c.distribution || [];
        const big = rows.filter(r => r.label === '200 k or more' || r.label === '150-200 k');
        const n = big.reduce((a, r) => a + r.n, 0);
        const tok = big.reduce((a, r) => a + billableOf(r.tokens), 0);
        const head = n ? `${n} ${n === 1 ? 'turn' : 'turns'} reached a context of 150 k tokens or more; they used ${fmtTok(tok)} tokens (input not from cache + output).` : 'No turn reached a context of 150 k tokens.';
        return `${head} ${inSessions(c)}`;
      },
      todo: 'A new thread at each stage costs fewer tokens per response and compacts less often.',
    },
    T3: {
      title: 'Tokens by model, effort and agent type',
      signal: 'Each agent\'s tokens, with its model and effort and what its commands did (read only, or also edit, build, test, release).',
      measured: 'Tokens per agent type, model and effort (input not from cache + output).',
      happened: c => {
        const rows = c.distribution || [];
        const sum = prefix => fmtTok(rows.filter(r => r.label.startsWith(prefix)).reduce((a, r) => a + billableOf(r.tokens), 0));
        return `Read-only sub-agents used ${sum('read-only')} tokens; worker sub-agents ${sum('worker')}; the main thread ${sum('main')} (input not from cache + output). ${inSessions(c)}`;
      },
      todo: 'Give read-only sub-agents a cheaper model or a lower effort (Codex: a model per agent in the agent config).',
    },
    T6: {
      title: 'Cost to start a sub-agent',
      signal: 'The first model call of a sub-agent: the instructions and context it reads before any work.',
      measured: 'Those tokens per sub-agent, and how many sub-agents used more to start than to work.',
      happened: c => {
        const s = c.stats || {};
        const more = s.more_to_start ? ` ${s.more_to_start} of them used more tokens to start than to work.` : '';
        return `${c.exposure.count} sub-agents. Starting them cost ${fmtTok(billableOf(c.exposure.tokens))} tokens (input not from cache + output).${more} ${inSessions(c)}`;
      },
      todo: 'Combine small lookups into one sub-agent, or do them in the main thread.',
    },
    T7: {
      title: 'Reasoning tokens',
      signal: 'The reasoning part of each agent\'s output tokens, by effort.',
      measured: 'The reasoning share of all output tokens.',
      happened: c => {
        const t = c.exposure.tokens || {};
        const pct = t.output ? Math.round((t.reasoning || 0) / t.output * 100) : 0;
        return `Reasoning is ${pct} % of all output tokens. ${inSessions(c)}`;
      },
      todo: 'Reasoning follows the effort setting. A lower effort for simple stages reduces it.',
    },
    D4: {
      title: 'Sub-agents ran one after another',
      signal: 'The main thread waited for sub-agents while at most one sub-agent was inside a turn.',
      measured: 'The part of each wait with at most one sub-agent working.',
      happened: c => `The main thread waited ${fmt(c.exposure.time_ms)} while only one sub-agent was working${shareText(c)}. ${inSessions(c)}`,
      todo: 'If the sub-agents did not depend on each other, start them together and wait once. If they did, let the main thread do its own next step while it waits. The log does not show whether they depended on each other.',
    },
    D7: {
      title: 'Commands that fail and get retried',
      signal: 'A retry group with at least one failed attempt. The same normalized command ran again after a failure.',
      measured: 'Time in retries, fixes between attempts, recovery steps and queue waits. Tokens of the turns inside those windows.',
      happened: c => {
        const groups = (c.stats && c.stats.groups) || c.exposure.count;
        const top = c.distribution && c.distribution[0];
        const shape = top ? ` The most common command shape: \`${shapeText(top.label)}\`, in ${plural(top.sessions, 'session')}.` : '';
        return `Commands failed and were run again in ${c.sessions} of ${c.of} sessions. ${plural(groups, 'retry group')} took ${fmt(c.exposure.time_ms)} in retries and fixes${shareText(c)}.${shape}`;
      },
      todo: 'Look at the command shape that fails most. For a setup step: fix the setup script or the image, or write the working command into the instructions file (AGENTS.md). For a test or build: add the check that the fix always does, before the run.',
    },
    D11: {
      title: 'Context compaction pauses',
      signal: 'A context compaction by the harness.',
      measured: 'Time of each compaction. The context size before it. The tokens of the first model call after it.',
      happened: c => {
        const s = c.stats || {};
        const rootN = s.count_main || 0;
        const subN = s.count_sub || 0;
        const ctx = s.context_median ? ` at about ${Math.round(s.context_median / 1000)} k tokens each time` : '';
        const root = rootN ? `${fmt(s.time_main || 0)} on the main thread${c.share ? ` (${c.share.pct.toFixed(1)} % of its elapsed time)` : ''}` : 'no time on the main thread';
        const sub = subN ? `, ${fmt(s.time_sub || 0)} in sub-agents (in parallel)` : '';
        return `The harness compacted the context ${c.exposure.count} times (main thread ${rootN}, sub-agents ${subN})${ctx}. This took ${root}${sub}. ${inSessions(c)}`;
      },
      todo: 'Split long tasks into new threads or sub-agents at stage boundaries. Keep instruction files short. Do not paste large outputs into the chat.',
    },
    T1: {
      title: 'Cache after a break',
      signal: 'A turn of the main thread that started after a wait for your reply, and the usage of its first model call.',
      measured: 'The share of the first call that came from the cache, by the length of the break. The input tokens not from cache after breaks of 15 minutes or more.',
      happened: c => {
        const rows = c.distribution || [];
        const share = label => {
          const r = rows.find(x => x.label === label);
          if (!r || !r.tokens || !r.tokens.input) return null;
          return Math.round((1 - (r.tokens.input - r.tokens.cached) / r.tokens.input) * 100);
        };
        const parts = [];
        const fast = share('under 5 min');
        const mid = share('1-4 h');
        const slow = share('4 h or more');
        if (fast !== null) parts.push(`When you replied within 5 minutes, ${fast} % of the context came from cache.`);
        if (mid !== null) parts.push(`After 1 to 4 hours: ${mid} %.`);
        if (slow !== null) parts.push(`After 4 hours or more: ${slow} %.`);
        const s = c.stats || {};
        if (s.starts_after_15m) parts.push(`${plural(s.starts_after_15m, 'turn start')} after breaks of 15 minutes or more cost ${fmtTok(s.uncached_after_15m || 0)} input tokens not from cache.`);
        parts.push(inSessions(c));
        return parts.join(' ');
      },
      todo: 'Reply within the cache window. After a long break, start a new thread with a short summary instead of continuing a large context.',
    },
  },
};

function pad2(n) {
  return String(n).padStart(2, '0');
}

function inSessions(c) {
  let s = INSIGHT_TEXT.states.inSessions(c.sessions, c.of);
  if (c.no_data) s += ' ' + INSIGHT_TEXT.states.noData(c.no_data, c.reason || '');
  return s;
}

// The denominator of a share, phrased: the main thread's time in turns, or its elapsed time.
const SHARE_OF = { in_turn: 'main thread time in turns', elapsed: 'main thread elapsed' };
function shareText(c) {
  if (!c.share || !c.share.of_ms) return '';
  return ` (of ${fmt(c.share.of_ms)} ${SHARE_OF[c.share.of] || c.share.of}, ${c.share.pct.toFixed(1)} %)`;
}

// laneLabel names a lane the way the timeline does: the main thread, or a sub-agent's last path segment.
function laneLabel(path) {
  if (!path || path === '/root') return typeof MAIN_THREAD === 'string' ? MAIN_THREAD : 'main thread';
  return path.split('/').pop();
}

function shapeText(shape) {
  const i = (shape || '').indexOf(' ');
  return i >= 0 ? shape.slice(i + 1) : shape;
}

/* ---------- state and data ---------- */
function insightsState() {
  if (!state.insights) {
    state.insights = { params: { cwd: '', period: { kind: '30d' }, from: '', to: '', session: '', includeLive: false }, axis: 'time', report: null, loading: false, error: '', openGroups: new Set(), showAll: new Set(), scan: null, stale: false, request: 0, timer: null, sessionReports: {} };
  }
  return state.insights;
}

// insightsQuery builds the report query in a fixed order (tests match the URL).
function insightsQuery(p) {
  const parts = ['period=' + encodeURIComponent(p.period.kind)];
  if (p.period.kind === 'session') {
    parts.push('session=' + encodeURIComponent(p.session || ''));
  } else if (p.cwd) {
    parts.push('cwd=' + encodeURIComponent(p.cwd));
  }
  if (p.period.kind === 'custom') {
    parts.push('from=' + dayStart(p.from));
    parts.push('to=' + dayEnd(p.to));
  }
  if (p.includeLive) parts.push('include_live=1');
  return parts.join('&');
}

// dayStart / dayEnd turn a date input value (YYYY-MM-DD) into local-time ms.
function dayStart(v) {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(v || '');
  if (!m) return 0;
  return new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]), 0, 0, 0, 0).getTime();
}

function dayEnd(v) {
  const s = dayStart(v);
  return s ? s + 24 * 3600e3 - 1 : 0;
}

function dateInputValue(t) {
  const d = new Date(t);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
}

function dayLabel(t) {
  const d = new Date(t);
  return `${d.getDate()} ${MONTHS[d.getMonth()]} ${d.getFullYear()}`;
}

function periodLabel(period, rep) {
  if (!period) return '';
  if (period.kind === 'session') {
    const src = rep && rep.sources && rep.sources[0];
    const s = state.sessions.find(x => x.id === period.session);
    const title = (s && s.title) || (src && src.title) || period.session || '';
    return trunc(title, 60);
  }
  return `${dayLabel(period.from)} – ${dayLabel(period.to)}`;
}

function projectsInPeriod(ins) {
  const p = ins.params;
  const counts = new Map();
  for (const s of state.sessions) {
    const inPeriod = !p.period.from || (s.updated >= p.period.from && s.updated <= p.period.to);
    if (!counts.has(s.cwd)) counts.set(s.cwd, { all: 0, period: 0 });
    const c = counts.get(s.cwd);
    c.all++;
    if (inPeriod) c.period++;
  }
  return [...counts.entries()].sort((a, b) => b[1].period - a[1].period || b[1].all - a[1].all || a[0].localeCompare(b[0]));
}

// defaultProject: the project of the open session, else the one with the most sessions.
function defaultProject(ins) {
  if (state.model && state.model.cwd) return state.model.cwd;
  const projects = projectsInPeriod(ins);
  return projects.length ? projects[0][0] : '';
}

async function loadInsights() {
  const ins = insightsState();
  const request = ++ins.request;
  ins.loading = true;
  ins.error = '';
  ins.stale = false;
  renderInsights();
  const owns = () => request === ins.request && state.page === 'insights';
  try {
    await loadSessions();
    if (!owns()) return;
    if (!ins.params.cwd && ins.params.period.kind !== 'session') ins.params.cwd = defaultProject(ins);
    const rep = await api('/api/insights/report?' + insightsQuery(ins.params));
    if (!owns()) return;
    ins.report = rep;
    ins.loading = false;
    ins.params.period = rep.params.period;
    ins.openGroups = new Set((rep.groups || []).filter(g => g.cards && g.cards.length).map(g => g.id));
    if ((rep.no_data || []).length) ins.openGroups.add('not_measured');
    ins.showAll = new Set();
    renderInsights();
    scheduleInsightsPolls();
  } catch (e) {
    if (!owns()) return;
    ins.loading = false;
    ins.error = e.message;
    renderInsights();
  }
}

// scheduleInsightsPolls keeps the report bar honest: scan progress every 2 s while a scan runs,
// a cheap staleness probe every 60 s otherwise. Nothing polls when the page is not shown.
function scheduleInsightsPolls() {
  const ins = insightsState();
  clearTimeout(ins.timer);
  if (state.page !== 'insights' || !ins.report) return;
  const scanning = ins.scan && ins.scan.running;
  ins.timer = setTimeout(() => insightsPoll(), scanning ? 2000 : 60000);
}

async function insightsPoll() {
  const ins = insightsState();
  if (state.page !== 'insights' || !ins.report) return;
  const request = ins.request;
  try {
    if (ins.scan && ins.scan.running) {
      const p = await api('/api/insights/status');
      if (request !== ins.request || state.page !== 'insights') return;
      ins.scan = p;
      if (!p.running) {
        toast(INSIGHT_TEXT.report.analyzed(p.done, p.errors));
        loadInsights();
        return;
      }
      renderReportBar();
    } else {
      const probe = await api('/api/insights/report?' + insightsQuery(ins.params) + '&probe=1');
      if (request !== ins.request || state.page !== 'insights') return;
      ins.stale = probe.sources_hash !== ins.report.sources_hash || probe.pending !== (ins.report.scope.pending || []).length;
      renderReportBar();
    }
  } catch (e) {
    console.error(e);
  }
  scheduleInsightsPolls();
}

async function startInsightsScan() {
  const ins = insightsState();
  try {
    const r = await apiPost('/api/insights/scan?' + insightsQuery(ins.params));
    const payload = r.payload || {};
    if (!r.ok && r.status !== 409) throw new Error(String(r.status));
    ins.scan = payload.scan || { running: true, done: 0, total: payload.queued || 0 };
    renderReportBar();
    scheduleInsightsPolls();
  } catch (e) {
    toast(INSIGHT_TEXT.states.error + ' ' + e.message);
  }
}

async function cancelInsightsScan() {
  const ins = insightsState();
  try {
    const r = await fetch('/api/insights/scan', { method: 'DELETE', cache: 'no-store' });
    ins.scan = r.ok ? await r.json() : null;
  } catch (e) {
    ins.scan = null;
  }
  renderReportBar();
  scheduleInsightsPolls();
}

/* ---------- actions ---------- */
function insightsAction(a, el) {
  const ins = insightsState();
  if (a === 'insights') return go('insights');
  if (a === 'ins-axis') {
    ins.axis = el.dataset.axis === 'tokens' ? 'tokens' : 'time';
    renderInsights();
    return;
  }
  if (a === 'ins-regenerate') return loadInsights();
  if (a === 'ins-scan') return startInsightsScan();
  if (a === 'ins-cancel') return cancelInsightsScan();
  if (a === 'ins-apply-dates') {
    ins.params.from = ($('#insFrom') || {}).value || ins.params.from;
    ins.params.to = ($('#insTo') || {}).value || ins.params.to;
    ins.params.period = { kind: 'custom', from: dayStart(ins.params.from), to: dayEnd(ins.params.to) };
    return loadInsights();
  }
  if (a === 'ins-group') {
    const id = el.dataset.insGroup;
    if (ins.openGroups.has(id)) ins.openGroups.delete(id);
    else ins.openGroups.add(id);
    renderInsights();
    return;
  }
  if (a === 'ins-more') {
    const id = el.dataset.rule;
    if (ins.showAll.has(id)) ins.showAll.delete(id);
    else ins.showAll.add(id);
    renderInsights();
    return;
  }
  if (a === 'ins-top') {
    const target = $('#card-' + el.dataset.rule);
    if (target) target.scrollIntoView({ block: 'start', behavior: 'smooth' });
    return;
  }
  if (a === 'ins-evidence') {
    state.pendingFocus = { a: Number(el.dataset.a), b: Number(el.dataset.b) };
    return go('session', el.dataset.id);
  }
  if (a === 'ins-project') {
    ins.params.cwd = el.dataset.cwd || '';
    ins.params.period = { kind: ins.params.period.kind === 'session' ? '30d' : ins.params.period.kind };
    if (state.page !== 'insights') return go('insights');
    return loadInsights();
  }
  if (a === 'ins-guide') return guide();
}

// insightsChange handles the report bar's selects (wired from app.js's change listener).
function insightsChange(id, value) {
  const ins = insightsState();
  if (id === 'insPeriod') {
    if (value === 'custom') {
      const to = Date.now();
      ins.params.from = ins.params.from || dateInputValue(to - 30 * 24 * 3600e3);
      ins.params.to = ins.params.to || dateInputValue(to);
      ins.params.period = { kind: 'custom', from: dayStart(ins.params.from), to: dayEnd(ins.params.to) };
    } else if (value === 'session') {
      const first = sessionsForPicker(ins)[0];
      ins.params.session = ins.params.session || (first ? first.id : '');
      ins.params.period = { kind: 'session' };
      if (!ins.params.session) {
        renderInsights();
        return;
      }
    } else {
      ins.params.period = { kind: value };
    }
    loadInsights();
    return;
  }
  if (id === 'insProject') {
    ins.params.cwd = value;
    loadInsights();
    return;
  }
  if (id === 'insSession') {
    ins.params.session = value;
    ins.params.period = { kind: 'session' };
    loadInsights();
    return;
  }
  if (id === 'insLive') {
    ins.params.includeLive = !!value;
    loadInsights();
  }
}

function sessionsForPicker(ins) {
  const cwd = ins.params.cwd;
  return state.sessions.filter(s => !cwd || s.cwd === cwd).slice().sort((a, b) => b.updated - a.updated);
}

/* ---------- rendering ---------- */
function renderInsights() {
  if (state.page !== 'insights') return;
  $('#main').innerHTML = insightsPage();
  $$('[data-icon]').forEach(el => el.innerHTML = icon(el.dataset.icon));
}

function insightsPage() {
  const ins = insightsState();
  const T = INSIGHT_TEXT;
  const head = `<section class="page-heading"><div><div class="eyebrow">${esc(T.page.eyebrow)}</div><h1>${esc(T.page.title)}</h1><p class="subtitle">${esc(T.page.subtitle)}</p></div></section>`;
  const bar = `<div id="reportBar">${reportBarHTML()}</div>`;
  let body = '';
  if (ins.loading && !ins.report) {
    body = skeletonHTML();
  } else if (ins.error) {
    body = `<div class="empty"><h3>${esc(T.states.error)}</h3><p>${esc(ins.error)}</p></div>`;
  } else if (ins.report) {
    body = reportBodyHTML(ins);
  }
  return head + bar + body + footer();
}

function skeletonHTML() {
  return `<div class="skeleton-list" aria-busy="true" aria-label="${esc(INSIGHT_TEXT.states.loading)}"><div class="skeleton"></div><div class="skeleton"></div><div class="skeleton"></div><p class="muted-note">${esc(INSIGHT_TEXT.states.loading)}</p></div>`;
}

function renderReportBar() {
  const el = $('#reportBar');
  if (el && state.page === 'insights') el.innerHTML = reportBarHTML();
}

function reportBarHTML() {
  const ins = insightsState();
  const T = INSIGHT_TEXT;
  const p = ins.params;
  const kind = p.period.kind;
  const periodSelect = `<label class="ctl">${esc(T.controls.period)}<select class="select" id="insPeriod" aria-label="${esc(T.controls.period)}">${Object.keys(T.periods).map(k => `<option value="${k}" ${k === kind ? 'selected' : ''}>${esc(T.periods[k])}</option>`).join('')}</select></label>`;
  let extra = '';
  if (kind === 'custom') {
    extra = `<label class="ctl">${esc(T.controls.from)}<input type="date" id="insFrom" value="${esc(p.from)}"></label><label class="ctl">${esc(T.controls.to)}<input type="date" id="insTo" value="${esc(p.to)}"></label><button class="btn small" data-action="ins-apply-dates">${esc(T.controls.apply)}</button>`;
  } else if (kind === 'session') {
    const list = sessionsForPicker(ins);
    extra = `<label class="ctl">${esc(T.controls.session)}<select class="select" id="insSession" aria-label="${esc(T.controls.session)}">${list.map(s => `<option value="${esc(s.id)}" ${s.id === p.session ? 'selected' : ''}>${esc(trunc(s.title || s.id, 60))} · ${stamp(s.updated)}</option>`).join('')}</select></label>`;
  }
  const projects = projectsInPeriod(ins).filter(([cwd, c]) => c.period > 0 || cwd === p.cwd);
  const projectSelect = kind === 'session' ? '' : `<label class="ctl">${esc(T.controls.project)}<select class="select" id="insProject" aria-label="${esc(T.controls.project)}"><option value="" ${p.cwd ? '' : 'selected'}>${esc(T.controls.allProjects)}</option>${projects.map(([cwd, c]) => `<option value="${esc(cwd)}" ${cwd === p.cwd ? 'selected' : ''}>${esc(shortPath(cwd))} (${c.period})</option>`).join('')}</select></label>`;
  const axis = `<div class="axis-switch" role="group" aria-label="${esc(T.controls.orderBy)}"><button data-action="ins-axis" data-axis="time" class="${ins.axis === 'time' ? 'active' : ''}" aria-pressed="${ins.axis === 'time'}">${esc(T.controls.time)}</button><button data-action="ins-axis" data-axis="tokens" class="${ins.axis === 'tokens' ? 'active' : ''}" aria-pressed="${ins.axis === 'tokens'}">${esc(T.controls.tokens)}</button></div>`;
  const regen = `<button class="btn ${ins.stale ? 'primary' : ''}" data-action="ins-regenerate" ${ins.loading ? 'disabled' : ''}>${icon('refresh', true)}${esc(T.controls.regenerate)}</button>`;
  return `<section class="report-bar" aria-label="Report period and project">${periodSelect}${extra}${projectSelect}<div class="report-status">${reportStatusHTML(ins)}</div>${axis}${regen}</section>`;
}

function reportStatusHTML(ins) {
  const T = INSIGHT_TEXT;
  const rep = ins.report;
  if (!rep) return `<span class="chip">${esc(T.states.loading)}</span>`;
  const parts = [];
  parts.push(`<span class="chip report-chip" id="reportChip">${esc(T.report.reportFor)} <b>${esc(periodLabel(rep.params.period, rep))}</b> · ${esc(T.report.sessions(rep.scope.sessions))} · ${esc(T.report.generated)} ${stamp(rep.generated_at)}</span>`);
  if (ins.stale) parts.push(`<span class="chip stale">${esc(T.report.stale)}</span>`);
  const scan = ins.scan;
  const pending = (rep.scope.pending || []).length;
  if (scan && scan.running) {
    const pct = scan.total ? Math.round(scan.done / scan.total * 100) : 0;
    parts.push(`<span class="chip pending">${esc(T.report.analyzing(scan.done, scan.total))}</span><div class="progress" role="progressbar" aria-valuenow="${pct}" aria-valuemin="0" aria-valuemax="100"><i style="width:${pct}%"></i></div><button class="btn small ghost" data-action="ins-cancel">${esc(T.controls.cancel)}</button>`);
  } else if (pending) {
    parts.push(`<span class="chip pending">${esc(T.report.pending(pending))}</span><button class="btn small primary" data-action="ins-scan">${esc(T.controls.analyze)}</button>`);
  }
  if (rep.scope.live_excluded) parts.push(`<span class="chip" title="${esc(T.report.liveExcluded(rep.scope.live_excluded))}">${rep.scope.live_excluded} live</span>`);
  return parts.join('');
}

function reportBodyHTML(ins) {
  const T = INSIGHT_TEXT;
  const rep = ins.report;
  const p = rep.params;
  if (rep.fallback === 'no_sessions') {
    const msg = p.cwd ? T.states.empty(dayLabel(p.period.from), dayLabel(p.period.to)) : T.states.emptyAll;
    return `<div class="empty"><h3>${esc(msg)}</h3>${projectsHint(ins)}</div>`;
  }
  const sc = rep.scope;
  const billable = sc.tokens.input - sc.tokens.cached + sc.tokens.output;
  const tiles = [
    [T.strip.sessions, String(sc.sessions), (sc.pending || []).length ? T.report.basedOn(sc.sessions, sc.sessions + sc.pending.length) : '', 'grid'],
    [T.strip.inTurn, fmt(sc.root_in_turn_ms), `${fmt(sc.root_elapsed_ms)} elapsed`, 'code'],
    [T.strip.waiting, fmt(sc.wait_user_ms), '', 'wait'],
    [T.strip.tokens, fmtTok(billable), T.strip.tokensNote(fmtTok(sc.tokens.cached), fmtTok(sc.tokens.reasoning)), 'brain'],
  ];
  const strip = `<section class="metrics open" aria-label="Report totals"><div class="metrics-grid">${tiles.map(([label, v, note, ic]) => `<div class="metric" title="${esc(note)}"><span class="metric-label">${icon(ic, true)}${esc(label)}</span><strong class="metric-value num">${esc(v)}</strong>${note ? `<span class="metric-note">${esc(note)}</span>` : ''}</div>`).join('')}</div></section>`;
  const few = rep.fallback === 'fewer_than_3_sessions' ? `<p class="muted-note ins-note">${esc(T.states.few(sc.sessions))}${projectsHint(ins)}</p>` : '';
  const groups = orderedGroups(ins);
  const cardsByRule = new Map();
  for (const g of groups) {
    for (const c of g.cards || []) cardsByRule.set(c.rule, c);
  }
  const top = (ins.axis === 'tokens' ? rep.top_tokens : rep.top_time) || [];
  const topHTML = top.length ? `<section class="top-findings" aria-label="${esc(T.strip.top)}"><div class="eyebrow">${esc(T.strip.top)}</div>${top.map((rule, i) => {
    const c = cardsByRule.get(rule);
    if (!c) return '';
    const value = ins.axis === 'tokens' ? fmtTok(billableOf(c.exposure.tokens)) : fmt(c.exposure.time_ms);
    return `<button data-action="ins-top" data-rule="${esc(rule)}" style="${toneStyle(c.group)}"><span class="rank">${pad2(i + 1)}</span><span><i class="tone-mark" aria-hidden="true"></i><b>${esc(ruleTitle(c))}</b> · ${esc(T.groups[c.group] ? T.groups[c.group].name : c.group)}</span><span class="mono">${esc(value)}</span></button>`;
  }).join('')}</section>` : '';
  let rank = 0;
  const noData = rep.no_data || [];
  const groupValue = g => ins.axis === 'tokens' ? billableOf(g.tokens) : g.time_ms;
  const maxGroup = Math.max(1, ...groups.map(groupValue));
  const groupsHTML = `<div class="insight-groups">${groups.map((g, gi) => {
    const def = T.groups[g.id] || { name: g.id, question: '' };
    const cards = (g.cards || []).slice().sort((a, b) => (a.info ? 1 : 0) - (b.info ? 1 : 0) || (ins.axis === 'tokens' ? billableOf(b.exposure.tokens) - billableOf(a.exposure.tokens) : b.exposure.time_ms - a.exposure.time_ms));
    const ranked = cards.filter(c => !c.info);
    const unseen = g.id === 'not_measured' && noData.length ? `<div class="nodata"><div class="part-label">${esc(T.card.noDataTitle)}</div><ul>${noData.map(nd => `<li><b>${esc(nd.rule)} · ${esc(ruleTitle(nd))}</b>: ${nd.sessions ? esc(T.states.noData(nd.sessions, nd.reason || '')) : ''}${nd.items ? ' ' + esc(T.states.noDataItems(nd.items)) : ''}</li>`).join('')}</ul></div>` : '';
    const has = cards.length > 0 || unseen !== '';
    const open = ins.openGroups.has(g.id) && has;
    const value = ins.axis === 'tokens' ? fmtTok(billableOf(g.tokens)) : fmt(g.time_ms);
    const body = has ? `<div class="group-body">${unseen}${cards.map(c => cardHTML(ins, c, c.info ? 0 : ++rank)).join('')}</div>` : `<p class="group-empty">${esc(T.states.emptyGroup)}</p>`;
    const gauge = ranked.length ? Math.round(groupValue(g) / maxGroup * 100) : 0;
    return `<section class="insight-group ${open ? 'open' : ''}" id="group-${esc(g.id)}" style="${toneStyle(g.id)}"><button class="group-head" data-action="ins-group" data-ins-group="${esc(g.id)}" aria-expanded="${!!open}" ${has ? '' : 'disabled'}><span class="mono-index"><i class="tone-mark" aria-hidden="true"></i>G${pad2(gi + 1)}</span><span><h2>${esc(def.name)}</h2><div class="question">${esc(def.question)}</div></span><span class="exposure"><b class="num">${ranked.length ? esc(value) : '—'}</b><span>${plural(cards.length, 'card')}</span></span><span class="chev">${icon('right', true)}</span><span class="gauge" aria-hidden="true"><i style="width:${gauge}%"></i></span></button>${body}</section>`;
  }).join('')}</div>`;
  return strip + few + topHTML + groupsHTML;
}

function projectsHint(ins) {
  const list = projectsInPeriod(ins).filter(([cwd, c]) => c.period >= 3 && cwd !== ins.params.cwd);
  if (!list.length) return '';
  return `<p class="muted-note">${esc(INSIGHT_TEXT.states.projectsWithMore)} ${list.slice(0, 6).map(([cwd, c]) => `<button class="text-btn" data-action="ins-project" data-cwd="${esc(cwd)}">${esc(shortPath(cwd))} (${c.period})</button>`).join(' · ')}</p>`;
}

// orderedGroups lists every group in the page order for the axis: the report's order on the
// chosen axis, groups without cards after those with, not_measured always last.
function orderedGroups(ins) {
  const rep = ins.report;
  const byID = new Map((rep.groups || []).map(g => [g.id, g]));
  const groups = INSIGHT_GROUPS.map(id => byID.get(id) || { id, cards: [], time_ms: 0, tokens: { input: 0, cached: 0, output: 0 }, order_time: 99, order_tokens: 99 });
  const key = g => ins.axis === 'tokens' ? g.order_tokens : g.order_time;
  return groups.sort((a, b) => {
    if (a.id === 'not_measured' || b.id === 'not_measured') return a.id === 'not_measured' ? 1 : -1;
    const ca = (a.cards || []).length ? 0 : 1;
    const cb = (b.cards || []).length ? 0 : 1;
    if (ca !== cb) return ca - cb;
    return key(a) - key(b) || INSIGHT_GROUPS.indexOf(a.id) - INSIGHT_GROUPS.indexOf(b.id);
  });
}

function billableOf(t) {
  if (!t) return 0;
  return (t.input || 0) - (t.cached || 0) + (t.output || 0);
}

function ruleTitle(c) {
  const r = INSIGHT_TEXT.rules[c.rule];
  return r ? r.title : c.title;
}

function cardHTML(ins, c, rank) {
  const T = INSIGHT_TEXT;
  const r = T.rules[c.rule] || { title: c.title, happened: () => '', todo: '' };
  const headline = r.happened(c);
  const rows = (c.distribution || []).slice(0, 8);
  const maxRow = c.rule === 'T1' ? 100 : Math.max(1, ...rows.map(x => ins.axis === 'tokens' ? billableOf(x.tokens) : x.time_ms));
  const dist = rows.length ? `<div><div class="part-label">${esc(T.card.spread)}</div><div class="dist">${rows.map(x => {
    let v = ins.axis === 'tokens' ? billableOf(x.tokens) : x.time_ms;
    let shown = ins.axis === 'tokens' ? fmtTok(v) : fmt(x.time_ms);
    if (c.rule === 'T1' && x.tokens && x.tokens.input) {
      // a cache row: the bar is the share not served from cache, the value says both numbers
      const uncached = x.tokens.input - x.tokens.cached;
      v = Math.round(uncached / x.tokens.input * 100);
      shown = `${100 - v} % cached · ${fmtTok(uncached)} not`;
    }
    const label = c.rule === 'D7' || c.rule === 'D9' || c.rule === 'D12' ? shapeText(x.label) : x.label.startsWith('/') ? laneLabel(x.label) : x.label;
    return `<div class="dist-row"><span class="label" title="${esc(x.label)}">${esc(label)}</span><span class="bar" aria-hidden="true"><i style="width:${Math.round(v / maxRow * 100)}%"></i></span><span class="n">${x.n}</span><span class="val">${esc(shown)}${x.sessions > 1 ? ` <small>· ${x.sessions} s.</small>` : ''}</span></div>`;
  }).join('')}</div></div>` : '';
  const all = c.evidence || [];
  const showAll = ins.showAll.has(c.rule);
  const shown = showAll ? all : all.slice(0, 3);
  const evidence = all.length ? `<div><div class="part-label">${esc(T.card.where)}</div><div class="evidence"><div class="ev-head" aria-hidden="true"><span>${esc(T.card.session)}</span><span>${esc(T.card.agent)}</span><span>${esc(T.card.when)}</span><span>${esc(T.card.duration)}</span><span>${esc(T.card.tokens)}</span><span>${esc(T.card.note)}</span></div>${shown.map(e => `<button class="ev-row" data-action="ins-evidence" data-id="${esc(e.session)}" data-a="${e.a}" data-b="${e.b}" title="${esc(e.title || e.session)}"><span>${esc(trunc(e.title || e.session, 48))}</span><span>${esc(laneLabel(e.lane))}</span><span class="mono">${stamp(e.a)}</span><span class="mono">${fmt(e.time_ms)}</span><span class="mono">${e.tokens ? fmtTok(billableOf(e.tokens)) : '—'}</span><span>${esc(e.note || '')}</span></button>`).join('')}</div>${all.length > 3 ? `<button class="text-btn" data-action="ins-more" data-rule="${esc(c.rule)}">${esc(showAll ? T.card.showFewer : T.card.showAll(all.length))}</button>` : ''}</div>` : '';
  const foot = [];
  for (const conv of c.conventions || []) foot.push(esc(conv));
  if (c.no_data_items) foot.push(esc(T.states.noDataItems(c.no_data_items)));
  foot.push(`<button class="text-btn" data-action="ins-guide">${esc(T.card.how)}</button>`);
  const tab = `<span class="mono-index card-tab" title="${esc(T.card.how)}">${esc(c.rule)} · ${c.info ? 'info' : pad2(rank)}</span>`;
  const badge = c.info ? `<span class="chip info-chip">${esc(T.card.info)}</span>` : '';
  return `<article class="insight-card ${c.info ? 'info' : ''}" id="card-${esc(c.rule)}" aria-labelledby="card-${esc(c.rule)}-title"><div class="card-top">${tab}<h3 id="card-${esc(c.rule)}-title">${esc(r.title)}</h3>${badge}</div><p class="headline">${esc(headline)}</p>${dist}<div><div class="part-label">${esc(T.card.todo)}</div><p>${esc(r.todo)}</p></div>${evidence}<div class="card-foot">${foot.join('<span>·</span>')}</div></article>`;
}

/* ---------- the session page card ---------- */
async function renderSessionInsights() {
  const el = $('#sessionInsights');
  const m = current();
  if (!el || !m || state.page !== 'session') return;
  const ins = insightsState();
  const id = m.id;
  let rep = ins.sessionReports[id];
  if (!rep || rep.version !== m.version) {
    try {
      rep = await api('/api/insights/report?period=session&session=' + encodeURIComponent(id));
    } catch (e) {
      return;
    }
    rep.version = m.version;
    ins.sessionReports[id] = rep;
  }
  const target = $('#sessionInsights');
  if (!target || state.page !== 'session' || !current() || current().id !== id) return;
  target.innerHTML = sessionInsightsHTML(rep, m);
}

function sessionInsightsHTML(rep, m) {
  const T = INSIGHT_TEXT;
  const cards = [];
  for (const g of rep.groups || []) {
    for (const c of g.cards || []) cards.push(c);
  }
  cards.sort((a, b) => b.exposure.time_ms - a.exposure.time_ms);
  const link = `<button class="text-btn" data-action="ins-project" data-cwd="${esc(m.cwd || '')}">${esc(T.sessionCard.project)}</button>`;
  if (!cards.length) return `<p class="muted-note">${esc(T.sessionCard.none)}</p>${link}`;
  return cards.slice(0, 3).map(c => {
    const r = T.rules[c.rule] || { title: c.title, happened: () => '' };
    return `<div class="si-row"><div><b>${esc(r.title)}</b><p class="muted-note">${esc(r.happened(c))}</p></div><span class="mono">${fmt(c.exposure.time_ms)}</span></div>`;
  }).join('') + link;
}

/* ---------- the guide section ---------- */
function insightsGuideHTML() {
  const T = INSIGHT_TEXT;
  const rows = Object.keys(T.rules).map(id => {
    const r = T.rules[id];
    return `<tr><td class="mono">${esc(id)}</td><td>${esc(r.title)}</td><td>${esc(r.signal)}</td><td>${esc(r.measured)}</td></tr>`;
  }).join('');
  return `<section class="guide-section" id="insightsGuide"><h3>${esc(T.guide.title)}</h3><p>${esc(T.guide.intro)}</p><p>${esc(T.guide.period)}</p><p>${esc(T.guide.axis)}</p><div style="overflow:auto;max-height:340px"><table class="rules-table"><thead><tr><th>Rule</th><th>Card</th><th>${esc(T.guide.signal)}</th><th>${esc(T.guide.exposure)}</th></tr></thead><tbody>${rows}</tbody></table></div></section>`;
}

// insightsTextSamples returns every string a reader can see, static or rendered from a sample
// card, so the plain-English checks in app_test.js run over one list.
function insightsTextSamples() {
  const sample = {
    rule: 'D4', group: 'sub_agents', title: 'x', sessions: 4, of: 7, no_data: 2, reason: 'CLI < 0.153',
    exposure: { time_ms: 24060000, count: 12, tokens: { input: 5e6, cached: 4e6, output: 1e5 } },
    share: { pct: 27.7, of_ms: 87240000, of: 'in_turn' },
    distribution: [
      { label: 'under 5 min', n: 40, tokens: { input: 2566000, cached: 2013000, output: 0 } },
      { label: '1-4 h', n: 5, tokens: { input: 540000, cached: 321000, output: 0 } },
      { label: '4 h or more', n: 7, tokens: { input: 1118000, cached: 32000, output: 0 } },
      { label: 'infra docker run', n: 12, sessions: 6, time_ms: 5340000 },
    ],
    stats: { groups: 9, attempts: 30, context_median: 212000, count_main: 59, count_sub: 81, time_main: 10440000, time_sub: 6360000, starts_after_15m: 25, uncached_after_15m: 5000000, median: 240000, p90: 2460000, unknown_ms: 7080000, no_telemetry_ms: 120000, stage_implement: 15000000, stage_review: 6900000, more_to_start: 3 },
  };
  sample.distribution.push({ label: '200 k or more', n: 2, tokens: { input: 5e6, cached: 4.5e6, output: 2e5 } });
  sample.distribution.push({ label: 'read-only sub-agents · m / xhigh', n: 39, tokens: { input: 5e7, cached: 4.9e7, output: 1e6 } });
  const out = [];
  const walk = v => {
    if (typeof v === 'string') out.push(v);
    else if (typeof v === 'function') out.push(String(v.length >= 2 ? v(3, 5) : v(sample)));
    else if (v && typeof v === 'object') Object.values(v).forEach(walk);
  };
  walk(INSIGHT_TEXT);
  return out;
}
