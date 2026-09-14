'use strict';

const { test } = require('node:test');
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const { join } = require('node:path');
const vm = require('node:vm');

const shell = readFileSync(join(__dirname, 'web/index.html'), 'utf8');
// The SPA is several classic scripts sharing one global scope; run them in page order.
const scripts = [...shell.matchAll(/<script src="([^"]+)"><\/script>/g)]
  .map(match => ({ name: match[1], source: readFileSync(join(__dirname, 'web', match[1]), 'utf8') }));
const flush = async () => { for (let i = 0; i < 16; i++) await Promise.resolve(); };

// Run the complete application, including startup and event registration. DOM nodes
// track replacement so a stale inspector write cannot hide behind a permissive mock.
function harness() {
  const nodes = new Map(), requests = [], intervals = new Map(), timeouts = new Map(), errors = [];
  let timerID = 0;
  function events(object = {}) {
    const listeners = new Map();
    object.addEventListener = (name, fn) => {
      if (!listeners.has(name)) listeners.set(name, []);
      listeners.get(name).push(fn);
    };
    object.emit = (name, event = {}) => {
      for (const fn of listeners.get(name) || []) fn({ target: object, ...event });
    };
    return object;
  }
  function element(id = '') {
    const classes = new Set();
    const node = events({
      id, style: {}, dataset: {}, value: '', textContent: '', disabled: false, open: false,
      clientWidth: 1000, children: new Set(),
      classList: {
        add: x => classes.add(x), remove: x => classes.delete(x),
        toggle: (x, on) => on ? classes.add(x) : classes.delete(x),
      },
      setAttribute() {}, scrollIntoView() {},
      getBoundingClientRect: () => ({ left: 0, top: 0, width: 1000, height: 300 }),
      showModal() { this.open = true; },
      close() { this.open = false; this.emit('close'); },
      closest: () => null,
    });
    let html = '';
    Object.defineProperty(node, 'innerHTML', {
      get: () => html,
      set: value => {
        for (const child of node.children) if (nodes.get(child.id) === child) nodes.delete(child.id);
        node.children.clear(); html = value;
        for (const match of value.matchAll(/\sid="([^"]+)"[^>]*>([^<]*)/g)) {
          const child = element(match[1]); child.textContent = match[2];
          node.children.add(child); nodes.set(child.id, child);
        }
      },
    });
    return node;
  }
  for (const match of shell.matchAll(/\sid="([^"]+)"/g)) nodes.set(match[1], element(match[1]));
  const document = events({
    hidden: false,
    querySelector: selector => selector.startsWith('#') ? nodes.get(selector.slice(1)) || null : element(),
    querySelectorAll: () => [],
  });
  const location = { hash: '#sessions' };
  const window = events({ innerWidth: 1200, innerHeight: 900, scrollTo() {} });
  const context = vm.createContext({
    document, window, location, history: { pushState: (_state, _title, hash) => { location.hash = hash; } },
    console: { error: e => errors.push(e), warn: e => errors.push(e) },
    setInterval: (fn, delay) => { const id = ++timerID; intervals.set(id, { fn, delay }); return id; },
    clearInterval: id => intervals.delete(id),
    setTimeout: fn => { const id = ++timerID; timeouts.set(id, fn); return id; },
    clearTimeout: id => timeouts.delete(id),
    fetch: url => new Promise((resolve, reject) => {
      const request = {
        url, done: false,
        resolve(data) { this.done = true; resolve({ ok: true, json: async () => JSON.parse(JSON.stringify(data)) }); },
        reject(error = new Error('synthetic request failure')) { this.done = true; reject(error); },
      };
      requests.push(request);
    }),
  });
  for (const script of scripts) vm.runInContext(script.source, context, { filename: script.name });
  const run = script => vm.runInContext(script, context);
  function take(url) {
    const request = requests.find(r => r.url === url && !r.done);
    assert.ok(request, `No pending request for ${url}`);
    return request;
  }
  return {
    run, take, requests, errors, document, location,
    node: id => nodes.get(id),
    action(action, data = {}) {
      const target = element(); target.dataset = { action, ...data };
      target.closest = selector => selector === '[data-action]' ? target : null;
      document.emit('click', { target });
    },
    poll() {
      const timer = intervals.get(run('state.pollTimer'));
      assert.ok(timer, 'Polling must be scheduled');
      return timer.fn();
    },
    async ready() {
      take('/api/sessions').resolve([]); await flush();
      take('/api/sessions').resolve([]); await flush();
      assert.equal(errors.length, 0);
      return this;
    },
    async open(id, model = session(id)) {
      run(`go('session', ${JSON.stringify(id)})`);
      take('/api/sessions/' + encodeURIComponent(id)).resolve(model); await flush();
      assert.equal(run('state.id'), id);
      assert.equal(errors.length, 0);
    },
  };
}

function session(id = 'session', lane = 'lane', turn = 'turn', op = 'op-A') {
  return {
    id, title: 'Synthetic session', version: 'v1', source: 'codex', cwd: '/synthetic',
    started: 1000, ended: 121000, now: 121000, live: false, groups: [],
    totals: { ops: 2, user_messages: 0, tokens: {} },
    lanes: [{
      id: lane, path: '/root', depth: 0, started: 1000, ended: 121000,
      turns: [{ id: turn, start: 1000, end: 121000, status: 'completed' }],
      ops: [
        { id: op, lane, turn, title: 'Operation A', phase: 'code', kind: 'read', status: 'completed', start: 2000, end: 3000 },
        { id: 'op-B', lane, turn, title: 'Operation B', phase: 'code', kind: 'read', status: 'completed', start: 5000, end: 6000 },
      ],
      markers: [], segments: [{ s: 1000, e: 121000, p: 'code', op }],
      stages: [{ s: 1000, e: 121000, p: 'code' }], by_phase: { code: 120000 },
    }],
  };
}

// the timeline is two SVGs: root rows in #chartSvg, sub-agent rows in #agentSvg
function timelineHTML(h) { return h.node('chartSvg').innerHTML + (h.node('agentSvg') ? h.node('agentSvg').innerHTML : ''); }

function attributeValues(html, name) {
  return [...html.matchAll(new RegExp(`\\b${name}="([^"]*)"`, 'g'))].map(match => match[1]
    .replace(/&quot;/g, '"').replace(/&#39;/g, "'").replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&amp;/g, '&'));
}

test('source identifiers remain single attributes throughout the rendered UI and URLs', async () => {
  const h = await harness().ready();
  const suffix = '" onmouseover="synthetic&<', id = 'session' + suffix, lane = 'lane' + suffix;
  const turn = 'turn' + suffix, op = 'op' + suffix, child = 'child' + suffix, background = 'background' + suffix;
  const model = session(id, lane, turn, op);
  model.lanes[0].ops[0].status = 'failed';
  model.lanes[0].ops.push({ ...model.lanes[0].ops[0], id: background, background: true, start: 3000, end: 110000 });
  model.lanes[0].markers.push({ t: 4000, kind: 'agent_started', lane, ref: child });
  model.lanes.push({ id: child, parent: lane, path: '/root/child', depth: 1, started: 4000, ended: 121000 });
  h.run(`state.sessions = ${JSON.stringify([{ id, title: 'Example', cwd: '/synthetic', started: 1000, updated: 121000, bytes: 10, agents: 1 }])}; renderFleetRows()`);
  assert.ok(attributeValues(h.node('fleetRows').innerHTML, 'data-id').includes(id));
  await h.open(id, model);
  assert.equal(h.location.hash, '#session/' + encodeURIComponent(id));
  h.run(`state.expanded.add(${JSON.stringify(lane)}); renderTimeline()`);
  assert.ok(attributeValues(h.node('main').innerHTML, 'value').includes(lane));
  const chart = timelineHTML(h);
  for (const value of [lane, child]) assert.ok(attributeValues(chart, 'data-lane').includes(value));
  for (const value of [op, background]) assert.ok(attributeValues(chart, 'data-op').includes(value));
  assert.ok(attributeValues(chart, 'data-turn').includes(lane + ':' + turn));
  assert.ok(attributeValues(chart, 'data-mk').includes(lane + ':4000:agent_started'));
  assert.ok(attributeValues(h.node('operationList').innerHTML, 'data-id').includes(op));
  assert.ok(attributeValues(h.node('agentsTable').innerHTML, 'data-lane').includes(lane));
  const inspecting = h.run(`inspect(${JSON.stringify(op)})`);
  assert.ok(attributeValues(h.node('inspector').innerHTML, 'data-id').includes(op));
  h.take(`/api/sessions/${encodeURIComponent(id)}/op/${encodeURIComponent(op)}`).resolve({ detail: 'safe detail' });
  await inspecting;
  for (const node of ['fleetRows', 'main', 'chartSvg', 'agentSvg', 'operationList', 'agentsTable', 'inspector']) {
    if (h.node(node)) assert.doesNotMatch(h.node(node).innerHTML, /\sonmouseover="/);
  }
});

test('question and final-answer markers may omit text', async () => {
  const h = await harness().ready(), model = session();
  model.lanes[0].markers.push({ t: 2000, kind: 'question', lane: 'lane' }, { t: 120000, kind: 'final_answer', lane: 'lane' });
  await h.open('session', model);
  assert.match(h.node('conversation').innerHTML, /Agent asks/);
  assert.match(h.node('conversation').innerHTML, /Final answer/);
  assert.equal(h.run('root().markers[0].text'), '');
});

for (const reject of [false, true]) test(`navigation ignores an older ${reject ? 'error' : 'response'}`, async () => {
  const h = await harness().ready();
  h.run("go('session', 'A')"); const old = h.take('/api/sessions/A');
  await h.open('B');
  const displayed = h.node('main').innerHTML;
  if (reject) old.reject(); else old.resolve(session('A'));
  await flush();
  assert.equal(h.run('state.id'), 'B'); assert.equal(h.node('main').innerHTML, displayed);
  assert.equal(h.errors.length, 0);
});

test('leaving the session page invalidates a pending model load', async () => {
  const h = await harness().ready();
  h.run("go('session', 'A')"); const old = h.take('/api/sessions/A');
  h.run("go('sessions')"); h.take('/api/sessions').resolve([]); await flush();
  old.resolve(session('A')); await flush();
  assert.equal(h.run('state.page'), 'sessions'); assert.equal(h.run('state.id'), null);
  assert.match(h.node('main').innerHTML, /Search sessions/);
});

test('routing decodes an escaped session ID before loading it', async () => {
  const h = await harness().ready(), id = 'session #with?reserved&characters';
  h.location.hash = '#session/' + encodeURIComponent(id);
  h.run('route()'); h.take('/api/sessions/' + encodeURIComponent(id)).resolve(session(id)); await flush();
  assert.equal(h.run('state.id'), id);
});

for (const reject of [false, true]) test(`inspector ignores an older operation ${reject ? 'error' : 'response'}`, async () => {
  const h = await harness().ready(); await h.open('session');
  const a = h.run("inspect('op-A')"), old = h.take('/api/sessions/session/op/op-A');
  const b = h.run("inspect('op-B')"); h.take('/api/sessions/session/op/op-B').resolve({ detail: 'B detail' }); await b;
  if (reject) old.reject(); else old.resolve({ detail: 'A detail' });
  await a;
  assert.match(h.node('inspector').innerHTML, /Operation B/);
  assert.equal(h.node('opDetail').textContent, 'B detail');
});

test('switching between operation and marker inspectors invalidates both request types', async () => {
  const h = await harness().ready(), model = session();
  model.lanes[0].markers.push({ t: 4000, kind: 'question', lane: 'lane', text: 'Choose', src: { file: '/synthetic/rollout.jsonl', off: 10, len: 20 } });
  await h.open('session', model);
  const a = h.run("inspect('op-A')"), old = h.take('/api/sessions/session/op/op-A');
  const marker = h.run("inspectMarker('lane:4000:question')");
  const markerRequest = h.take('/api/event?file=%2Fsynthetic%2Frollout.jsonl&off=10&len=20');
  const b = h.run("inspect('op-B')"); h.take('/api/sessions/session/op/op-B').resolve({ detail: 'B detail' }); await b;
  old.reject(); markerRequest.reject(); await Promise.all([a, marker]);
  assert.equal(h.node('opDetail').textContent, 'B detail');
  assert.equal(h.node('mkSource'), undefined);
});

test('native close and reopening cancel inspector writes without cancelling the new selection', async () => {
  const h = await harness().ready(); await h.open('session');
  const a = h.run("inspect('op-A')"), old = h.take('/api/sessions/session/op/op-A');
  const oldDetail = h.node('opDetail'), before = oldDetail.textContent;
  h.node('inspector').close();
  const b = h.run("inspect('op-B')");
  // Browsers may deliver a queued close event after the next selection reopened the dialog.
  h.node('inspector').emit('close');
  h.take('/api/sessions/session/op/op-B').resolve({ detail: 'B detail' }); await b;
  old.resolve({ detail: 'A detail' }); await a;
  assert.equal(oldDetail.textContent, before); assert.equal(h.node('opDetail').textContent, 'B detail');
});

test('navigation closes the inspector and suppresses its pending error', async () => {
  const h = await harness().ready(); await h.open('A');
  const pending = h.run("inspect('op-A')"), old = h.take('/api/sessions/A/op/op-A');
  const detail = h.node('opDetail'), before = detail.textContent;
  await h.open('B'); old.reject(); await pending;
  assert.equal(h.node('inspector').open, false); assert.equal(detail.textContent, before);
});

test('polling remains single-flight across timer ticks and rescheduling', async () => {
  const h = await harness().ready(); await h.open('session');
  const first = h.poll(), old = h.take('/api/sessions/session/version');
  const skipped = h.poll();
  h.document.emit('change', { target: { id: 'intervalSelect', value: '30' } });
  const rescheduled = h.poll();
  assert.equal(h.requests.filter(r => r.url.endsWith('/version')).length, 1);
  await Promise.all([skipped, rescheduled]);
  old.resolve({ version: 'stale' }); await first;
  assert.equal(h.requests.filter(r => r.url === '/api/sessions/session').length, 1);
  const next = h.poll(); h.take('/api/sessions/session/version').resolve({ version: 'v1' }); await next;
  assert.equal(h.requests.filter(r => r.url.endsWith('/version')).length, 2);
});

test('a stale version poll cannot supersede a manual refresh', async () => {
  const h = await harness().ready(); await h.open('session');
  const pending = h.poll(), old = h.take('/api/sessions/session/version');
  h.action('refresh');
  h.take('/api/sessions/session?refresh=1').resolve({ ...session(), title: 'Manual refresh', version: 'v2' }); await flush();
  old.resolve({ version: 'v3' }); await flush();
  assert.equal(h.requests.filter(r => r.url.startsWith('/api/sessions/session') && !r.url.includes('/version')).length, 2);
  await pending;
  assert.equal(h.run('current().title'), 'Manual refresh');
});

test('navigation invalidates an old session poll', async () => {
  const h = await harness().ready(); await h.open('A');
  const pending = h.poll(), old = h.take('/api/sessions/A/version');
  await h.open('B'); old.resolve({ version: 'stale' }); await flush();
  assert.equal(h.run('state.id'), 'B');
  assert.equal(h.requests.filter(r => r.url === '/api/sessions/A').length, 1);
  assert.equal(h.requests.filter(r => r.url === '/api/sessions/B').length, 1);
  await pending;
});

test('rescheduling also invalidates a poll that already started loading its model', async () => {
  const h = await harness().ready(); await h.open('session');
  const pending = h.poll(); h.take('/api/sessions/session/version').resolve({ version: 'v2' }); await flush();
  const old = h.take('/api/sessions/session');
  h.document.emit('change', { target: { id: 'intervalSelect', value: '30' } });
  old.resolve({ ...session(), title: 'Old poll', version: 'v2' }); await pending;
  assert.equal(h.run('current().title'), 'Synthetic session');
});

for (const trigger of ['visibility', 'interval']) test(`pending navigation survives ${trigger} poll rescheduling`, async () => {
  const h = await harness().ready(); await h.open('A');
  h.run("go('session', 'B')"); const next = h.take('/api/sessions/B');
  if (trigger === 'visibility') h.document.emit('visibilitychange');
  else h.document.emit('change', { target: { id: 'intervalSelect', value: '30' } });
  if (h.run('state.pollTimer') !== null) {
    const polling = h.poll();
    const version = h.requests.find(r => r.url === '/api/sessions/A/version' && !r.done);
    if (version) {
      version.resolve({ version: 'v2' }); await flush();
      const old = h.requests.find(r => r.url === '/api/sessions/A' && !r.done);
      if (old) old.resolve({ ...session('A'), version: 'v2' });
    }
    await polling;
  }
  next.resolve(session('B')); await flush();
  assert.equal(h.run('state.id'), 'B');
  assert.equal(h.location.hash, '#session/B');
  assert.equal(h.requests.filter(r => r.url === '/api/sessions/A').length, 1);
});

test('manual refresh cannot supersede a pending navigation with the old loaded session', async () => {
  const h = await harness().ready(); await h.open('A');
  h.run("go('session', 'B')"); const next = h.take('/api/sessions/B');
  h.action('refresh');
  const old = h.requests.find(r => r.url === '/api/sessions/A' && !r.done);
  if (old) old.resolve({ ...session('A'), version: 'v2' });
  await flush(); next.resolve(session('B')); await flush();
  assert.equal(h.run('state.id'), 'B');
  assert.equal(h.requests.filter(r => r.url === '/api/sessions/A').length, 1);
});

test('browser Back cancels navigation to a session that has not finished loading', async () => {
  const h = await harness().ready(); await h.open('A');
  h.run("go('session', 'B')"); const pending = h.take('/api/sessions/B');
  h.location.hash = '#session/A'; h.run('route()');
  pending.resolve(session('B')); await flush();
  assert.equal(h.run('state.id'), 'A');
  assert.equal(h.location.hash, '#session/A');
  assert.doesNotMatch(h.node('main').innerHTML, /Parsing session/);
});

// A session with two sub-agents: "early" lives in the first half, "late" in the second.
function agentsSession() {
  const model = session();
  const lane = model.lanes[0];
  lane.markers.push({ t: 5000, kind: 'user_message', lane: 'lane', text: 'Build the thing' });
  lane.markers.push({ t: 10000, kind: 'agent_started', lane: 'lane', ref: 'early', text: '/root/early' });
  lane.markers.push({ t: 80000, kind: 'agent_started', lane: 'lane', ref: 'late', text: '/root/late' });
  model.lanes.push({
    id: 'early', parent: 'lane', path: '/root/early', depth: 1, nickname: 'Galileo', model: 'gpt-test', started: 10500, ended: 30000,
    turns: [{ id: 'e1', start: 10500, end: 30000, status: 'completed', effort: 'high', final: 'Early is done.' }],
    markers: [{ t: 10600, kind: 'user_message', lane: 'early', text: 'Review module A', src: { file: '/synthetic/early.jsonl', off: 5, len: 9 } }],
    active: [{ s: 10500, e: 30000 }], by_phase: { code: 19500 },
  });
  model.lanes.push({
    id: 'late', parent: 'lane', path: '/root/late_verifier_with_long_name', depth: 1, nickname: 'Kepler', model: 'gpt-test', started: 80500, ended: 100000,
    turns: [{ id: 'l1', start: 80500, end: 100000, status: 'completed' }],
    markers: [{ t: 80600, kind: 'message_received', lane: 'late', ref: '/root', text: 'Message Type: NEW_TASK\nTask name: /root/late_verifier_with_long_name\nSender: /root\nPayload:\n', src: { file: '/synthetic/late.jsonl', off: 7, len: 11 } }],
    active: [{ s: 80500, e: 100000 }], by_phase: { code: 19500 },
  });
  return model;
}

test('only sub-agent lanes alive in the visible window are drawn', async () => {
  const h = await harness().ready();
  await h.open('session', agentsSession());
  let lanes = attributeValues(timelineHTML(h), 'data-lane');
  assert.ok(lanes.includes('early') && lanes.includes('late'));
  assert.equal(h.node('laneCount').textContent, '2 sub-agent lanes');
  h.run('setWindow(1000, 61000)');
  lanes = attributeValues(timelineHTML(h), 'data-lane');
  assert.ok(lanes.includes('lane') && lanes.includes('early'));
  assert.ok(!lanes.includes('late'));
  assert.equal(h.node('laneCount').textContent, '2 sub-agent lanes · 1 in this window');
  h.run('setWindow(61000, 121000)');
  lanes = attributeValues(timelineHTML(h), 'data-lane');
  assert.ok(lanes.includes('lane') && lanes.includes('late'));
  assert.ok(!lanes.includes('early'));
});

test('sub-agent labels carry the nickname and model, long names are cut with an ellipsis', async () => {
  const h = await harness().ready();
  await h.open('session', agentsSession());
  const chart = timelineHTML(h);
  assert.match(chart, />Galileo · gpt-test</);
  assert.match(chart, />Kepler · gpt-test</);
  assert.match(chart, />late_verifier_with_…</);
  assert.ok(attributeValues(chart, 'data-lane').filter((v, i, all) => all.indexOf(v) === i).length >= 3, 'every lane row carries its id');
  assert.deepEqual(attributeValues(chart, 'data-action').filter(a => a === 'lane-card').length, 3, 'one card glyph per lane row');
  assert.match(h.node('agentsTable').innerHTML, /sub-agent · Galileo · gpt-test \/ high|root · Galileo · gpt-test \/ high/);
});

test('the agent card shows a plain-text spawn prompt and the final answer', async () => {
  const h = await harness().ready();
  await h.open('session', agentsSession());
  const opening = h.run("inspectLane('early')");
  const html = h.node('inspector').innerHTML;
  assert.match(html, /Galileo/);
  assert.match(html, /gpt-test · effort high/);
  assert.match(html, /First message in this thread/);
  assert.match(html, /Review module A/);
  assert.match(html, /Early is done\./);
  assert.match(h.node('lanePromptNote').textContent, /^User-role message · 15 chars\./);
  h.take('/api/event?file=%2Fsynthetic%2Fearly.jsonl&off=5&len=9').resolve({ payload: { content: [{ type: 'input_text', text: 'Review module A' }] } });
  await opening;
  assert.match(h.node('lanePromptNote').textContent, /^User-role message/);
  assert.match(h.node('laneSource').textContent, /Review module A/);
});

test('the agent card says so when the spawn prompt is stored encrypted', async () => {
  const h = await harness().ready();
  await h.open('session', agentsSession());
  const opening = h.run("inspectLane('late')");
  assert.match(h.node('lanePromptNote').textContent, /Only the message header/);
  h.take('/api/event?file=%2Fsynthetic%2Flate.jsonl&off=7&len=11').resolve({ payload: { content: [{ type: 'input_text', text: 'Message Type: NEW_TASK' }, { type: 'encrypted_content', encrypted_content: 'gAAAAAB' }] } });
  await opening;
  assert.match(h.node('lanePromptNote').textContent, /stored encrypted \(encrypted_content, 7 chars\); no plaintext of it is recorded/);
  assert.match(h.node('inspector').innerHTML, /Prompt from the parent/);
  assert.match(h.node('inspector').innerHTML, /Message Type: NEW_TASK/);
  assert.match(h.node('inspector').innerHTML, /no final answer recorded/);
});

test('the root card shows the first user message; zooming to a lane frames its lifetime', async () => {
  const h = await harness().ready();
  await h.open('session', agentsSession());
  h.run("inspectLane('lane')");
  assert.match(h.node('inspector').innerHTML, /First user message/);
  assert.match(h.node('inspector').innerHTML, /Build the thing/);
  h.action('lane-card', { lane: 'late' });
  assert.match(h.node('inspector').innerHTML, /Kepler/);
  h.action('zoom-lane', { lane: 'late' });
  assert.equal(h.node('inspector').open, false);
  const a = h.run('state.a'), b = h.run('state.b');
  assert.ok(a <= 80000 && b >= 100000, 'window covers the lifetime');
  assert.ok(a >= 40000 && b - a <= 80000, 'window is framed around the lifetime, not the whole session');
});

test('a lane focused in the operations list stays on the timeline outside its window', async () => {
  const h = await harness().ready();
  await h.open('session', agentsSession());
  h.action('lane-focus', { lane: 'early' });
  h.run('setWindow(61000, 121000)');
  const lanes = attributeValues(timelineHTML(h), 'data-lane');
  assert.ok(lanes.includes('early') && lanes.includes('late'), 'the focused lane is pinned');
  assert.equal(h.node('laneCount').textContent, '2 sub-agent lanes · 1 in this window');
  h.action('lane-focus', { lane: 'early' });
  assert.ok(!attributeValues(timelineHTML(h), 'data-lane').includes('early'), 'unfocused, it leaves again');
});

test('effort is stated only when every turn agrees', async () => {
  const h = await harness().ready();
  const model = agentsSession();
  model.lanes[1].turns.push({ id: 'e2', start: 30500, end: 31000, status: 'completed', effort: 'low' });
  await h.open('session', model);
  h.run("inspectLane('early')");
  assert.match(h.node('inspector').innerHTML, /gpt-test · effort mixed/);
  h.run("inspectLane('late')");
  assert.match(h.node('inspector').innerHTML, /<dd class="mono">gpt-test<\/dd>/);
});

test('a clipped agent message is completed from its source event', async () => {
  const h = await harness().ready();
  const model = agentsSession();
  const long = 'Message Type: NEW_TASK\nTask name: /root/late_verifier_with_long_name\nSender: /root\nPayload:\n' + 'x'.repeat(3000);
  model.lanes[2].markers[0].text = long.slice(0, 2999) + '…';
  await h.open('session', model);
  const opening = h.run("inspectLane('late')");
  assert.equal(h.node('lanePromptNote').textContent, '');
  h.take('/api/event?file=%2Fsynthetic%2Flate.jsonl&off=7&len=11').resolve({ payload: { content: [{ type: 'input_text', text: long }] } });
  await opening;
  assert.equal(h.node('lanePromptText').textContent, long);
});

test('phase and retry-role quick filters are mutually exclusive', async () => {
  const h = await harness().ready();
  await h.open('session');
  h.action('filter', { phase: 'compaction' });
  assert.equal(h.run('state.phase'), 'compaction');
  h.action('filter-role', { role: 'retry_after_failure' });
  assert.equal(h.run('state.role'), 'retry_after_failure');
  assert.equal(h.run('state.phase'), 'all', 'choosing a role drops the phase');
  h.action('filter', { phase: 'test' });
  assert.equal(h.run('state.phase'), 'test');
  assert.equal(h.run('state.role'), 'all', 'choosing a phase drops the role');
  h.action('filter', { phase: 'test' });
  assert.equal(h.run('state.phase'), 'all', 'clicking the active phase again clears it');
});

// A root lane with two turns and a wait for the user between them.
function waitingSession() {
  const model = session();
  const lane = model.lanes[0];
  model.ended = model.now = lane.ended = 301000;
  lane.turns = [{ id: 'turn', start: 1000, end: 50000, status: 'completed', final: 'Done with part one.' }, { id: 'turn2', start: 70000, end: 301000, status: 'completed' }];
  lane.ops[1].turn = 'turn2';
  lane.ops[1].start = 80000;
  lane.ops[1].end = 81000;
  lane.segments = [{ s: 1000, e: 50000, p: 'code', op: 'op-A' }, { s: 50000, e: 70000, p: 'wait_user' }, { s: 70000, e: 301000, p: 'code', op: 'op-B' }];
  lane.stages = [{ s: 1000, e: 50000, p: 'code' }, { s: 70000, e: 301000, p: 'code' }];
  lane.by_phase = { code: 280000, wait_user: 20000 };
  lane.markers.push({ t: 69000, kind: 'user_message', lane: 'lane', text: 'Now part two' });
  return model;
}

test('the breakdown shows a record count per row and hides rows with neither time nor records', async () => {
  const h = await harness().ready();
  await h.open('session', waitingSession());
  const body = h.node('breakdownBody').innerHTML;
  const row = phase => (body.match(new RegExp(`data-phase="${phase}"[^]*?</button>`)) || [''])[0];
  assert.match(row('code'), /<span class="count num">2<\/span>/);
  assert.match(row('wait_user'), /<span class="count num">1<\/span>/);
  assert.match(row('wait_user'), /1 intervals in the list/);
  assert.equal(row('test'), '', 'a phase with no time and no operations in the window is hidden');
  assert.equal(row('no_telemetry'), '');
  assert.doesNotMatch(body, /Inside testing & release/, 'an all-empty section is hidden too');
});

test('waiting for the user lists the gaps themselves and opens the waiting inspector', async () => {
  const h = await harness().ready();
  await h.open('session', waitingSession());
  h.action('filter', { phase: 'wait_user' });
  assert.match(h.node('operationCount').textContent, /^1 intervals/);
  const list = h.node('operationList').innerHTML;
  assert.match(list, /data-action="inspect-interval"/);
  assert.match(list, /data-ta="50000" data-tb="70000"/);
  h.action('inspect-interval', { lane: 'lane', phase: 'wait_user', ta: '50000', tb: '70000' });
  assert.equal(h.node('inspector').open, true);
  assert.match(h.node('inspector').innerHTML, /Waiting for user/);
  assert.match(h.node('inspector').innerHTML, /Done with part one\./);
  assert.match(h.node('inspector').innerHTML, /Now part two/);
  h.run('setWindow(100000, 301000)');
  assert.match(h.node('operationCount').textContent, /^0 intervals/);
  assert.doesNotMatch(h.node('breakdownBody').innerHTML, /data-phase="wait_user"/, 'no waiting in this window: the row is hidden');
});

test('the breakdown carries a share column that sums to 100 % per section, sorted by time', async () => {
  const h = await harness().ready();
  await h.open('session', waitingSession());
  const body = h.node('breakdownBody').innerHTML;
  const shares = [...body.matchAll(/data-phase="([a-z_]+)"[^]*?<span class="share num">([^<]*)<\/span>/g)].map(m => [m[1], parseFloat(m[2])]);
  assert.deepEqual(shares.map(s => s[0]), ['code', 'wait_user'], 'rows ordered by time');
  assert.ok(Math.abs(shares.reduce((n, s) => n + s[1], 0) - 100) < 0.2, `shares sum to 100: ${JSON.stringify(shares)}`);
  assert.match(body, /<option value="longest" selected>by time \(%\)<\/option>/);
  h.run("state.breakdownSort = 'records'; renderLower()");
  const byRecords = [...h.node('breakdownBody').innerHTML.matchAll(/data-phase="([a-z_]+)"/g)].map(m => m[1]);
  assert.deepEqual(byRecords, ['code', 'wait_user']);
});

test('the operations list scrolls and grows in chunks instead of paging', async () => {
  const h = await harness().ready();
  const model = session();
  const lane = model.lanes[0];
  for (let i = 0; i < 150; i++) lane.ops.push({ id: 'bulk-' + i, lane: 'lane', turn: 'turn', title: 'Bulk ' + i, phase: 'code', kind: 'read', status: 'completed', start: 10000 + i * 500, end: 10000 + i * 500 + 100 });
  await h.open('session', model);
  assert.match(h.node('operationCount').textContent, /^152 operations/);
  let list = h.node('operationList').innerHTML;
  assert.equal((list.match(/class="operation"/g) || []).length, 60);
  assert.match(list, /60 of 152 · scroll for more/);
  assert.doesNotMatch(h.node('main').innerHTML, /data-action="list-page"/);
  const node = h.node('operationList');
  node.scrollTop = 0; node.clientHeight = 500; node.scrollHeight = 5000;
  h.run('showMoreOperations()');
  assert.equal((h.node('operationList').innerHTML.match(/class="operation"/g) || []).length, 60, 'far from the end: nothing added');
  node.scrollTop = 4400;
  h.run('showMoreOperations()');
  list = h.node('operationList').innerHTML;
  assert.equal((list.match(/class="operation"/g) || []).length, 120);
  assert.match(list, /120 of 152 · scroll for more/);
  h.run('setWindow(1000, 61000)');
  assert.equal(h.run('state.listShown'), 60, 'a new window starts from the first chunk');
});

test('sub-agent rows render in their own scrolling block under the fixed root rows', async () => {
  const h = await harness().ready();
  await h.open('session', agentsSession());
  const head = attributeValues(h.node('chartSvg').innerHTML, 'data-lane'), sub = attributeValues(h.node('agentSvg').innerHTML, 'data-lane');
  assert.ok(head.includes('lane') && !head.includes('early') && !head.includes('late'));
  assert.ok(sub.includes('early') && sub.includes('late') && !sub.includes('lane'));
  assert.equal(h.node('agentScroll').hidden, false);
  await h.open('solo', session('solo'));
  assert.equal(h.node('agentScroll').hidden, true);
  assert.equal(h.node('agentSvg').innerHTML, '');
});

test('identical consecutive operations in one lane fold into one row that unfolds on demand', async () => {
  const h = await harness().ready();
  const model = session();
  const lane = model.lanes[0];
  for (let i = 0; i < 4; i++) lane.ops.push({ id: 'sleep-' + i, lane: 'lane', turn: 'turn', title: 'sleep {"duration_ms":45000}', phase: 'wait_worker', kind: 'sleep', status: 'completed', start: 20000 + i * 10000, end: 25000 + i * 10000 });
  lane.ops.push({ id: 'between', lane: 'lane', turn: 'turn', title: 'sleep {"duration_ms":45000}', phase: 'wait_worker', kind: 'sleep', status: 'completed', start: 90000, end: 95000 });
  lane.ops.push({ id: 'other', lane: 'lane', turn: 'turn', title: 'ls', phase: 'code', kind: 'list_files', status: 'completed', start: 70000, end: 71000 });
  lane.ops.push({ id: 'think', lane: 'lane', turn: 'turn', title: 'reasoning', phase: 'llm', kind: 'reasoning', status: 'completed', start: 36000, end: 39000 });
  await h.open('session', model);
  let list = h.node('operationList').innerHTML;
  assert.match(h.node('operationCount').textContent, /^9 operations · 6 rows, 1 run of identical calls folded/);
  assert.match(list, /×4/);
  assert.match(list, /4 identical calls in a row/);
  assert.ok(!attributeValues(list, 'data-id').includes('sleep-1'), 'members are hidden while folded');
  assert.ok(attributeValues(list, 'data-id').includes('between'), 'a later identical call after another op is not part of the run');
  h.action('run-toggle', { run: 'lane:sleep-0' });
  list = h.node('operationList').innerHTML;
  assert.ok(attributeValues(list, 'data-id').includes('sleep-1'), 'unfolded members are listed');
  assert.match(list, /fold run/);
  assert.match(h.node('operationCount').textContent, /^9 operations · durations/);
});
