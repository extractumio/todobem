'use strict';
// The #inspector dialogs. This file defines functions only; every name it uses ($, state, PHASES,
// inspectorRequest, …) is an app.js global resolved at call time, so load order does not matter.
/* ---------- inspector ---------- */
async function inspect(id) {
  const m = current(), o = m.opById.get(id); if (!o) return; state.selected = id; hideTooltip();
  const request = ++inspectorRequest;
  state.reopen = null;
  const l = m.laneById.get(o.lane), g = o.group ? m.groupById.get(o.group) : null, role = roleOf(o.kind);
  const dlg = $('#inspector');
  const facts = [['Duration', fmt(o.end - o.start, true) + (o.open ? ' · still open' : '')], ['Started', stampS(o.start)], [o.open ? 'Observed until' : 'Ended', stampS(o.end)], ['Lane', l ? l.path : o.lane], ['Phase · kind', `${PHASES[o.phase].name}${o.sub ? ' › ' + subgroupName(o.phase, o.sub) : ''} · ${baseKind(o.kind)}`], ['Lifecycle stage', `${(LIFECYCLES[lifecycleOf(o)] || LIFECYCLES.unknown).name} · ${o.lc_rule || 'phase default'}`], ['Model', modelOf(o) ? `${modelOf(o)}${effortOf(o) ? ' · effort ' + effortOf(o) : ''}` : '—'], ['Status', `${o.status}${o.exit != null ? ' · exit ' + o.exit : ''}${o.query_miss ? ' · query miss (not counted as a failure)' : ''}`], ['Rule', o.rule || '—'], ...(o.shares ? [['Compound command', `wall clock shared: ${sharesText(o)} — an equal split, not measured${failure(o) ? '; the command failed, so its later parts may not have run' : ''}`]] : []), ['Retry group', g ? `${g.id} · attempt ${o.attempt || '—'} of ${g.attempts}` : 'none (no repeated identity)'], ['Flags', [o.background ? 'background process: outlived its turn, excluded from totals' : '', o.remote ? 'remote runner' : '', o.queued ? 'poll loop before work' : '', role ? ROLES[role]?.name || role : '', o.parallel ? `${o.parallel} parallel commands` : ''].filter(Boolean).join(', ') || '—']];
  dlg.innerHTML = `<div class="dialog-head"><span class="eyebrow">Operation</span><button class="btn icon-only ghost" data-action="close-inspector" aria-label="Close" autofocus>${icon('close')}</button></div><div class="dialog-body"><span class="chip ${failure(o) ? 'failed' : o.status === 'completed' ? 'passed' : o.status === 'running' ? 'running' : ''}"><i class="color-square" style="background:${PHASES[o.phase].color}"></i>${PHASES[o.phase].name} · ${esc(o.status)}${o.query_miss ? ' · query miss' : ''}</span><h2 id="inspectorTitle">${esc(o.title)}</h2><p class="desc">Timing and labels come from the source event and the rule table. They do not say why the step was slow.</p><dl class="facts">${facts.map(([k, v]) => `<div><dt>${k}</dt><dd class="mono">${esc(v)}</dd></div>`).join('')}</dl><div class="dialog-actions"><button class="btn primary" data-action="focus-op" data-id="${esc(o.id)}">${icon('expand', true)}Show in timeline</button>${g ? `<button class="btn" data-action="focus-group" data-group="${esc(g.id)}">${icon('loop', true)}See full group</button>` : ''}</div><h3>Command / detail</h3><pre class="event-log" id="opDetail">loading…</pre>${g ? `<h3>${esc(g.id)} · ${g.attempts} attempts · ${g.failed} failed</h3><p class="dialog-note desc">Grouped by identical normalized command (redirections stripped). Not inferred from similar text.</p><div class="group-sequence">${g.members.map(id => m.opById.get(id)).filter(Boolean).sort((a, b) => a.start - b.start).map(it => `<button class="group-op ${it.id === id ? 'active' : ''}" data-action="inspect" data-id="${esc(it.id)}"><i class="color-square" style="background:${PHASES[it.phase].color}"></i>${it.attempt ? '#' + it.attempt : esc(ROLES[roleOf(it.kind)]?.name || it.phase)}<span class="subtle">${it.status === 'failed' ? 'failed' : it.status === 'completed' ? 'ok' : esc(it.status)}</span><span class="mono">${fmt(it.end - it.start)}</span></button>`).join('')}</div>` : ''}<details class="json-details"><summary>Source event (raw)</summary><pre class="event-log" id="opSource">loading…</pre></details><details class="json-details"><summary>Normalized operation</summary><pre class="event-log">${esc(JSON.stringify(o, null, 2))}</pre></details></div>`;
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
  state.reopen = () => inspectMarker(key);
  const dlg = $('#inspector'); const def = MARKS[kind] || { name: kind, color: '#aaa' };
  // a user message, a question and a final answer are prose; every other marker is shown as recorded
  const message = ['user_message', 'question', 'final_answer'].includes(kind);
  dlg.innerHTML = `<div class="dialog-head"><span class="eyebrow">Marker</span><div class="answer-actions">${message ? viewToggle() : ''}<button class="btn icon-only ghost" data-action="close-inspector" aria-label="Close" autofocus>${icon('close')}</button></div></div><div class="dialog-body"><span class="chip"><i class="color-square" style="background:${def.color}"></i>${esc(def.name)}</span><h2 id="inspectorTitle">${stampS(mk.t)} · ${esc(l.path)}</h2>${mk.ref && m.laneById.get(mk.ref) ? `<p class="desc">Sub-agent: ${esc(m.laneById.get(mk.ref).path)}</p>` : ''}<div class="prose-block prose-log ${message ? proseView() : 'prose-raw'}">${message ? prose(mk.text || '(no text)') : esc(mk.text || '(no text)')}</div><div class="dialog-actions"><button class="btn primary" data-action="jump" data-t="${mk.t}">${icon('expand', true)}Zoom around this moment</button></div><details class="json-details"><summary>Source event (raw)</summary><pre class="event-log" id="mkSource">loading…</pre></details></div>`;
  if (!dlg.open) dlg.showModal();
  const source = $('#mkSource');
  const ownsRequest = () => request === inspectorRequest && dlg.open && state.page === 'session' && current()?.id === m.id;
  if (mk.src) {
    try {
      const d = await api(`/api/event?session=${encodeURIComponent(m.id)}&file=${encodeURIComponent(mk.src.file)}&off=${mk.src.off}&len=${mk.src.len}`);
      if (ownsRequest()) source.textContent = JSON.stringify(d, null, 1).slice(0, 60000);
    } catch (e) { if (ownsRequest()) source.textContent = 'unavailable'; }
  } else source.textContent = '(no source pointer)';
}
// inspectWait explains a waiting interval: what the agent last said before it stopped (the
// preceding turn's final answer / last final_answer marker) and the user message that ended it.
function inspectWait(laneId, ta, tb) {
  const m = current();
  const l = m.laneById.get(laneId) || root();
  hideTooltip();
  const isUser = l.depth === 0;
  const prev = [...l.turns].filter(t => t.end <= tb + 1000).sort((a, b) => a.end - b.end).pop();
  let finalText = prev && prev.final ? prev.final : '';
  let finalT = prev ? prev.end : ta;
  if (!finalText) {
    const fa = [...l.markers].filter(k => k.kind === 'final_answer' && k.t <= tb + 1000).sort((a, b) => a.t - b.t).pop();
    if (fa) { finalText = fa.text; finalT = fa.t; }
  }
  const since = prev ? prev.end : ta;
  const nextUser = l.markers.filter(k => k.kind === 'user_message' && k.t >= since - 1000).sort((a, b) => a.t - b.t)[0];
  const label = isUser ? 'Waiting for user' : 'Idle (awaiting parent)';
  const lastHead = isUser ? 'Last message from the agent before it waited' : 'Last message from this sub-agent before it went idle';
  const dlg = $('#inspector');
  state.reopen = () => inspectWait(laneId, ta, tb);
  dlg.innerHTML = `<div class="dialog-head"><span class="eyebrow">Interval</span><div class="answer-actions">${viewToggle()}<button class="btn icon-only ghost" data-action="close-inspector" aria-label="Close" autofocus>${icon('close')}</button></div></div><div class="dialog-body"><span class="chip"><i class="color-square" style="background:${PHASES[isUser ? 'wait_user' : 'idle'].color}"></i>${label}</span><h2 id="inspectorTitle">${fmt(tb - ta, true)} · ${esc(l.path)}</h2><p class="desc">${esc(stampS(ta))} → ${esc(stampS(tb))}. No agent activity in this span — the model had produced its answer and was waiting.</p><h3>${lastHead}</h3><div class="prose-block prose-log ${proseView()}">${prose(finalText || '(no final message recorded for the preceding turn)')}</div><div class="dialog-actions"><button class="btn" data-action="jump" data-t="${Math.round(finalT)}">${icon('expand', true)}Jump to that moment</button></div><h3>${isUser ? 'Next user message' : 'Next instruction from the parent'}</h3><div class="prose-block prose-log ${proseView()}">${prose(nextUser ? nextUser.text : '(the session ended while waiting)')}</div></div>`;
  if (!dlg.open) dlg.showModal();
}
// lanePrompt finds the message that started a thread. The root lane's is its first user message.
// A sub-agent's task arrives either as its first user-role message (Codex CLI before 0.144) or,
// since 0.144, as a NEW_TASK agent_message from the parent whose payload is stored encrypted
// (`encrypted_content`) — then only the header is text. Only the NEW_TASK header proves that a
// message is the parent's task; a user-role message is shown as what it is: the first message.
function lanePrompt(l) {
  const mk = l.markers.find(k => k.kind === 'user_message' || (k.kind === 'message_received' && k.text.startsWith('Message Type: NEW_TASK')));
  if (!mk) return null;
  const task = mk.kind === 'message_received';
  const at = task ? mk.text.indexOf('\nPayload:') : -1;
  const payload = at >= 0 ? mk.text.slice(at + '\nPayload:'.length).trim() : mk.text;
  return { marker: mk, task, headerOnly: at >= 0 && payload === '' };
}
// laneFinal is the thread's last final answer: the last closed turn's final text, else the last
// final_answer marker.
function laneFinal(l) {
  const turn = [...l.turns].reverse().find(t => t.final);
  if (turn) return turn.final;
  const mk = [...l.markers].reverse().find(k => k.kind === 'final_answer');
  return mk ? mk.text : '';
}
// inspectLane is the agent card: who the thread is (path, nickname, model), when it ran, the
// prompt it was started with and its final answer — from the lane's own markers, rendered.
async function inspectLane(id) {
  const m = current();
  const l = m.laneById.get(id);
  if (!l) return;
  hideTooltip();
  const request = ++inspectorRequest;
  const { t0, t1 } = laneSpan(l);
  const active = sum((l.active || []).map(iv => iv.e - iv.s));
  const parent = l.parent ? m.laneById.get(l.parent) : null;
  const last = l.turns[l.turns.length - 1];
  const prompt = lanePrompt(l);
  const final = laneFinal(l);
  const name = l.depth === 0 ? MAIN_THREAD : l.path.split('/').pop();
  const facts = [
    ['Nickname', l.nickname || '—'],
    ['Model', l.model ? l.model + (laneEffort(l) ? ' · effort ' + laneEffort(l) : '') : '—'],
    [l.depth ? 'Spawned' : 'Started', stampS(t0)],
    [l.live ? 'Observed until' : 'Ended', stampS(t1)],
    [l.depth ? 'Active (own turns)' : 'Elapsed', fmt(l.depth ? active : t1 - t0, true)],
    ['Turns · ops', `${l.turns.length} · ${l.ops.length}`],
    ['Tokens', l.tokens ? fmtTok(l.tokens.total) : '—'],
    ['Parent', parent ? parent.path : '—'],
    ['Status', l.live ? 'live' : last ? 'last turn ' + last.status : 'no turns'],
    ['Log file', l.file || '—'],
  ];
  // a Claude Code sub-agent's file always starts with the parent's prompt as a user-role message
  const claude = sourceOf(current()) === 'claude';
  const promptHead = !l.depth ? 'First user message' : (prompt && prompt.task) || claude ? 'Prompt from the parent' : 'First message in this thread';
  const promptNote = !prompt ? 'No starting message is recorded in this log.'
    : prompt.headerOnly ? 'Only the message header is stored as text; checking the source event for the payload…'
    : l.depth && !prompt.task && !claude ? `User-role message · ${prompt.marker.text.length} chars. Before Codex CLI 0.144 this is how the parent's task reached a sub-agent.`
    : l.depth && claude ? `The Agent tool's prompt · ${prompt.marker.text.length} chars.`
    : '';
  const dlg = $('#inspector');
  state.reopen = () => inspectLane(id);
  dlg.innerHTML = `<div class="dialog-head"><span class="eyebrow">${l.depth ? 'Sub-agent' : 'Mother agent'}</span><div class="answer-actions">${viewToggle()}<button class="btn icon-only ghost" data-action="close-inspector" aria-label="Close" autofocus>${icon('close')}</button></div></div>`
    + `<div class="dialog-body"><span class="chip">${icon('agents', true)}${esc(l.path)}</span><h2 id="inspectorTitle">${esc(name)}${l.nickname ? ` · ${esc(l.nickname)}` : ''}</h2>`
    + `<dl class="facts">${facts.map(([k, v]) => `<div><dt>${k}</dt><dd class="mono">${esc(v)}</dd></div>`).join('')}</dl>`
    + `<div class="dialog-actions"><button class="btn primary" data-action="zoom-lane" data-lane="${esc(l.id)}">${icon('expand', true)}Zoom to this agent</button></div>`
    + `<h3>${promptHead}${prompt ? ' · ' + stampS(prompt.marker.t) : ''}</h3><p class="desc" id="lanePromptNote">${esc(promptNote)}</p><div class="prose-block prose-log ${proseView()}" id="lanePromptText">${prose(prompt ? prompt.marker.text : '(not recorded)')}</div>`
    + `<h3>Final answer</h3><div class="prose-block prose-log ${proseView()}">${prose(final || '(no final answer recorded)')}</div>`
    + (prompt && prompt.marker.src ? `<details class="json-details"><summary>Source event of the prompt (raw)</summary><pre class="event-log" id="laneSource">loading…</pre></details>` : '')
    + `</div>`;
  if (!dlg.open) dlg.showModal();
  if (!prompt || !prompt.marker.src) return;
  const note = $('#lanePromptNote');
  const source = $('#laneSource');
  const ownsRequest = () => request === inspectorRequest && dlg.open && state.page === 'session' && current()?.id === m.id;
  try {
    const src = prompt.marker.src;
    const d = await api(`/api/event?session=${encodeURIComponent(m.id)}&file=${encodeURIComponent(src.file)}&off=${src.off}&len=${src.len}`);
    if (!ownsRequest()) return;
    source.textContent = JSON.stringify(d, null, 1).slice(0, 60000);
    const content = d.payload?.content || [];
    if (!prompt.headerOnly) {
      // the marker text of an agent message is clipped; the source event has all of it
      const full = content.filter(c => typeof c.text === 'string').map(c => c.text).join('');
      if (full.length > prompt.marker.text.length) $('#lanePromptText').innerHTML = prose(full);
      return;
    }
    const sealed = content.find(c => c.type === 'encrypted_content');
    note.textContent = sealed
      ? `This message's payload is stored encrypted (encrypted_content, ${(sealed.encrypted_content || '').length} chars); no plaintext of it is recorded.`
      : 'The source event carries no payload for this message.';
  } catch (e) {
    if (!ownsRequest()) return;
    source.textContent = 'unavailable';
    if (prompt.headerOnly) note.textContent = 'Only the message header is stored as text; the source event could not be read.';
  }
}
