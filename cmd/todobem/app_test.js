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
function harness({ hash = '#sessions' } = {}) {
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
  const location = { hash };
  const window = events({ innerWidth: 1200, innerHeight: 900, scrollTo() {} });
  const context = vm.createContext({
    document, window, location, history: { pushState: (_state, _title, hash) => { location.hash = hash; }, replaceState: (_state, _title, hash) => { location.hash = hash; } },
    console: { error: e => errors.push(e), warn: e => errors.push(e) },
    setInterval: (fn, delay) => { const id = ++timerID; intervals.set(id, { fn, delay }); return id; },
    clearInterval: id => intervals.delete(id),
    setTimeout: fn => { const id = ++timerID; timeouts.set(id, fn); return id; },
    clearTimeout: id => timeouts.delete(id),
    fetch: (url, options = {}) => new Promise((resolve, reject) => {
      const request = {
        url, options, done: false,
        // resolve(data, status): status 204 carries no body; any status ≥ 400 is a failed response
        resolve(data, status = 200) {
          this.done = true;
          const body = JSON.stringify(data === undefined ? null : data);
          resolve({ ok: status >= 200 && status < 300, status, json: async () => JSON.parse(body), text: async () => body });
        },
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
    run, take, requests, errors, document, location, window,
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
    // ready boots the app with the gate off: /api/auth, then the two session-list loads
    async ready({ auth = { enabled: false, authenticated: true } } = {}) {
      take('/api/auth').resolve(auth); await flush();
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
  assert.match(h.node('agentsTable').innerHTML, /sub-agent · Galileo · gpt-test \/ high/, 'a sub-agent without a recorded role is still a sub-agent');
  assert.match(h.node('agentsTable').innerHTML, />main thread<\/button><div class="lane-meta">mother agent</, 'the first lane is the main thread');
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

test('a block click frames the block, a second click zooms in; a turn glyph inside the view keeps the window', async () => {
  const h = await harness().ready();
  const model = session();
  model.ended = model.now = model.lanes[0].ended = 3600000;
  model.lanes[0].turns = [{ id: 'turn', start: 1000, end: 3600000, status: 'completed' }];
  await h.open('session', model);
  h.run('setWindow(0, 3600000)');
  // a 5-minute block in a 60-minute view: framed with a margin, centred on the block
  h.run('zoomToBlock(1200000, 1500000)');
  let a = h.run('state.a'), b = h.run('state.b');
  assert.equal(b - a, 390000, 'block × 1.3');
  assert.equal((a + b) / 2, 1350000, 'centred on the block');
  // the same block again already fills the view: zoom in on its centre
  h.run('zoomToBlock(1200000, 1500000)');
  a = h.run('state.a'); b = h.run('state.b');
  assert.equal(b - a, 130000, 'a third of the framed view');
  assert.equal((a + b) / 2, 1350000);
  // a tiny block never goes below two minutes
  h.run('zoomToBlock(1350000, 1351000)');
  assert.equal(h.run('state.b') - h.run('state.a'), 120000);
  // a turn glyph whose turn is in view: the window stays, the band appears
  h.run('setWindow(1000000, 2000000)');
  h.run('focusInterval(1200000, 1500000)');
  assert.deepEqual([h.run('state.a'), h.run('state.b')], [1000000, 2000000], 'the view is kept');
  assert.equal(JSON.stringify(h.run('state.focus')), JSON.stringify({ a: 1200000, b: 1500000 }));
  // partly in view: framed; entirely elsewhere: the whole session for orientation
  h.run('focusInterval(1900000, 2200000)');
  assert.equal(h.run('state.b') - h.run('state.a'), 390000, 'framed');
  h.run('focusInterval(3000000, 3100000)');
  assert.deepEqual([h.run('state.a'), h.run('state.b')], [1000, 3600000], 'whole session');
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

test('"Failures only" lists failed steps and leaves query misses out; both counts are named in the heading', async () => {
  const h = await harness().ready();
  const model = session();
  const lane = model.lanes[0];
  lane.ops.push(
    { id: 'miss', lane: 'lane', turn: 'turn', title: 'rg needle src', phase: 'code', kind: 'search', status: 'failed', exit: 1, query_miss: true, start: 7000, end: 7100 },
    { id: 'fail', lane: 'lane', turn: 'turn', title: 'go test ./...', phase: 'test', kind: 'go test', status: 'failed', exit: 1, start: 8000, end: 9000 },
  );
  model.totals.failed_ops = 1;
  model.totals.query_misses = 1;
  await h.open('session', model);
  assert.match(h.node('main').innerHTML, /1 failed<\/span> · <span [^>]*>1 query misses<\/span>/);
  h.run('state.failedOnly = true; renderLower()');
  const listed = attributeValues(h.node('operationList').innerHTML, 'data-id');
  assert.deepEqual(listed, ['fail'], 'the search miss is not a failure');
  assert.match(h.node('operationCount').textContent, /^1 operation · failures only/);
  h.run('state.failedOnly = false; state.sort = "failed"; renderLower()');
  assert.equal(attributeValues(h.node('operationList').innerHTML, 'data-id')[0], 'fail', '"Failed first" puts the failed step, not the miss, on top');
  assert.match(h.node('operationList').innerHTML, /rg needle src[^]*?failed exit 1 · query miss, not a failure/);
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
  assert.match(row('wait_user'), /1 interval in the list/);
  assert.equal(row('test'), '', 'a phase with no time and no operations in the window is hidden');
  assert.equal(row('no_telemetry'), '');
  assert.doesNotMatch(body, /Attempts &amp; retries/, 'an all-empty section is hidden too');
});

test('waiting for the user lists the gaps themselves and opens the waiting inspector', async () => {
  const h = await harness().ready();
  await h.open('session', waitingSession());
  h.action('filter', { phase: 'wait_user' });
  assert.match(h.node('operationCount').textContent, /^1 interval /);
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

// A session with a code-review turn: the second partition keys the same segments by SDLC stage.
function lifecycleSession() {
  const model = session();
  const lane = model.lanes[0];
  model.ended = model.now = lane.ended = 301000;
  lane.turns = [
    { id: 'turn', start: 1000, end: 50000, status: 'completed' },
    { id: 'turn2', start: 70000, end: 301000, status: 'completed', skill: 'code-review-cc', review: true, lc: 'review' },
  ];
  lane.ops = [
    { id: 'op-A', lane: 'lane', turn: 'turn', title: 'Operation A', phase: 'code', kind: 'read', status: 'completed', start: 2000, end: 3000, lc: 'implement', lc_rule: 'phase code' },
    { id: 'op-T', lane: 'lane', turn: 'turn', title: 'go test', phase: 'test', kind: 'go test', status: 'completed', start: 10000, end: 20000, lc: 'test', lc_rule: 'phase test' },
    { id: 'op-B', lane: 'lane', turn: 'turn2', title: 'go test', phase: 'test', kind: 'go test', status: 'completed', start: 80000, end: 90000, lc: 'review', lc_rule: 'skill code-review-cc' },
    { id: 'op-P', lane: 'lane', turn: 'turn2', title: 'journalctl -u app', phase: 'infra', kind: 'journalctl', status: 'completed', start: 100000, end: 101000, lc: 'review', lc_rule: 'skill code-review-cc' },
  ];
  lane.segments = [
    { s: 1000, e: 2000, p: 'llm', lc: 'implement' },
    { s: 2000, e: 3000, p: 'code', lc: 'implement', op: 'op-A' },
    { s: 3000, e: 10000, p: 'llm', lc: 'test' },
    { s: 10000, e: 20000, p: 'test', lc: 'test', op: 'op-T' },
    { s: 20000, e: 50000, p: 'llm', lc: 'llm' },
    { s: 50000, e: 70000, p: 'wait_user', lc: 'wait_user' },
    { s: 70000, e: 80000, p: 'llm', lc: 'review' },
    { s: 80000, e: 90000, p: 'test', lc: 'review', op: 'op-B' },
    { s: 90000, e: 100000, p: 'llm', lc: 'review' },
    { s: 100000, e: 101000, p: 'infra', lc: 'review', op: 'op-P' },
    { s: 101000, e: 301000, p: 'llm', lc: 'review' },
  ];
  lane.stages = [{ s: 1000, e: 3000, p: 'code' }, { s: 3000, e: 20000, p: 'test' }, { s: 70000, e: 90000, p: 'test' }, { s: 90000, e: 101000, p: 'infra' }];
  lane.by_phase = { llm: 248000, code: 1000, test: 20000, infra: 1000, wait_user: 20000 };
  lane.by_lifecycle = { implement: 2000, test: 17000, llm: 30000, wait_user: 20000, review: 231000 };
  model.totals = { ops: 4, user_messages: 0, tokens: {}, reviews: 1, by_phase: lane.by_phase, by_lifecycle: lane.by_lifecycle };
  return model;
}

test('the breakdown leads with lifecycle stages: same total as the activity list, with each stage split into model and tool time', async () => {
  const h = await harness().ready();
  await h.open('session', lifecycleSession());
  const body = h.node('breakdownBody').innerHTML;
  const lifecycleAt = body.indexOf('Lifecycle stage'), activityAt = body.indexOf('>Activity<');
  assert.ok(lifecycleAt >= 0 && activityAt > lifecycleAt, 'lifecycle section precedes the activity section');
  const row = lc => (body.match(new RegExp(`data-lc="${lc}"[^]*?</button>`)) || [''])[0];
  assert.match(row('review'), /Code review/);
  assert.match(row('review'), /<span class="time num">3m</);
  assert.match(row('review'), /model 3m · tools 11s/, 'a review stage includes its model time and says so');
  assert.match(row('implement'), /model 1s · tools 1s/);
  assert.match(row('llm'), /Model output — no tool call followed/);
  assert.match(row('llm'), /<span class="time num">30s/);
  assert.equal(row('operate'), '', 'a stage with no time and no records is hidden');
  const shares = [...body.matchAll(/data-lc="([a-z_]+)"[^]*?<span class="share num">([^<]*)<\/span>/g)].map(m => parseFloat(m[2]));
  assert.ok(Math.abs(shares.reduce((n, x) => n + x, 0) - 100) < 0.2, `lifecycle shares sum to 100: ${shares}`);
  const activity = [...body.matchAll(/data-phase="([a-z_]+)"[^]*?<span class="share num">([^<]*)<\/span>/g)].map(m => parseFloat(m[2]));
  assert.ok(Math.abs(activity.reduce((n, x) => n + x, 0) - 100) < 0.2, `activity shares still sum to 100: ${activity}`);
  assert.match(row('review'), /<span class="count num">2<\/span>/, 'the two operations inside the review turn');
});

test('a lifecycle row filters the operations list by stage and is exclusive with the activity filter', async () => {
  const h = await harness().ready();
  await h.open('session', lifecycleSession());
  h.action('filter-lifecycle', { lc: 'review' });
  assert.match(h.node('operationCount').textContent, /^2 operations/);
  assert.match(h.node('operationList').innerHTML, /journalctl -u app/);
  assert.doesNotMatch(h.node('operationList').innerHTML, /Operation A/);
  assert.match(h.node('roleFilter').innerHTML, /data-action="clear-lifecycle"[^]*?Code review/);
  h.action('filter', { phase: 'test' });
  assert.equal(h.run('state.lifecycle'), 'all', 'an activity filter replaces the stage filter');
  assert.match(h.node('operationCount').textContent, /^2 operations/, 'both go test runs, whichever stage they served');
  h.action('filter-lifecycle', { lc: 'test' });
  assert.equal(h.run('state.phase'), 'all');
  assert.match(h.node('operationCount').textContent, /^1 operation /, 'only the test run that served the testing stage');
  h.action('filter-lifecycle', { lc: 'wait_user' });
  assert.equal(h.run('state.phase'), 'wait_user', 'a pass-through stage is its activity phase');
  assert.match(h.node('operationCount').textContent, /^1 interval /);
  h.action('clear-filters');
  assert.equal(h.run('state.lifecycle'), 'all');
});

test('the timeline draws a lifecycle strip under the fill for work stages only, and the inspector names the stage and its rule', async () => {
  const h = await harness().ready();
  await h.open('session', lifecycleSession());
  const svg = timelineHTML(h);
  const strips = [...svg.matchAll(/class="lc-strip"[^>]*data-lc="([a-z_]+)"/g)].map(m => m[1]);
  assert.ok(strips.includes('review') && strips.includes('implement'), `strips: ${strips}`);
  assert.ok(!strips.includes('llm') && !strips.includes('wait_user'), 'pass-through stages draw no strip');
  assert.match(svg, /<title>Code review · /);
  // the tiles are part of the page markup; the metrics container is not a tracked node
  assert.match(h.node('main').innerHTML, /Code review<\/span><strong class="metric-value num">3m</);
  assert.doesNotMatch(h.node('main').innerHTML, /Planning<\/span>/);
  h.action('inspect', { id: 'op-P' });
  assert.match(h.node('inspector').innerHTML, /Lifecycle stage<\/dt><dd class="mono">Code review · skill code-review-cc/);
});

test('the guide lists every lifecycle stage with its rule sources and says which have no detector', async () => {
  const h = await harness().ready();
  await h.open('session', lifecycleSession());
  h.action('guide');
  h.take('/api/rules').resolve({ rules: [{ match: 'go test', phase: 'test', kind: 'go test' }], priority: {}, builtin_rules: 1, review_skills: ['(?i)code-review'], lifecycle: { stages: ['plan', 'requirements', 'design', 'implement', 'review', 'test', 'release', 'operate'], defaults: { code: 'implement', build: 'implement', infra: 'implement', test: 'test', release: 'release' }, pins: { 'pr review': 'review', journalctl: 'operate' }, matchers: { skills: { review: ['(?i)code-review'] }, roles: { review: ['^pragmatic$'] }, paths: {} } } });
  await flush();
  const table = h.node('lifecycleTable').innerHTML;
  for (const name of ['Planning', 'Requirements', 'Design', 'Implementation', 'Code review', 'Testing / QA', 'Deployment / release', 'Maintenance / operations']) assert.match(table, new RegExp(name));
  assert.match(table, /Requirements[^]*?no built-in detector/);
  assert.match(table, /command kinds: pr review/);
  assert.match(table, /agent roles: <code>\^pragmatic\$<\/code>/);
  assert.match(table, /after this lane's first release only/);
});

test('the session heading names the source next to the id, model and CLI version', async () => {
  const h = await harness().ready();
  await h.open('session', { ...session(), source: 'codex', model: 'gpt-6-astra', cli: '0.153.4' });
  assert.match(h.node('main').innerHTML, /<span class="source-tag">codex<\/span> Session session · gpt-6-astra · cli 0\.153\.4/);
});

test('the recorded request and the recorded answer share one prose style and one header height', async () => {
  const h = await harness().ready();
  const model = session();
  model.lanes[0].markers = [{ kind: 'user_message', t: 1000, text: 'Do the thing.' }, { kind: 'final_answer', t: 60000, text: 'Done.' }];
  model.lanes[0].turns = [{ id: 'turn', start: 1000, end: 60000, status: 'completed', final: 'Done.' }];
  await h.open('session', model);
  const main = h.node('main').innerHTML;
  assert.match(main, /<div class="answer-head"><span class="eyebrow">First user message · [^<]*<\/span><\/div><p class="prose-block scroll-fade">Do the thing\.<\/p>/);
  assert.match(main, /<div class="answer-block prose-block scroll-fade" tabindex="0">Done\.<\/div>/);
  // a live session without an answer yet keeps the same header, so the two eyebrows still align
  const live = { ...session('live'), live: true };
  live.lanes[0].markers = [{ kind: 'user_message', t: 1000, text: 'Do the thing.' }];
  await h.open('live', live);
  assert.match(h.node('main').innerHTML, /<div id="lastRecorded"><div class="answer-head"><span class="eyebrow">Last answer<\/span><\/div><p class="muted-note">No final answer recorded yet/);
});

test('the stylesheet keeps [hidden] above every display rule (the Lock button hid only in the DOM property)', () => {
  // .btn sets display:inline-flex, which beats the UA's [hidden]{display:none}; with the gate off
  // the Lock button therefore rendered although lockBtn.hidden was true
  const css = readFileSync(join(__dirname, 'web/app.css'), 'utf8');
  assert.match(css, /\[hidden\]\{display:none!important\}/);
});

test('a stage bracket tooltip states the real split: tool time of the phase vs model output before it', async () => {
  const h = await harness().ready();
  const model = session();
  const lane = model.lanes[0];
  // 47 s of model output ending in a 2 s infra command: the bracket is Infra 49 s, the fill is llm
  lane.ops = [{ id: 'dk', lane: 'lane', turn: 'turn', title: 'docker run', phase: 'infra', kind: 'docker', status: 'completed', start: 48000, end: 50000 }];
  lane.segments = [{ s: 1000, e: 48000, p: 'llm' }, { s: 48000, e: 50000, p: 'infra', op: 'dk' }, { s: 50000, e: 121000, p: 'llm' }];
  lane.stages = [{ s: 1000, e: 50000, p: 'infra', n: 1 }];
  lane.by_phase = { llm: 118000, infra: 2000 };
  await h.open('session', model);
  h.run("const bracket = { dataset: { bracket: '1', stage: '1', ta: '1000', tb: '50000', phase: 'infra', lane: 'lane' }, classList: { contains: () => false } }; bracket.closest = () => bracket; tooltip({ target: bracket, clientX: 10, clientY: 10 })");
  const tip = h.node('tooltip').innerHTML;
  assert.match(tip, /Stage: Infrastructure · 49s/);
  assert.match(tip, /tools 2s · model output before them 47s/);
  assert.match(tip, /1 infra operation plus the LLM time before each/);
});

test('the session list shows the last completed answer as its description, verbatim; the session page leaves it to the Last answer panel', async () => {
  const h = await harness().ready();
  // the list: from the index's tail read (last_answer), first paragraph only, markdown stripped
  h.run("go('sessions')");
  h.take('/api/sessions').resolve([{ id: 'abc', title: 'Ship it', cwd: '/synthetic', started: 1000, updated: 2000, bytes: 10, agents: 0, last_answer: 'Deployed **1.0.14** to the device, including the fix.\n\nDetails:\n- a\n- b' }]);
  await flush();
  const rows = h.node('fleetRows').innerHTML;
  assert.match(rows, /<em class="desc"[^>]*>Deployed 1\.0\.14 to the device, including the fix\.<\/em>/);
  assert.doesNotMatch(rows, /Details:/, 'only the first paragraph');
  // the page: from the last completed turn's final message of the loaded model
  const model = session();
  model.lanes[0].turns = [
    { id: 'turn', start: 1000, end: 60000, status: 'completed', final: 'Earlier answer.' },
    { id: 'turn2', start: 61000, end: 121000, status: 'completed', final: 'На iPad 7 установлена и запущена версия.\n\nПодробности ниже.' },
    { id: 'turn3', start: 121000, end: 121000, status: 'aborted', final: '' },
  ];
  await h.open('session', model);
  const main = h.node('main').innerHTML;
  assert.match(main, /<h1>Synthetic session<\/h1><p class="subtitle">/, 'no description under the title: the panel below shows the whole answer');
  assert.doesNotMatch(main, /session-desc/);
});

// ---- lite authentication ----
test('a 401 locks the viewer: the lock screen replaces the page, nothing of a session is rendered, polling stops', async () => {
  const h = await harness().ready({ auth: { enabled: true, authenticated: true } });
  assert.equal(h.node('lockBtn').hidden, false, 'the Lock button shows when the gate is on');
  await h.open('session', session());
  assert.ok(h.run('state.pollTimer') != null, 'polling runs on a session page');
  // the next poll answers 401: the session is dropped and the lock screen shown
  const polling = h.poll();
  h.take('/api/sessions/session/version').resolve({ error: 'auth' }, 401);
  await polling; await flush();
  assert.equal(h.run('state.locked'), true);
  assert.equal(h.run('state.pollTimer'), null, 'polling stopped');
  assert.equal(h.run('state.model'), null, 'no session data kept while locked');
  const main = h.node('main').innerHTML;
  assert.match(main, /<h1 id="lockTitle">Enter a login token<\/h1>/);
  assert.match(main, /todobem token/);
  assert.doesNotMatch(main, /Synthetic session/);
  // navigation is inert while locked
  h.run("go('sessions')");
  assert.equal(h.run('state.locked'), true);
  assert.doesNotMatch(h.node('main').innerHTML, /Sessions in view/);
});

test('a valid token unlocks: JSON POST to /api/login, then a normal boot; a bad token explains why', async () => {
  const h = await harness().ready({ auth: { enabled: true, authenticated: false } });
  // the first list load answers 401 → locked
  h.run("state.locked = false; loadSessions().catch(() => {})");
  h.take('/api/sessions').resolve({ error: 'auth' }, 401); await flush();
  assert.equal(h.run('state.locked'), true);
  // a used token
  h.node('lockToken').value = 'USEDTOKEN';
  h.run("unlock('USEDTOKEN').catch(e => console.error(e))");
  const bad = h.take('/api/login');
  assert.equal(bad.options.method, 'POST');
  assert.equal(bad.options.headers['Content-Type'], 'application/json');
  assert.deepEqual(JSON.parse(bad.options.body), { token: 'USEDTOKEN' });
  bad.resolve({ error: 'auth', reason: 'used' }, 401); await flush();
  assert.match(h.node('main').innerHTML, /already used/);
  assert.equal(h.run('state.locked'), true);
  // a good token: 204, then the app boots again
  h.run("unlock('  goodtoken  ').catch(e => console.error(e))");
  const good = h.take('/api/login');
  assert.deepEqual(JSON.parse(good.options.body), { token: 'goodtoken' }, 'trimmed');
  good.resolve(undefined, 204); await flush();
  h.take('/api/auth').resolve({ enabled: true, authenticated: true }); await flush();
  h.take('/api/sessions').resolve([]); await flush();
  h.take('/api/sessions').resolve([]); await flush();
  assert.equal(h.run('state.locked'), false);
  assert.match(h.node('main').innerHTML, /<h1>Sessions<\/h1>/);
  assert.equal(h.errors.length, 0);
});

test('the CLI link (#token=…) is redeemed once on load and scrubbed from the URL', async () => {
  const h = harness({ hash: '#token=ABCDEFGHIJKLMNOPQRSTUVWXYZ234567ABCDEFGHIJKLMNOPQRST' });
  // boot is already running (scripts executed at harness creation): it must have posted the token
  const req = h.take('/api/login');
  assert.deepEqual(JSON.parse(req.options.body), { token: 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567ABCDEFGHIJKLMNOPQRST' });
  assert.equal(h.location.hash, '#sessions', 'the fragment is replaced before the request is answered');
  req.resolve(undefined, 204); await flush();
  h.take('/api/auth').resolve({ enabled: true, authenticated: true }); await flush();
  h.take('/api/sessions').resolve([]); await flush();
  h.take('/api/sessions').resolve([]); await flush();
  assert.equal(h.run('state.locked'), false);
  assert.match(h.node('main').innerHTML, /<h1>Sessions<\/h1>/);
  assert.equal(h.errors.length, 0);
});

test('a login link arriving as a hash change (tab already open) is redeemed too', async () => {
  const h = await harness().ready({ auth: { enabled: true, authenticated: true } });
  h.location.hash = '#token=ABCDEFGHIJKLMNOPQRSTUVWXYZ234567ABCDEFGHIJKLMNOPQRST';
  h.window.emit('hashchange');
  const req = h.take('/api/login');
  assert.deepEqual(JSON.parse(req.options.body), { token: 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567ABCDEFGHIJKLMNOPQRST' });
  assert.equal(h.location.hash, '#sessions');
  req.resolve(undefined, 204); await flush();
  h.take('/api/auth').resolve({ enabled: true, authenticated: true }); await flush();
  h.take('/api/sessions').resolve([]); await flush();
  h.take('/api/sessions').resolve([]); await flush();
  assert.equal(h.run('state.locked'), false);
  assert.equal(h.errors.length, 0);
});

test('the Lock button posts a logout and locks the viewer', async () => {
  const h = await harness().ready({ auth: { enabled: true, authenticated: true } });
  h.action('lock');
  const req = h.take('/api/logout');
  assert.equal(req.options.method, 'POST');
  req.resolve(undefined, 204); await flush();
  assert.equal(h.run('state.locked'), true);
  assert.match(h.node('main').innerHTML, /Enter a login token/);
});

test('with the gate off the Lock button stays hidden and no login is ever attempted', async () => {
  const h = await harness().ready();
  assert.equal(h.node('lockBtn').hidden, true);
  assert.equal(h.requests.filter(r => r.url === '/api/login').length, 0);
});

test('a 401 while opening a session keeps the lock screen (no "Could not load session") and names the command to run', async () => {
  const h = await harness().ready({ auth: { enabled: true, authenticated: true } });
  h.run("go('session', 'abc')");
  h.take('/api/sessions/abc').resolve({ error: 'auth' }, 401); await flush();
  const main = h.node('main').innerHTML;
  assert.equal(h.run('state.locked'), true);
  assert.doesNotMatch(main, /Could not load session/);
  assert.match(main, /Enter a login token/);
  assert.match(main, /Run the following command in the project root/);
  assert.match(main, /<pre class="lock-cmd">\.\/todobem token -ttl 30d<\/pre>/);
  assert.equal(h.errors.length, 0, 'a lock is not an error');
});

/* ---------- Insights page ---------- */
function insightsReport({ sessions = 3, fallback = '', pending = [] } = {}) {
  const card = (rule, group, time_ms, tokens, extra = {}) => ({
    rule, group, title: rule, exposure: { time_ms, count: 2, tokens }, sessions: 2, of: sessions, no_data: 0,
    distribution: [{ label: rule === 'T1' ? 'under 5 min' : '/root/a', n: 2, time_ms, tokens, sessions: 2 }],
    evidence: [
      { session: 'S1', title: 'First session', lane: '/root', lane_id: 'L1', a: 2000, b: 3000, time_ms: 1000, note: 'one sub-agent at a time' },
      { session: 'S2', title: 'Second session', lane: '/root', lane_id: 'L2', a: 4000, b: 5000, time_ms: 1000, note: 'one sub-agent at a time' },
    ],
    ...extra,
  });
  return {
    generated_at: 1000, params: { cwd: '', period: { kind: '30d', from: 0, to: 1000 } },
    scope: { sessions, live_excluded: 0, pending, root_elapsed_ms: 36000e3, root_in_turn_ms: 18000e3, wait_user_ms: 7200e3, tokens: { input: 1e6, cached: 9e5, output: 1e4, reasoning: 0, total: 1.01e6 }, clis: {} },
    top_time: ['D4'], top_tokens: ['T1'],
    groups: [
      { id: 'sub_agents', time_ms: 3600e3, tokens: { input: 0, cached: 0, output: 0 }, order_time: 0, order_tokens: 1, cards: [card('D4', 'sub_agents', 3600e3, null, { share: { pct: 20, of_ms: 18000e3, of: 'in_turn' } })] },
      { id: 'you_and_the_agent', time_ms: 60e3, tokens: { input: 5e5, cached: 1e5, output: 0 }, order_time: 1, order_tokens: 0, cards: [card('T1', 'you_and_the_agent', 60e3, { input: 5e5, cached: 1e5, output: 0 }, { stats: { starts_after_15m: 3, uncached_after_15m: 4e5 } })] },
    ],
    fallback, sources: [{ id: 'S1', fp: 'a' }], sources_hash: 'h1',
  };
}

async function openInsights(h, report = insightsReport(), sessions = []) {
  h.run("go('insights')");
  h.take('/api/sessions').resolve(sessions); await flush();
  h.take('/api/insights/report?period=30d').resolve(report); await flush();
  assert.equal(h.run('state.page'), 'insights');
  assert.equal(h.errors.length, 0);
}

test('the insights page loads a 30-day report and renders groups, cards and the period', async () => {
  const h = await harness().ready();
  await openInsights(h);
  const html = h.node('main').innerHTML;
  assert.match(html, /Sub-agents ran one after another/);
  assert.match(html, /Cache after a break/);
  assert.match(html, /Report for/);
  assert.match(html, /3 closed sessions/);
  assert.match(html, /Do sub-agents run in parallel/);
  // time axis: the sub-agents group (1 h) comes before "you and the agent" (1 min)
  assert.ok(html.indexOf('id="group-sub_agents"') < html.indexOf('id="group-you_and_the_agent"'));
  // every group is listed, the empty ones say so, not_measured last
  assert.match(html, /Nothing found in this period/);
  assert.ok(html.lastIndexOf('id="group-not_measured"') > html.indexOf('id="group-models_and_effort"'));
  assert.match(html, /The main thread waited 1h while only one sub-agent was working \(of 5h main thread time in turns, 20\.0 %\)\. In 2 of 3 sessions\./);
});

test('the axis switch reorders groups by tokens and back', async () => {
  const h = await harness().ready();
  await openInsights(h);
  h.action('ins-axis', { axis: 'tokens' });
  let html = h.node('main').innerHTML;
  assert.ok(html.indexOf('id="group-you_and_the_agent"') < html.indexOf('id="group-sub_agents"'));
  assert.match(html, /aria-pressed="true">Tokens/);
  h.action('ins-axis', { axis: 'time' });
  html = h.node('main').innerHTML;
  assert.ok(html.indexOf('id="group-sub_agents"') < html.indexOf('id="group-you_and_the_agent"'));
});

test('an evidence row opens the session and focuses the interval', async () => {
  const h = await harness().ready();
  await openInsights(h);
  h.action('ins-evidence', { id: 'S1', a: '2000', b: '3000' });
  h.take('/api/sessions/S1').resolve(session('S1')); await flush();
  assert.equal(h.run('state.page'), 'session');
  assert.equal(h.run('state.id'), 'S1');
  assert.equal(h.run('JSON.stringify(state.focus)'), '{"a":2000,"b":3000}');
  assert.equal(h.run('state.pendingFocus'), null);
});

test('a focus parameter in the session hash highlights the interval after the load', async () => {
  const h = await harness().ready();
  h.location.hash = '#session/S9?focus=5000-6000';
  h.run('route()');
  h.take('/api/sessions/S9').resolve(session('S9')); await flush();
  assert.equal(h.run('state.id'), 'S9');
  assert.equal(h.run('JSON.stringify(state.focus)'), '{"a":5000,"b":6000}');
});

test('changing the period or the project requests a new report', async () => {
  const h = await harness().ready();
  await openInsights(h);
  h.document.emit('change', { target: { id: 'insPeriod', value: '7d' } });
  h.take('/api/sessions').resolve([{ id: 'S1', title: 'One', cwd: '/proj', updated: Date.now(), started: 1 }]); await flush();
  h.take('/api/insights/report?period=7d&cwd=%2Fproj').resolve(insightsReport()); await flush();
  assert.equal(h.run('state.insights.params.period.kind'), '30d', 'the report answers with its own resolved period');
  h.document.emit('change', { target: { id: 'insProject', value: '' } });
  h.take('/api/sessions').resolve([]); await flush();
  h.take('/api/insights/report?period=30d').resolve(insightsReport()); await flush();
  h.document.emit('change', { target: { id: 'insPeriod', value: 'custom' } });
  h.take('/api/sessions').resolve([]); await flush();
  const url = h.requests.find(r => !r.done && r.url.startsWith('/api/insights/report?period=custom')).url;
  assert.match(url, /period=custom&from=\d+&to=\d+$/);
  assert.equal(h.errors.length, 0);
});

test('fewer than three sessions and an empty period are said in plain words', async () => {
  const h = await harness().ready();
  await openInsights(h, insightsReport({ sessions: 2, fallback: 'fewer_than_3_sessions' }));
  assert.match(h.node('main').innerHTML, /Only 2 closed sessions in this period\. Findings across sessions need 3 or more\./);
  h.action('ins-regenerate');
  h.take('/api/sessions').resolve([]); await flush();
  h.take('/api/insights/report?period=30d').resolve(insightsReport({ sessions: 0, fallback: 'no_sessions' })); await flush();
  assert.match(h.node('main').innerHTML, /No closed sessions in this period\. Change the period\./);
  assert.equal(h.errors.length, 0);
});

test('pending sessions show the Analyze button and a scan polls until it finishes', async () => {
  const h = await harness().ready();
  await openInsights(h, insightsReport({ pending: [{ id: 'P1', title: 'Pending' }] }));
  assert.match(h.node('main').innerHTML, /1 session not analyzed yet/);
  h.action('ins-scan');
  h.take('/api/insights/scan?period=30d').resolve({ started: true, queued: 1, scan: { running: true, done: 0, total: 1 } }); await flush();
  assert.match(h.node('reportBar').innerHTML, /Analyzing 0 of 1 sessions/);
  h.run('insightsPoll()'); // timers are mocked: fire the poll by hand
  h.take('/api/insights/status').resolve({ running: false, done: 1, total: 1, errors: 0 }); await flush();
  h.take('/api/sessions').resolve([]); await flush();
  h.take('/api/insights/report?period=30d').resolve(insightsReport()); await flush();
  assert.match(h.node('main').innerHTML, /3 closed sessions/);
  assert.equal(h.errors.length, 0);
});

test('every visible insights string is plain English', async () => {
  const h = await harness().ready();
  const samples = h.run('insightsTextSamples()');
  const banned = /\b(utilize|leverage|facilitate|via|i\.e\.|e\.g\.|etc\.?|paradigm|orthogonal|granular|heuristic|deterministic|counterfactual)\b/i;
  assert.ok(samples.length > 60);
  for (const text of samples) {
    assert.doesNotMatch(text, banned, text);
    for (const sentence of text.split(/(?<=[.!?])\s+/)) {
      const words = sentence.trim().split(/\s+/).filter(Boolean).length;
      assert.ok(words <= 24, `sentence too long (${words} words): ${sentence}`);
    }
  }
});
