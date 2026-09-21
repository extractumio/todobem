'use strict';
// The Insights page: a period report over the sessions of one project (docs/ARCHITECTURE.md §10
// §7). This file defines functions and the text table only; every other name it uses ($, esc,
// fmt, fmtTok, stamp, icon, state, api, go, toast, focusInterval, loadSessions, MONTHS, …) is an
// app.js global resolved at call time, so load order does not matter.
//
// Every string a reader sees lives in INSIGHT_TEXT (plain English, rule 10 of the spec): short
// sentences, common words, numbers with units, "you" and "the agent".

const INSIGHT_GROUPS = ['you_and_the_agent', 'sub_agents', 'verification', 'failures_and_retries', 'tool_calls', 'long_tool_runs', 'context_size', 'models_and_effort', 'not_measured'];
// Each group carries one of the timeline's phase colours (app.css variables) so a reader can tell
// the groups apart on a long page: failures are the error red, long runs the Testing yellow, …
const INSIGHT_TONES = { you_and_the_agent: 'wait_user', sub_agents: 'wait_worker', verification: 'test', failures_and_retries: 'error', tool_calls: 'code', long_tool_runs: 'test', context_size: 'compaction', models_and_effort: 'llm', not_measured: 'unknown' };
// ERROR_RED is the timeline's red for failed tool calls and interrupted turns (app.js markers).
const ERROR_RED = '#d76368';
function toneColor(id) {
  const key = INSIGHT_TONES[id] || 'unknown';
  if (key === 'error') return ERROR_RED;
  const phase = typeof PHASES === 'object' && PHASES[key];
  return (phase && phase.color) || '#98a4ad';
}
const toneStyle = id => `--tone:${toneColor(id)}`;
// Card classes (report.go): an exposure card is ranked by time or tokens, a check by the
// sessions it happened in, an info card is a measurement that is never ranked.
const isInfo = c => c.class === 'info';
const isCheck = c => c.class === 'check';
const classRank = c => (isCheck(c) ? 1 : isInfo(c) ? 2 : 0);
// "1 retry", "5 retries": the one plural the generic helper gets wrong.
const retries = n => `${n} ${n === 1 ? 'retry' : 'retries'}`;

const INSIGHT_TEXT = {
  page: {
    eyebrow: 'Agent insights on this machine',
    title: 'Insights',
    subtitle: 'Where your sessions lose time and tokens, with the evidence.',
  },
  controls: {
    session: 'Session',
    orderBy: 'Order by',
    time: 'Time',
    tokens: 'Tokens',
    regenerate: 'Regenerate',
    analyze: 'Analyze',
    cancel: 'Cancel',
  },
  // the period and project controls are the shared filter (filter.js); one period is this page's
  periods: {
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
    verification: { name: 'Verification loop', question: 'Were the last changes tested and reviewed before the answer?' },
    failures_and_retries: { name: 'Failures and retries', question: 'What breaks, and how often?' },
    tool_calls: { name: 'Tool calls', question: 'What did the agent spend its tool calls on, and how many found nothing?' },
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
    badLink: 'The link carried a filter this page could not read; showing the report as it stands.',
    noData: (n, reason) => `No data in ${plural(n, 'session')}${reason ? ` (${reason})` : ''}.`,
    noDataItems: n => `${plural(n, 'item')} had no usage record.`,
    inSessions: (n, of) => `In ${n} of ${of} sessions.`,
    checkSessions: (n, of, what) => `In ${n} of ${of} ${of === 1 ? 'session' : 'sessions'} ${what}.`,
    projectsWithMore: 'Projects with 3 or more sessions in this period:',
  },
  card: {
    info: 'For information',
    check: 'A check',
    checkValue: (n, of) => `${n} of ${of} ${of === 1 ? 'session' : 'sessions'}`,
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
    session: 'Open the full report for this session',
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
      measured: 'The length of each such wait. How many questions came from a turn that had already changed files.',
      happened: c => {
        const s = c.stats || {};
        const parts = [`The agent asked you a question ${c.exposure.count} ${c.exposure.count === 1 ? 'time' : 'times'} and waited ${fmt(c.exposure.time_ms)} in total${shareText(c)}.`];
        if (s.after_changes) parts.push(`${plural(s.after_changes, 'question')} came from a turn that had already changed files, ${fmt(s.after_changes_ms || 0)} of waiting.`);
        parts.push(inSessions(c));
        return parts.join(' ');
      },
      todo: 'Reply sooner. Or write the default answers into the instructions file (AGENTS.md) so the agent does not need to ask. A question asked after the turn began editing is a decision to settle before the work starts. Put it in the task or in plan mode.',
    },
    D2: {
      title: 'Time to your reply',
      signal: 'A wait for you shorter than 4 hours, ended by a turn you started. A gap under 1 second is a queued message, not a wait.',
      measured: 'The length of each wait: total, median and buckets. Your own pace, shown for the picture and never ranked as a loss.',
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
      measured: 'The time inside each stopped turn and its tokens. Sub-agent turns stopped with the main thread are counted apart: their time runs in parallel.',
      happened: c => {
        const s = c.stats || {};
        const main = s.count_main || 0;
        const sub = s.count_sub || 0;
        const parts = [`You stopped ${plural(main, 'turn')} on the main thread. The agent had worked ${fmt(s.time_main || 0)} inside them${shareText(c)}.`];
        if (sub) parts.push(`${plural(sub, 'sub-agent turn')} stopped with them, ${fmt(s.time_sub || 0)} in parallel.`);
        parts.push(inSessions(c));
        return parts.join(' ');
      },
      todo: 'Before a long task, ask for a plan first (plan mode). Ask the agent to report at checkpoints. Tell it when to stop.',
    },
    D9: {
      title: 'Long tool runs',
      signal: 'A test, build, release or infra command, or a stop hook, whose shape ran two or more times in the period. On Claude Code a command\'s time runs from the call to its result. That includes any permission prompt you answered, which the log does not record.',
      measured: 'The time of each run, by command shape. A stop hook is listed under its own command, marked hook.',
      happened: c => {
        const top = c.distribution && c.distribution[0];
        const first = top ? ` The longest shape: \`${shapeText(top.label)}\`, ${top.n} runs, ${fmt(top.time_ms)}, ${fmt(top.time_ms / Math.max(1, top.n), true)} per run.` : '';
        return `Commands that ran two or more times took ${mainText(c)}.${parallelNote(c)}${first} ${inSessions(c)}`;
      },
      todo: 'Try a faster or incremental version. Or start it early and let the agent do other work while it runs. A slow stop hook holds every turn: narrow it or move it to a pre-commit step.',
    },
    D26: {
      title: 'Sleeps and polling loops',
      signal: 'A wait the agent wrote itself on the main thread: a `sleep N`, a polling loop, a `tail -f`. The harness\'s own waits — for a sub-agent, a CI watch, a stop hook — are not counted here.',
      measured: 'The time of each such wait, by kind. The per-run average on the card is the interval the agent chose. What it was waiting for is not inferred.',
      happened: c => {
        const top = c.distribution && c.distribution[0];
        const first = top ? ` Most of it: ${top.label}, ${top.n} runs, ${fmt(top.time_ms)}, ${fmt(top.time_ms / Math.max(1, top.n), true)} per run.` : '';
        return `Sleeps and polling loops the agent wrote took ${mainText(c)}.${first} ${inSessions(c)}`;
      },
      todo: 'The agent picks the interval; the harness has no say beyond the tool\'s timeout. Many short rounds: a longer interval, or a blocking wait such as `gh run watch` or a `--wait` flag, costs fewer tool calls. A few long sleeps: a shorter interval returns control sooner. Tell the agent in CLAUDE.md or AGENTS.md how to wait for what it usually polls, or give it a command that blocks until ready.',
    },
    D12: {
      title: 'Processes left running',
      signal: 'A command that kept running after its turn ended (a server, a watcher).',
      measured: 'How long each kept running.',
      happened: c => `A server or watcher kept running after its turn ended ${c.exposure.count} ${c.exposure.count === 1 ? 'time' : 'times'}, ${fmt(c.exposure.main_ms || 0)} on the main thread.${parallelNote(c)} ${inSessions(c)}`,
      todo: 'Stop it, or run it under the harness process manager.',
    },
    D13: {
      title: 'Commands no rule matched',
      signal: 'A command the classifier could not match to any rule (phase unknown). It may be an opaque script, a Claude Code tool without a mapping, or a command no rule knows.',
      measured: 'The time of each such command on every lane, by its first word. How it splits between scripts, unmapped tools and unmatched commands. How much of it sat inside the main thread\'s edit loops.',
      happened: c => {
        const s = c.stats || {};
        const top = c.distribution && c.distribution[0];
        // the total and its split are the same measure: every lane's unknown ops, raw op-sum
        const kinds = [['unknown_script_ms', 'opaque scripts'], ['unknown_tool_ms', 'tools without a mapping'], ['unknown_command_ms', 'unmatched commands']];
        const all = kinds.reduce((n, [k]) => n + (s[k] || 0), 0) || c.exposure.time_ms;
        const parts = [`${fmt(all)} went to commands that matched no rule, on every lane.`];
        const split = kinds.filter(([k]) => s[k] > 0).map(([k, name]) => `${fmt(s[k])} ${name}`);
        if (split.length > 1) parts.push(`Of that: ${split.join(', ')}.`);
        if (s.unknown_in_change_window_ms) parts.push(`${fmt(s.unknown_in_change_window_ms)} on the main thread sat between the first and the last edit of a turn, where a rule pays off first.`);
        if (top) parts.push(`The most time: \`${top.label}\`, ${top.n} ${top.n === 1 ? 'run' : 'runs'}, ${fmt(top.time_ms)}.`);
        parts.push(inSessions(c));
        return parts.join(' ');
      },
      todo: 'Add rules for these commands in the rules file (~/.todobem/rules.json). After a rule change, all cached sessions are analyzed again.',
    },
    D18: {
      title: 'Time with no telemetry',
      signal: 'A turn on the main thread that never closed: the log ends inside it, or the next prompt arrived before it closed. The time after its last event is not measured.',
      measured: 'That time, with the interval of each such turn. It is neither work nor waiting, and it is never counted as either.',
      happened: c => {
        const top = (c.evidence || [])[0];
        const longest = top ? ` The longest: ${fmt(top.time_ms)} in one session.` : '';
        return `${fmt(c.exposure.time_ms)} of the period has no telemetry, in ${plural(c.exposure.count, 'interval')}.${longest} ${inSessions(c)}`;
      },
      todo: 'Check how these sessions ended. A killed or crashed CLI leaves the turn open. A prompt sent days later leaves the earlier turn unclosed. Nothing here is counted as work or as waiting.',
    },

    D15: {
      title: 'Tool calls that failed',
      signal: 'A failed edit, patch, script, shell, git, code-hosting or network call. Reads and searches that found nothing are answers, not failures. So is a CI status check still pending.',
      measured: 'The count, by what the call did (editing files, shell, git, …); the command and its kind on each row.',
      happened: c => {
        const top = c.distribution && c.distribution[0]; // rows come sorted by count
        const kind = top ? ` Most often: ${subgroupText(top.label).toLowerCase()}, ${top.n} ${top.n === 1 ? 'time' : 'times'}.` : '';
        return `${plural(c.exposure.count, 'tool call')} failed.${kind} ${inSessions(c)}`;
      },
      todo: 'For edits: ask the agent to read the file right before it edits, and keep patches small. For shell, git and network calls: the evidence rows name the command.',
    },
    D16: {
      title: 'What the tool calls did',
      signal: 'Every tool call on the main thread, grouped by what it did. Reading files, searching, editing, git, code hosting, network, shell; compiling, installing dependencies, test, release, infrastructure; sub-agents, polling, hooks.',
      measured: 'The calls and their exclusive time per group, the same partition as the session breakdown. How many found nothing or failed.',
      happened: c => {
        const s = c.stats || {};
        const rows = (c.distribution || []).slice().sort((a, b) => b.n - a.n);
        const calls = rows.reduce((n, r) => n + r.n, 0);
        const misses = Object.keys(s).filter(k => k.startsWith('misses_')).reduce((n, k) => n + s[k], 0);
        const top = rows.slice(0, 3).map(r => `${subgroupText(r.label).toLowerCase()} ${r.n}`);
        const head = calls ? `${calls} tool calls on the main thread, ${fmt(c.exposure.time_ms)} of tool time.${top.length ? ` Most of them: ${top.join(', ')}.` : ''}` : 'No tool calls on the main thread.';
        const miss = misses ? ` ${misses} ${misses === 1 ? 'call' : 'calls'} found nothing (a search with no match, a read of a missing path, a CI status still pending).` : '';
        const polling = rows.find(r => r.label === 'wait_worker:polling');
        const poll = polling && polling.time_ms ? ` ${fmt(polling.time_ms)} went to polling: sleep, CI status and process waits on the main thread.` : '';
        return `${head}${miss}${poll} ${inSessions(c)}`;
      },
      todo: 'Many searches that find nothing, or repeated reads of the same files, mean the agent lacks a map of the code. Keep the project\'s instruction file current and name the files in the prompt.',
    },
    M1: {
      title: 'Model time by model, effort and stage',
      signal: 'The model-output segments of the main thread\'s turns, with the model and effort of each turn.',
      measured: 'That time, by model and effort. The split by stage comes from the stage each segment served: the nearest tool call in the turn, or the turn\'s signal. Output of turns without any tool call is not a stage and is stated apart.',
      happened: c => {
        const s = c.stats || {};
        const stageName = k => { const key = k.slice(6); const def = typeof LIFECYCLES === 'object' && LIFECYCLES[key]; return def ? def.short : key; };
        const stages = Object.keys(s).filter(k => k.startsWith('stage_') && s[k] > 0).sort((a, b) => s[b] - s[a]).slice(0, 4).map(k => `${stageName(k)} ${fmt(s[k])}`);
        const by = stages.length ? ` By stage: ${stages.join(', ')}.` : '';
        const none = s.no_tool_call_ms ? ` ${fmt(s.no_tool_call_ms)} in turns without a tool call (not a stage).` : '';
        return `The model spent ${fmt(c.exposure.time_ms)} generating on the main thread${shareText(c)}.${by}${none} ${inSessions(c)}`;
      },
      todo: 'Where your harness allows a model or an effort per agent role or per stage, this split says which stage would be affected. It does not say the result would be as good.',
    },
    T2: {
      title: 'Context size',
      signal: 'The largest input of one model call in each turn: the context it reached.',
      measured: 'The tokens of each turn, by context size.',
      happened: c => {
        const rows = c.distribution || [];
        const big = rows.filter(r => !r.label.startsWith('under') && !r.label.startsWith('50-') && !r.label.startsWith('100-'));
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
      todo: 'Read-only sub-agents on the default model are the usual place to try a cheaper model or a lower effort. Codex takes a model per agent in the agent config. Check their answers after the change; the log does not measure quality.',
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
      todo: 'Reasoning follows the effort setting. A lower effort reduces it; whether the answers stay as good is not in the log.',
    },
    D4: {
      title: 'Sub-agents ran one after another',
      signal: 'The main thread waited for sub-agents while at most one sub-agent was inside a turn.',
      measured: 'The part of each wait with at most one sub-agent working.',
      happened: c => `The main thread waited ${fmt(c.exposure.main_ms)} while only one sub-agent was working${shareText(c)}. ${inSessions(c)}`,
      todo: 'If the sub-agents did not depend on each other, start them together and wait once. Both harnesses block the main thread on the wait, so it never works meanwhile. The log does not show whether they depended on each other.',
    },
    D17: {
      title: 'Final changes had no later successful test',
      signal: 'At session end: the session changed files, and no test ran and passed after the last change. A test is a test, lint or type-check command, or a stop hook that runs one. A build does not count. A test that ran after a push still verifies the session; the push itself is the question of D25.',
      measured: 'The sessions where this happened, out of the sessions with changes. Per turn: turns with edits and no passing test after their last edit. Test runs and how many failed. Sessions whose last test failed. What verified the verified sessions, by the kind the rule table gave the command. Sessions that ran stop hooks, and those verified by one.',
      happened: c => {
        const s = c.stats || {};
        const parts = [INSIGHT_TEXT.states.checkSessions(c.sessions, c.of, 'with changes, no test passed after the last edit')];
        if (s.edit_turns) parts.push(`${s.edit_turns_unverified || 0} of ${plural(s.edit_turns, 'turn')} with edits had no passing test after their last edit.`);
        if (s.edit_turns_ambiguous) parts.push(`${plural(s.edit_turns_ambiguous, 'turn')} had a failed compound test with no per-step verdict.`);
        if (s.tests) parts.push(`${s.tests_failed || 0} of ${plural(s.tests, 'test run')} failed.`);
        if (s.last_verdict_failed) parts.push(`In ${plural(s.last_verdict_failed, 'session')} the last test failed.`);
        const kinds = verifiedKinds(s);
        if (kinds.length) {
          parts.push(`First passing check after the last edit, in the verified sessions: ${kinds.map(k => `${k.kind} ${k.n}`).join(', ')}.`);
          if (s.verified_static_only) parts.push(`${plural(s.verified_static_only, 'session')} had a lint, type check or syntax check alone.`);
        }
        if (s.hook_sessions) parts.push(`${plural(s.hook_sessions, 'session')} ran stop hooks; ${s.verified_by_hook || 0} ${s.verified_by_hook === 1 ? 'was' : 'were'} verified by one.`);
        if (c.no_data) parts.push(INSIGHT_TEXT.states.noData(c.no_data, c.reason || ''));
        if (c.not_applicable) parts.push(`${plural(c.not_applicable, 'session')} changed no files.`);
        parts.push('The log does not say what the test covered.');
        return parts.join(' ');
      },
      todo: 'Add a verification step after the last edit. A line in the instructions file (AGENTS.md) works, and so does a stop hook that runs the project\'s test command. The evidence opens the last edit; everything after it ran without a passing test.',
    },
    D24: {
      title: 'A review did not cover the last changes',
      signal: 'A review skill ran (or the agent was in review mode), and files were changed after that review ended. Reviews the harness did not record cannot be seen.',
      measured: 'The sessions where this happened, out of the sessions with a recorded review. How many edits came after the last review.',
      happened: c => {
        const s = c.stats || {};
        const parts = [INSIGHT_TEXT.states.checkSessions(c.sessions, c.of, 'with a review, files changed after the last review ended')];
        if (s.edits_after_review) parts.push(`${plural(s.edits_after_review, 'edit')} came after the last review.`);
        if (c.no_data) parts.push(INSIGHT_TEXT.states.noData(c.no_data, c.reason || ''));
        if (c.not_applicable) parts.push(`${plural(c.not_applicable, 'session')} had no recorded review run.`);
        parts.push('The log does not say what the review looked at.');
        return parts.join(' ');
      },
      todo: 'Run the review skill again after the edits it asked for, or make the review the last step of the turn. The evidence opens the interval from the review\'s end to the last edit.',
    },
    D25: {
      title: 'Pushed with no passing test since the last edit',
      signal: 'A git push with files changed before it, and no test that ran and passed between the last change and the push. An edit to any file counts, docs included. CI that ran after the push is not in the log.',
      measured: 'The sessions where this happened, out of the sessions with a push after a change. Pushes, and how many had no passing test before them. Pushes whose last test before them failed. Sessions with edits after their last push, and those edits.',
      happened: c => {
        const s = c.stats || {};
        const parts = [INSIGHT_TEXT.states.checkSessions(c.sessions, c.of, 'with a push after a change, a push had no passing test since the last edit')];
        if (s.pushes) parts.push(`${s.pushes_unverified || 0} of ${s.pushes} ${s.pushes === 1 ? 'push' : 'pushes'} had no passing test between the last edit and the push.`);
        if (s.pushes_after_failed_test) parts.push(`${s.pushes_after_failed_test} ${s.pushes_after_failed_test === 1 ? 'push' : 'pushes'} came after a failed test.`);
        if (s.sessions_with_edits_after_last_push) parts.push(`${plural(s.sessions_with_edits_after_last_push, 'session')} ended with edits after the last push (${plural(s.edits_after_last_push, 'edit')} in all).`);
        if (c.no_data) parts.push(INSIGHT_TEXT.states.noData(c.no_data, c.reason || ''));
        if (c.not_applicable) parts.push(`${plural(c.not_applicable, 'session')} pushed nothing after a change.`);
        parts.push('The log does not say what the test covered, nor what CI did after the push.');
        return parts.join(' ');
      },
      todo: 'Run the test command before every push: a pre-push hook, a PreToolUse hook on git push, or a line in the instructions file (AGENTS.md). The evidence opens the interval from the last edit to the push.',
      noDataItems: n => `${n} ${n === 1 ? 'push was' : 'pushes were'} not measurable: an unknown command or a telemetry gap between the last edit and the push.`,
    },
    D7: {
      title: 'Commands that fail and get retried',
      signal: 'A retry group with at least one failed attempt. The same normalized command ran again after a failure.',
      measured: 'Time in retries, fixes between attempts, recovery steps and queue waits. Tokens of the turns inside those windows. What the thread ran between each failure and its retry.',
      happened: c => {
        const s = c.stats || {};
        const groups = s.groups || c.exposure.count;
        const top = c.distribution && c.distribution[0];
        const parts = [`Commands failed and were run again in ${c.sessions} of ${c.of} sessions. Retries and fixes in ${plural(groups, 'retry group')} took ${mainText(c)}.${parallelNote(c)}`];
        if (top) parts.push(`The most common command shape: \`${shapeText(top.label)}\`, in ${plural(top.sessions, 'session')}.`);
        if (s.windows) {
          const blind = s.windows_blind || 0;
          parts.push(`${blind} of ${retries(s.windows)} ran again with only reads recorded between the failure and the retry.`);
          if (s.windows_blind_passed) parts.push(`${s.windows_blind_passed} of those ${s.windows_blind_passed === 1 ? 'was a test that' : 'were tests that'} passed on the retry.`);
          const steps = [['windows_fix', 'a fix'], ['windows_infra', 'an infra step'], ['windows_worker', 'a wait'], ['windows_other', 'another step'], ['windows_mixed', 'several kinds of step']].filter(([k]) => s[k]).map(([k, what]) => `${s[k]} after ${what}`);
          if (steps.length) parts.push(`Between the failure and the retry: ${steps.join(', ')}.`);
          if (s.windows_after_user) parts.push(`${retries(s.windows_after_user)} came after a message from you.`);
          if (s.retries_failed_again) parts.push(`${plural(s.retries_failed_again, 'fix or recovery step')} ${s.retries_failed_again === 1 ? 'was' : 'were'} followed by another failure.`);
        }
        return parts.join(' ');
      },
      todo: 'Look at the command shape that fails most. For a setup step: fix the setup script or the image, or write the working command into the instructions file (AGENTS.md). For a test or build: add the check that the fix always does, before the run. A test that failed and then passed with only reads between points at its environment. Pin seeds, ports and caches, or retry at the runner instead of in the agent\'s loop. Many retries with nothing between them point at an error message the agent cannot act on.',
    },
    D11: {
      title: 'Context compaction pauses',
      signal: 'A context compaction by the harness.',
      measured: 'Time of each compaction. The context size before it. The tokens of the first model call after it. How many sat between two edits of one turn.',
      happened: c => {
        const s = c.stats || {};
        const rootN = s.count_main || 0;
        const subN = s.count_sub || 0;
        const ctx = s.context_median ? ` at about ${Math.round(s.context_median / 1000)} k tokens each time` : '';
        const root = rootN ? `${fmt(s.time_main || 0)} on the main thread${c.share ? ` (${c.share.pct.toFixed(1)} % of its time in turns)` : ''}` : 'no time on the main thread';
        const sub = subN ? `, ${fmt(s.time_sub || 0)} in sub-agents (in parallel)` : '';
        const loop = s.in_change_window ? ` ${s.in_change_window} of them happened between two edits of one turn, in the middle of the work.` : '';
        return `The harness compacted the context ${c.exposure.count} times (main thread ${rootN}, sub-agents ${subN})${ctx}. This took ${root}${sub}.${loop} ${inSessions(c)}`;
      },
      todo: 'Split long tasks into new threads or sub-agents at stage boundaries. Keep instruction files short. Do not paste large outputs into the chat. Before a long edit loop, write the plan and the files to touch into a note the next thread can read.',
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
      todo: 'Reply within the cache window. After a long break, a new thread with a short summary costs less only when most of the old context is stale. The number shown is the input read again.',
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
// noDataItemsText words a rule's items without a usable record: the rule's own sentence when it
// has one (D25: a push whose window was blind), else the usage-record default.
function noDataItemsText(rule, n) {
  const r = INSIGHT_TEXT.rules[rule];
  return r && r.noDataItems ? r.noDataItems(n) : INSIGHT_TEXT.states.noDataItems(n);
}

// verifiedKinds reads the `verified_kind:<kind>` stats of D17 as a list, most sessions first.
function verifiedKinds(stats) {
  const out = [];
  for (const [k, n] of Object.entries(stats || {})) {
    if (k.startsWith('verified_kind:') && n > 0) out.push({ kind: k.slice('verified_kind:'.length), n });
  }
  return out.sort((a, b) => b.n - a.n || a.kind.localeCompare(b.kind));
}

// mainText words a multi-lane exposure's main-thread part with its share; parallelNote is the
// sentence about the sub-agents' part, which ran in parallel and never joins a root total
// (product rule 6). Empty when every finding sat on the main thread.
function mainText(c) {
  return `${fmt(c.exposure.main_ms || 0)} on the main thread${shareText(c)}`;
}
function parallelNote(c) {
  const sub = (c.exposure.time_ms || 0) - (c.exposure.main_ms || 0);
  return sub > 0 ? ` Sub-agents add ${fmt(sub)} in parallel.` : '';
}

// subgroupText names a "phase:subgroup" key the way the session breakdown does ("code:read" →
// "Reading files"); a bare phase key gets the phase name; anything else is shown as is.
function subgroupText(key) {
  const i = key.indexOf(':');
  const phase = i >= 0 ? key.slice(0, i) : key, sub = i >= 0 ? key.slice(i + 1) : '';
  const phaseDef = typeof PHASES === 'object' && PHASES[phase];
  if (!phaseDef) return key;
  if (sub && typeof subgroupName === 'function') return subgroupName(phase, sub);
  return phaseDef.name;
}
// laneLabel names a lane the way the timeline does: the main thread, or a sub-agent's last path segment.
function laneLabel(path) {
  if (!path || path === '/root') return typeof MAIN_THREAD === 'string' ? MAIN_THREAD : 'main thread';
  return path.split('/').pop();
}

function shapeText(shape) {
  const i = (shape || '').indexOf(' ');
  if (i < 0) return shape;
  // the phase prefix is dropped; a stop hook keeps its mark so it is not read as the agent's own run
  return (shape.startsWith('hook ') ? 'hook · ' : '') + shape.slice(i + 1);
}

/* ---------- state and data ---------- */
// The report parameters are a shared filter value (filter.js: kind, from, to, cwd) plus this
// page's own: session (kind 'session'). cwd starts as null — "not chosen yet", replaced by the
// default project on the first load — and '' once the reader picks "All projects".
function insightsState() {
  if (!state.insights) {
    state.insights = { params: { kind: '30d', from: '', to: '', cwd: null, session: '', sources: {} }, axis: 'time', report: null, loading: false, error: '', openGroups: new Set(), showAll: new Set(), scan: null, stale: false, request: 0, timer: null, sessionReports: {} };
  }
  return state.insights;
}

// insightsQuery builds the report query in a fixed order (tests match the URL).
function insightsQuery(p) {
  const parts = ['period=' + encodeURIComponent(p.kind)];
  if (p.kind === 'session') {
    parts.push('session=' + encodeURIComponent(p.session || ''));
  } else if (p.cwd) {
    parts.push('cwd=' + encodeURIComponent(p.cwd));
  }
  if (p.kind === 'custom') {
    parts.push('from=' + dayStart(p.from));
    parts.push('to=' + dayEnd(p.to));
  }
  // a source switched off narrows the report's scope; all on is the server's default
  const on = SOURCE_ORDER.filter(k => sourceOn(p, k));
  if (p.kind !== 'session' && on.length < SOURCE_ORDER.length) parts.push('sources=' + on.join(','));
  // one host narrows it too ('.' is this machine, the server's insights.LocalHost)
  if (p.kind !== 'session' && p.host) parts.push('hosts=' + encodeURIComponent(p.host));
  return parts.join('&');
}

// insightsHash is the report's address: one session's report is #session/<id>/insights, every
// other period is #insights?f=<filter> (filterToLink — a set of filters opens by its address),
// and the plain #insights until the project has been resolved or a session picked.
function insightsHash(p) {
  if (p.kind === 'session') return p.session ? '#session/' + encodeURIComponent(p.session) + '/insights' : '#insights';
  if (p.cwd === null) return '#insights';
  return '#insights?f=' + filterToLink(p);
}
const SESSION_INSIGHTS_HASH = /^#session\/([^/?]+)\/insights$/;
// applyInsightsLink reads a report address into the params — the session form, or the filter
// form — and says whether it carried one; an unreadable filter word is said so and applies
// nothing, so the page opens as it stands.
function applyInsightsLink(hash) {
  const ins = insightsState();
  const session = SESSION_INSIGHTS_HASH.exec(hash);
  if (session) {
    let id;
    try { id = decodeURIComponent(session[1]); } catch (e) { return false; }
    ins.params.kind = 'session';
    ins.params.session = id;
    return true;
  }
  const word = /(?:^|&)f=([^&]*)/.exec(hash.startsWith('#insights?') ? hash.slice('#insights?'.length) : '');
  if (!word) return false;
  const f = filterFromLink(word[1]);
  if (!f) {
    toast(INSIGHT_TEXT.states.badLink);
    return false;
  }
  Object.assign(ins.params, f, { session: ins.params.session });
  return true;
}

// periodLabel names the period a report was built for (the server's resolved dates).
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

// defaultProject: the project of the open session, else the one with the most sessions.
function defaultProject(ins) {
  if (state.model && state.model.cwd) return state.model.cwd;
  const projects = filterProjects(ins.params, state.sessions);
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
    if (ins.params.cwd === null && ins.params.kind !== 'session') ins.params.cwd = defaultProject(ins) || null; // no project yet: keep waiting for one
    replaceHash(insightsHash(ins.params)); // the address names the report as it is now
    const rep = await api('/api/insights/report?' + insightsQuery(ins.params));
    if (!owns()) return;
    ins.report = rep;
    ins.loading = false;
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
    // the card's group is opened first: a card in a collapsed group has no box to scroll to
    // (data-ins-group, not data-group: the timeline tooltip claims that one for retry groups)
    const group = el.dataset.insGroup;
    if (group && !ins.openGroups.has(group)) {
      ins.openGroups.add(group);
      renderInsights();
    }
    const target = $('#card-' + el.dataset.rule);
    if (target) scrollBelowTopbar(target);
    return;
  }
  if (a === 'ins-evidence') {
    state.pendingFocus = { a: Number(el.dataset.a), b: Number(el.dataset.b) };
    return go('session', el.dataset.id);
  }
  if (a === 'ins-project') {
    ins.params.cwd = el.dataset.cwd || '';
    if (ins.params.kind === 'session') ins.params.kind = '30d';
    if (state.page !== 'insights') return go('insights');
    return loadInsights();
  }
  if (a === 'ins-guide') return guide();
}

// insightsChange handles the report bar's controls (wired from app.js's change listener): the
// shared filter's period and project, and this page's session picker. Choosing "One session…"
// takes the newest session of the project until one is picked.
function insightsChange(id, value) {
  const ins = insightsState();
  if (id === 'insSession') {
    ins.params.session = value;
    ins.params.kind = 'session';
    loadInsights();
    return;
  }
  const field = filterField('ins', id);
  if (!field || !filterChange(ins.params, field, value)) return;
  if (ins.params.kind === 'session') {
    const first = sessionsForPicker(ins)[0];
    ins.params.session = ins.params.session || (first ? first.id : '');
    if (!ins.params.session) {
      renderInsights();
      return;
    }
  }
  loadInsights();
}

function sessionsForPicker(ins) {
  const cwd = ins.params.cwd;
  return state.sessions.filter(s => (!cwd || s.cwd === cwd) && sourceOn(ins.params, sourceOf(s))).slice().sort((a, b) => b.updated - a.updated);
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

// reportBarHTML: the shared filter (with this page's "One session…" period and its session
// picker), the axis switch and Regenerate on the right, and the report status as its own row
// underneath — a full-width line that wraps as text instead of squeezing between the controls.
function reportBarHTML() {
  const ins = insightsState();
  const T = INSIGHT_TEXT;
  const p = ins.params;
  const kind = p.kind;
  let between = '';
  if (kind === 'session') {
    const list = sessionsForPicker(ins);
    between = `<label class="ctl">${esc(T.controls.session)}<select class="select" id="insSession" aria-label="${esc(T.controls.session)}">${list.map(s => `<option value="${esc(s.id)}" ${s.id === p.session ? 'selected' : ''}>${esc(trunc(s.title || s.id, 60))} · ${stamp(s.updated)}</option>`).join('')}</select></label>`;
  }
  const controls = filterBarHTML('ins', p, { extraPeriods: { session: T.periods.session }, between, project: kind !== 'session', projects: filterProjects(p, state.sessions), sessions: kind === 'session' ? [] : state.sessions });
  const axis = `<div class="axis-switch" role="group" aria-label="${esc(T.controls.orderBy)}"><button data-action="ins-axis" data-axis="time" class="${ins.axis === 'time' ? 'active' : ''}" aria-pressed="${ins.axis === 'time'}">${esc(T.controls.time)}</button><button data-action="ins-axis" data-axis="tokens" class="${ins.axis === 'tokens' ? 'active' : ''}" aria-pressed="${ins.axis === 'tokens'}">${esc(T.controls.tokens)}</button></div>`;
  const regen = `<button class="btn ${ins.stale ? 'primary' : ''}" data-action="ins-regenerate" ${ins.loading ? 'disabled' : ''}>${icon('refresh', true)}${esc(T.controls.regenerate)}</button>`;
  return `<section class="report-bar" aria-label="Report period, project and sources">${controls}<div class="bar-actions">${axis}${regen}</div><div class="report-status">${reportStatusHTML(ins)}</div></section>`;
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
  // the exposure cards of the chosen axis, then the checks (most sessions affected first)
  const top = ((ins.axis === 'tokens' ? rep.top_tokens : rep.top_time) || []).concat(rep.top_checks || []);
  const topHTML = top.length ? `<section class="top-findings" aria-label="${esc(T.strip.top)}"><div class="eyebrow">${esc(T.strip.top)}</div>${top.map((rule, i) => {
    const c = cardsByRule.get(rule);
    if (!c) return '';
    const value = isCheck(c) ? T.card.checkValue(c.sessions, c.of) : ins.axis === 'tokens' ? fmtTok(billableOf(c.exposure.tokens)) : fmt(c.exposure.main_ms);
    return `<button data-action="ins-top" data-rule="${esc(rule)}" data-ins-group="${esc(c.group)}" style="${toneStyle(c.group)}"><span class="rank">${pad2(i + 1)}</span><span><i class="tone-mark" aria-hidden="true"></i><b>${esc(ruleTitle(c))}</b> · ${esc(T.groups[c.group] ? T.groups[c.group].name : c.group)}</span><span class="mono">${esc(value)}</span></button>`;
  }).join('')}</section>` : '';
  let rank = 0;
  const noData = rep.no_data || [];
  const groupValue = g => ins.axis === 'tokens' ? billableOf(g.tokens) : g.time_ms;
  const maxGroup = Math.max(1, ...groups.map(groupValue));
  const groupsHTML = `<div class="insight-groups">${groups.map((g, gi) => {
    const def = T.groups[g.id] || { name: g.id, question: '' };
    const cards = (g.cards || []).slice().sort((a, b) => classRank(a) - classRank(b) || (isCheck(a) ? (b.sessions - a.sessions || b.exposure.count - a.exposure.count) : (ins.axis === 'tokens' ? billableOf(b.exposure.tokens) - billableOf(a.exposure.tokens) : b.exposure.main_ms - a.exposure.main_ms)));
    const ranked = cards.filter(c => !isInfo(c) && !isCheck(c));
    const unseen = g.id === 'not_measured' && noData.length ? `<div class="nodata"><div class="part-label">${esc(T.card.noDataTitle)}</div><ul>${noData.map(nd => `<li><b>${esc(nd.rule)} · ${esc(ruleTitle(nd))}</b>: ${nd.sessions ? esc(T.states.noData(nd.sessions, nd.reason || '')) : ''}${nd.items ? ' ' + esc(noDataItemsText(nd.rule, nd.items)) : ''}</li>`).join('')}</ul></div>` : '';
    const has = cards.length > 0 || unseen !== '';
    const open = ins.openGroups.has(g.id) && has;
    const value = ins.axis === 'tokens' ? fmtTok(billableOf(g.tokens)) : fmt(g.time_ms);
    const body = has ? `<div class="group-body">${unseen}${cards.map(c => cardHTML(ins, c, isInfo(c) ? 0 : ++rank)).join('')}</div>` : `<p class="group-empty">${esc(T.states.emptyGroup)}</p>`;
    const gauge = ranked.length ? Math.round(groupValue(g) / maxGroup * 100) : 0;
    return `<section class="insight-group ${open ? 'open' : ''}" id="group-${esc(g.id)}" style="${toneStyle(g.id)}"><button class="group-head" data-action="ins-group" data-ins-group="${esc(g.id)}" aria-expanded="${!!open}" ${has ? '' : 'disabled'}><span class="mono-index"><i class="tone-mark" aria-hidden="true"></i>G${pad2(gi + 1)}</span><span><h2>${esc(def.name)}</h2><div class="question">${esc(def.question)}</div></span><span class="exposure"><b class="num">${ranked.length ? esc(value) : '—'}</b><span>${plural(cards.length, 'card')}</span></span><span class="chev">${icon('right', true)}</span><span class="gauge" aria-hidden="true"><i style="width:${gauge}%"></i></span></button>${body}</section>`;
  }).join('')}</div>`;
  return strip + few + topHTML + groupsHTML;
}

function projectsHint(ins) {
  const list = filterProjects(ins.params, state.sessions).filter(([cwd, c]) => c.period >= 3 && cwd !== ins.params.cwd);
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
  const maxRow = c.rule === 'T1' ? 100 : isCheck(c) ? Math.max(1, ...rows.map(x => x.n)) : Math.max(1, ...rows.map(x => ins.axis === 'tokens' ? billableOf(x.tokens) : x.time_ms));
  const dist = rows.length ? `<div><div class="part-label">${esc(T.card.spread)}</div><div class="dist">${rows.map(x => {
    let v = ins.axis === 'tokens' ? billableOf(x.tokens) : x.time_ms;
    let shown = ins.axis === 'tokens' ? fmtTok(v) : fmt(x.time_ms);
    if (isCheck(c)) {
      // a check row counts sessions (a rule with several findings per session, D25's pushes,
      // still says "in N of M sessions") over its own denominator when the rule keeps one per
      // row (D17: the measurable sessions of that source), else the card's
      v = x.n;
      shown = `${x.sessions} of ${x.of || c.of}`;
    }
    if (c.rule === 'T1' && x.tokens && x.tokens.input) {
      // a cache row: the bar is the share not served from cache, the value says both numbers
      const uncached = x.tokens.input - x.tokens.cached;
      v = Math.round(uncached / x.tokens.input * 100);
      shown = `${100 - v} % cached · ${fmtTok(uncached)} not`;
    }
    const label = c.rule === 'D7' || c.rule === 'D9' || c.rule === 'D12' ? shapeText(x.label) : c.rule === 'D15' || c.rule === 'D16' ? subgroupText(x.label) : x.label.startsWith('/') ? laneLabel(x.label) : x.label;
    return `<div class="dist-row"><span class="label" title="${esc(x.label)}">${esc(label)}</span><span class="bar" aria-hidden="true"><i style="width:${Math.round(v / maxRow * 100)}%"></i></span><span class="n">${x.n}</span><span class="val">${esc(shown)}${x.sessions > 1 ? ` <small>· ${x.sessions} s.</small>` : ''}</span></div>`;
  }).join('')}</div></div>` : '';
  const all = c.evidence || [];
  const showAll = ins.showAll.has(c.rule);
  const shown = showAll ? all : all.slice(0, 3);
  const evidence = all.length ? `<div><div class="part-label">${esc(T.card.where)}</div><div class="evidence"><div class="ev-head" aria-hidden="true"><span>${esc(T.card.session)}</span><span>${esc(T.card.agent)}</span><span>${esc(T.card.when)}</span><span>${esc(T.card.duration)}</span><span>${esc(T.card.tokens)}</span><span>${esc(T.card.note)}</span></div>${shown.map(e => `<button class="ev-row" data-action="ins-evidence" data-id="${esc(e.session)}" data-a="${e.a}" data-b="${e.b}" title="${esc(e.title || e.session)}"><span>${esc(trunc(e.title || e.session, 48))}</span><span>${esc(laneLabel(e.lane))}</span><span class="mono">${stamp(e.a)}</span><span class="mono">${isCheck(c) ? fmt(e.b - e.a) : fmt(e.time_ms)}</span><span class="mono">${e.tokens ? fmtTok(billableOf(e.tokens)) : '—'}</span><span>${esc(e.note || '')}</span></button>`).join('')}</div>${all.length > 3 ? `<button class="text-btn" data-action="ins-more" data-rule="${esc(c.rule)}">${esc(showAll ? T.card.showFewer : T.card.showAll(all.length))}</button>` : ''}</div>` : '';
  const foot = [];
  for (const conv of c.conventions || []) foot.push(esc(conv));
  if (c.no_data_items) foot.push(esc(noDataItemsText(c.rule, c.no_data_items)));
  foot.push(`<button class="text-btn" data-action="ins-guide">${esc(T.card.how)}</button>`);
  const tab = `<span class="mono-index card-tab" title="${esc(T.card.how)}">${esc(c.rule)} · ${isInfo(c) ? 'info' : pad2(rank)}</span>`;
  const badge = isInfo(c) ? `<span class="chip info-chip">${esc(T.card.info)}</span>` : isCheck(c) ? `<span class="chip info-chip check-chip">${esc(T.card.check)}</span>` : '';
  return `<article class="insight-card ${isInfo(c) ? 'info' : isCheck(c) ? 'check' : ''}" id="card-${esc(c.rule)}" aria-labelledby="card-${esc(c.rule)}-title"><div class="card-top">${tab}<h3 id="card-${esc(c.rule)}-title">${esc(r.title)}</h3>${badge}</div><p class="headline">${esc(headline)}</p>${dist}<div><div class="part-label">${esc(T.card.todo)}</div><p>${esc(r.todo)}</p></div>${evidence}<div class="card-foot">${foot.join('<span>·</span>')}</div></article>`;
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
  // the session's biggest findings by the main thread's time; measurements and checks are not findings
  const ranked = cards.filter(c => !isInfo(c) && !isCheck(c)).sort((a, b) => b.exposure.main_ms - a.exposure.main_ms);
  // the session's own report is an address (#session/<id>/insights), so it can be copied and opened as one
  const link = `<div class="si-links"><a class="text-btn" href="${esc(insightsHash({ kind: 'session', session: m.id }))}">${esc(T.sessionCard.session)}</a><button class="text-btn" data-action="ins-project" data-cwd="${esc(m.cwd || '')}">${esc(T.sessionCard.project)}</button></div>`;
  if (!ranked.length) return `<p class="muted-note">${esc(T.sessionCard.none)}</p>${link}`;
  return ranked.slice(0, 3).map(c => {
    const r = T.rules[c.rule] || { title: c.title, happened: () => '' };
    return `<div class="si-row"><div><b>${esc(r.title)}</b><p class="muted-note">${esc(r.happened(c))}</p></div><span class="mono">${fmt(c.exposure.main_ms)}</span></div>`;
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
    rule: 'D4', group: 'sub_agents', title: 'x', sessions: 4, of: 7, no_data: 2, not_applicable: 3, reason: 'CLI < 0.153',
    exposure: { time_ms: 24060000, main_ms: 18000000, count: 12, tokens: { input: 5e6, cached: 4e6, output: 1e5 } },
    share: { pct: 27.7, of_ms: 87240000, of: 'in_turn' },
    distribution: [
      { label: 'under 5 min', n: 40, tokens: { input: 2566000, cached: 2013000, output: 0 } },
      { label: '1-4 h', n: 5, tokens: { input: 540000, cached: 321000, output: 0 } },
      { label: '4 h or more', n: 7, tokens: { input: 1118000, cached: 32000, output: 0 } },
      { label: 'infra docker run', n: 12, sessions: 6, time_ms: 5340000 },
      { label: 'wait_worker:polling', n: 30, sessions: 6, time_ms: 900000 },
    ],
    evidence: [{ session: 'S1', title: 'First session', lane: '/root', lane_id: 'L1', a: 2000, b: 3000, time_ms: 7200000, note: 'the log ends inside this turn' }],
    stats: { groups: 9, attempts: 30, no_tool_call_ms: 120000, unknown_script_ms: 300000, unknown_tool_ms: 60000, misses_code_search: 41, context_median: 212000, count_main: 59, count_sub: 81, time_main: 10440000, time_sub: 6360000, starts_after_15m: 25, uncached_after_15m: 5000000, median: 240000, p90: 2460000, unknown_ms: 7080000, no_telemetry_ms: 120000, stage_implement: 15000000, stage_review: 6900000, more_to_start: 3 , windows: 14, windows_blind: 3, windows_after_user: 1, retries_failed_again: 4, in_change_window: 6, after_changes: 2, after_changes_ms: 600000, unknown_in_change_window_ms: 120000, edit_turns: 12, edit_turns_unverified: 7, verified_by_hook: 2, last_verdict_failed: 1, edits_after_review: 9, windows: 12, windows_blind: 4, windows_blind_passed: 2, windows_fix: 3, windows_other: 2, windows_mixed: 1, tests: 40, tests_failed: 9, 'verified_kind:go test': 5, 'verified_kind:syntax-check': 1, verified_static_only: 1, hook_sessions: 6, verified_by_hook: 2, pushes: 20, pushes_unverified: 7, pushes_after_failed_test: 2, sessions_with_edits_after_last_push: 3, edits_after_last_push: 11 },
  };
  sample.distribution.push({ label: '200-500 k', n: 2, tokens: { input: 5e6, cached: 4.5e6, output: 2e5 } });
  sample.distribution.push({ label: 'read-only sub-agents · m / xhigh', n: 39, tokens: { input: 5e7, cached: 4.9e7, output: 1e6 } });
  const out = [];
  const walk = v => {
    if (typeof v === 'string') out.push(v);
    else if (typeof v === 'function') out.push(String(v.length >= 2 ? v(3, 5) : v(sample)));
    else if (v && typeof v === 'object') Object.values(v).forEach(walk);
  };
  walk(INSIGHT_TEXT);
  walk(FILTER_TEXT);
  return out;
}
