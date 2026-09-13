'use strict';

const { test } = require('node:test');
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const { join } = require('node:path');
const vm = require('node:vm');

const source = readFileSync(join(__dirname, 'web/app.js'), 'utf8');
const shell = readFileSync(join(__dirname, 'web/index.html'), 'utf8');
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
  vm.runInContext(source, context, { filename: 'app.js' });
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
  const chart = h.node('chartSvg').innerHTML;
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
  for (const node of ['fleetRows', 'main', 'chartSvg', 'operationList', 'agentsTable', 'inspector']) {
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
  h.take('/api/sessions/session').resolve({ ...session(), title: 'Manual refresh', version: 'v2' }); await flush();
  old.resolve({ version: 'v3' }); await flush();
  assert.equal(h.requests.filter(r => r.url === '/api/sessions/session').length, 2);
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
