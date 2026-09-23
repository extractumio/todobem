'use strict';

const { test } = require('node:test');
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const { join } = require('node:path');
const { spawnSync } = require('node:child_process');
const vm = require('node:vm');

const shell = readFileSync(join(__dirname, 'web/index.html'), 'utf8');
// The SPA is several classic scripts sharing one global scope; run them in page order.
const scripts = [...shell.matchAll(/<script src="([^"]+)"><\/script>/g)]
  .map(match => ({ name: match[1], source: readFileSync(join(__dirname, 'web', match[1]), 'utf8') }));
const flush = async () => { for (let i = 0; i < 16; i++) await Promise.resolve(); };

// Run the complete application, including startup and event registration. DOM nodes
// track replacement so a stale inspector write cannot hide behind a permissive mock.
function harness({ hash = '#sessions', cookie = '' } = {}) {
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
    hidden: false, cookie,
    querySelector: selector => selector.startsWith('#') ? nodes.get(selector.slice(1)) || null : element(),
    querySelectorAll: () => [],
  });
  const location = { hash };
  const window = events({ innerWidth: 1200, innerHeight: 900, scrollTo() {} });
  const context = vm.createContext({
    document, window, location, history: { pushState: (_state, _title, hash) => { location.hash = hash; }, replaceState: (_state, _title, hash) => { location.hash = hash; } },
    btoa, atob, TextEncoder, TextDecoder,
    console: { error: e => errors.push(e), warn: e => errors.push(e) },
    setInterval: (fn, delay) => { const id = ++timerID; intervals.set(id, { fn, delay }); return id; },
    clearInterval: id => intervals.delete(id),
    setTimeout: fn => { const id = ++timerID; timeouts.set(id, fn); return id; },
    clearTimeout: id => timeouts.delete(id),
    fetch: (url, options = {}) => new Promise((resolve, reject) => {
      const request = {
        url, options, done: false,
        // resolve(data, status, headers): status 204 carries no body; any status ≥ 400 is a
        // failed response; headers are the response's, looked up case-insensitively
        resolve(data, status = 200, headers = {}) {
          this.done = true;
          const body = JSON.stringify(data === undefined ? null : data);
          const lower = Object.fromEntries(Object.entries(headers).map(([k, v]) => [k.toLowerCase(), v]));
          resolve({ ok: status >= 200 && status < 300, status, headers: { get: name => lower[name.toLowerCase()] ?? null }, json: async () => JSON.parse(body), text: async () => body });
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
      by_phase: { code: 120000 },
    }],
  };
}

// the timeline is two SVGs: root rows in #chartSvg, sub-agent rows in #agentSvg
function timelineHTML(h) { return h.node('chartSvg').innerHTML + (h.node('agentSvg') ? h.node('agentSvg').innerHTML : ''); }

function attributeValues(html, name) {
  return [...html.matchAll(new RegExp(`\\b${name}="([^"]*)"`, 'g'))].map(match => match[1]
    .replace(/&quot;/g, '"').replace(/&#39;/g, "'").replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&amp;/g, '&'));
}

test('model normalization preserves an older segment lifecycle fallback', () => {
  const h = harness();
  const legacy = session();
  legacy.lanes[0].segments = [
    { s: 1000, e: 2000, p: 'test', op: 'op-A' },
    { s: 2000, e: 3000, p: 'code', lc: 'future-stage', op: 'op-B' },
  ];
  const got = JSON.parse(h.run(`JSON.stringify((m => { prepare(m); return m.lanes[0].segments.map(sg => [sg.lc || '', lifecycleOf(sg)]); })(${JSON.stringify(legacy)}))`));
  assert.deepEqual(got, [['', 'test'], ['unknown', 'unknown']]);
});

test('deploy default-address fallback rejects a process with another explicit address', () => {
  const deploy = readFileSync(join(__dirname, '../../scripts/deploy.sh'), 'utf8');
  const start = deploy.indexOf('is_instance() {');
  const end = deploy.indexOf('\n}\nlisteners()', start);
  assert.ok(start >= 0 && end > start, 'is_instance function found');
  const fn = deploy.slice(start, end + 2);
  const check = line => spawnSync('/bin/sh', ['-c', `set -eu\ncmdline() { printf '%s\\n' "$LINE"; }\nis_binary() { return 0; }\n${fn}\nis_instance 123`], {
    env: { ...process.env, LINE: line, ADDR: '127.0.0.1:7788', AGENT: '' },
  }).status;
  assert.equal(check('/repo/todobem -open=false'), 0, 'a hand-started default instance without -addr remains adoptable');
  assert.equal(check('/repo/todobem -addr 127.0.0.1:7788 -open=false'), 0);
  assert.notEqual(check('/repo/todobem -addr 127.0.0.1:9000 -open=false'), 0);
  assert.notEqual(check('/repo/todobem -addr=127.0.0.1:9000 -open=false'), 0);
});

test('source identifiers remain single attributes throughout the rendered UI and URLs', async () => {
  const h = await harness().ready();
  const suffix = '" onmouseover="synthetic&<', id = 'session' + suffix, lane = 'lane' + suffix;
  const turn = 'turn' + suffix, op = 'op' + suffix, child = 'child' + suffix, background = 'background' + suffix;
  const model = session(id, lane, turn, op);
  model.lanes[0].ops[0].phase = suffix;
  model.lanes[0].ops[0].lc = suffix;
  model.lanes[0].ops[0].shares = [{ phase: suffix, lc: suffix, ms: 1000 }];
  model.lanes[0].segments[0].p = suffix;
  model.lanes[0].segments[0].lc = suffix;
  model.lanes[0].ops[0].status = 'failed';
  model.lanes[0].ops.push({ ...model.lanes[0].ops[0], id: background, background: true, start: 3000, end: 110000 });
  model.lanes[0].markers.push({ t: 4000, kind: 'agent_started', lane, ref: child });
  model.lanes.push({ id: child, parent: lane, path: '/root/child', depth: 1, started: 4000, ended: 121000 });
  h.run(`state.fleetFilter.kind = 'all'; state.sessions = ${JSON.stringify([{ id, title: 'Example', cwd: '/synthetic', started: 1000, updated: 121000, bytes: 10, agents: 1 }])}; renderFleetRows()`);
  assert.ok(attributeValues(h.node('fleetRows').innerHTML, 'data-id').includes(id));
  await h.open(id, model);
  assert.equal(h.run('state.model.lanes[0].ops[0].phase'), 'unknown');
  assert.equal(h.run('state.model.lanes[0].ops[0].lc'), 'unknown');
  assert.equal(h.run('state.model.lanes[0].ops[0].shares[0].phase'), 'unknown');
  assert.equal(h.run('state.model.lanes[0].segments[0].p'), 'unknown');
  assert.equal(h.run('state.model.lanes[0].segments[0].lc'), 'unknown');
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
  const markerRequest = h.take('/api/event?session=session&file=%2Fsynthetic%2Frollout.jsonl&off=10&len=20');
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

// The page follows the server's build: every answer names the UI the server was built with;
// the first one seen is this page's, and a later answer from another build reloads the page
// (a deploy replaced the binary under the tab). A server without the header changes nothing.
test('an answer from another build reloads the page; the same build or no header never does', async () => {
  const h = harness();
  let reloads = 0;
  h.location.reload = () => reloads++;
  h.take('/api/auth').resolve({ enabled: false, authenticated: true }, 200, { 'X-Todobem-Build': 'aaaaaaaaaaaa' }); await flush();
  h.take('/api/sessions').resolve([], 200, { 'x-todobem-build': 'aaaaaaaaaaaa' }); await flush();
  h.take('/api/sessions').resolve([]); await flush();
  assert.equal(reloads, 0, 'the same build, then no header: the page stays');
  // the list page polls nothing: a tab coming back into view asks the gate's state instead
  h.document.hidden = true; h.document.emit('visibilitychange');
  assert.equal(h.requests.filter(r => r.url === '/api/auth').length, 1, 'going hidden asks nothing');
  h.document.hidden = false; h.document.emit('visibilitychange');
  h.take('/api/auth').resolve({ enabled: false, authenticated: true }, 200, { 'X-Todobem-Build': 'aaaaaaaaaaaa' }); await flush();
  assert.equal(reloads, 0, 'the same build on return');
  await h.open('session');
  h.poll();
  h.take('/api/sessions/session/version').resolve({ version: 'changed' }, 200, { 'X-Todobem-Build': 'bbbbbbbbbbbb' }); await flush();
  assert.equal(reloads, 1, 'a new build under the open tab');
  // the answer that carried the new build is never used: the poll saw a changed version, but
  // the old scripts do not fetch (or render) a model from the build replacing them
  assert.equal(h.requests.filter(r => r.url === '/api/sessions/session').length, 1, 'no re-fetch by the old scripts');
  assert.equal(h.errors.length, 0);
});

test('the HTML build cookie rejects a different first API answer instead of blessing mixed assets', async () => {
  const h = harness({ cookie: 'todobem-page-build=aaaaaaaaaaaa' });
  let reloads = 0;
  h.location.reload = () => reloads++;
  h.take('/api/auth').resolve({ enabled: false, authenticated: true }, 200, { 'X-Todobem-Build': 'bbbbbbbbbbbb' });
  await flush();
  assert.equal(reloads, 1);
  assert.equal(h.requests.filter(r => r.url === '/api/sessions').length, 0, 'the old scripts never start rendering the new server');
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
  h.take('/api/event?session=session&file=%2Fsynthetic%2Fearly.jsonl&off=5&len=9').resolve({ payload: { content: [{ type: 'input_text', text: 'Review module A' }] } });
  await opening;
  assert.match(h.node('lanePromptNote').textContent, /^User-role message/);
  assert.match(h.node('laneSource').textContent, /Review module A/);
});

test('the agent card says so when the spawn prompt is stored encrypted', async () => {
  const h = await harness().ready();
  await h.open('session', agentsSession());
  const opening = h.run("inspectLane('late')");
  assert.match(h.node('lanePromptNote').textContent, /Only the message header/);
  h.take('/api/event?session=session&file=%2Fsynthetic%2Flate.jsonl&off=7&len=11').resolve({ payload: { content: [{ type: 'input_text', text: 'Message Type: NEW_TASK' }, { type: 'encrypted_content', encrypted_content: 'gAAAAAB' }] } });
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
  h.take('/api/event?session=session&file=%2Fsynthetic%2Flate.jsonl&off=7&len=11').resolve({ payload: { content: [{ type: 'input_text', text: long }] } });
  await opening;
  // the completed text goes through the prose view, not textContent: rendered, one paragraph
  assert.equal(h.node('lanePromptText').innerHTML, `<p>${long}</p>`);
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
    { id: 'op-A', lane: 'lane', turn: 'turn', title: 'Operation A', phase: 'code', kind: 'read', sub: 'read', status: 'completed', start: 2000, end: 3000, lc: 'implement', lc_rule: 'phase code' },
    { id: 'op-T', lane: 'lane', turn: 'turn', title: 'go test', phase: 'test', kind: 'go test', status: 'completed', start: 10000, end: 20000, lc: 'test', lc_rule: 'phase test' },
    { id: 'op-B', lane: 'lane', turn: 'turn2', title: 'go test', phase: 'test', kind: 'go test', status: 'completed', start: 80000, end: 90000, lc: 'review', lc_rule: 'skill code-review-cc' },
    { id: 'op-P', lane: 'lane', turn: 'turn2', title: 'journalctl -u app', phase: 'infra', kind: 'journalctl', status: 'completed', start: 100000, end: 101000, lc: 'review', lc_rule: 'skill code-review-cc' },
  ];
  lane.segments = [
    { s: 1000, e: 2000, p: 'llm', lc: 'implement' },
    { s: 2000, e: 3000, p: 'code', lc: 'implement', op: 'op-A', sub: 'read' },
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
  lane.by_phase = { llm: 248000, code: 1000, test: 20000, infra: 1000, wait_user: 20000 };
  lane.by_lifecycle = { implement: 2000, test: 17000, llm: 30000, wait_user: 20000, review: 231000 };
  model.totals = { ops: 4, user_messages: 0, tokens: {}, reviews: 1, elapsed_ms: 300000, by_phase: lane.by_phase, by_lifecycle: lane.by_lifecycle };
  return model;
}

test('the code phase is labelled Development and its activity number is tool time only', async () => {
  const h = await harness().ready();
  await h.open('session', lifecycleSession());
  const body = h.node('breakdownBody').innerHTML;
  const row = (body.match(/data-phase="code"[^]*?<\/button>/) || [''])[0];
  assert.match(row, /<span class="label">Development<\/span>/);
  // op-A is 1 s of tool time; the 1 s of model output before it is LLM, not Development
  assert.match(row, /<span class="time num">1s/);
  assert.match(row, /title="Development: 1s · tool calls only · 1 operation in the list/);
  assert.match(row, /<small class="row-sub">tool calls only<\/small>/, 'the row says on screen what the lifecycle rows above it do not: no model time');
  assert.doesNotMatch(row, /bracket/, 'no activity-bracket figure: the stage band carries the model time');
  assert.doesNotMatch(h.node('main').innerHTML, /Coding/);
  assert.match(h.node('main').innerHTML, /<option value="code">Development<\/option>/, 'the phase filter uses the same label');
});

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
  assert.match(row('llm'), /Model output, no tool call/);
  assert.match(row('llm'), /<span class="time num">30s/);
  // not a stage: the llm row sits under "Outside stages", after every stage row; the wait is the
  // same row as under Activity and is listed once, there — the heading says so
  const outsideAt = body.indexOf('>Outside stages<');
  assert.ok(outsideAt > lifecycleAt && outsideAt < activityAt, 'an Outside stages footer sits between the stages and the activity list');
  for (const lc of ['review', 'implement', 'test']) assert.ok(body.indexOf(`data-lc="${lc}"`) < outsideAt, `${lc} is a stage row`);
  assert.ok(body.indexOf('data-lc="llm"') > outsideAt, 'model output without a tool call is not a stage');
  assert.equal(row('wait_user'), '', 'a wait identical to its Activity row is not repeated');
  assert.match(body, /<h3>Lifecycle stage<\/h3><span class="mini-note">same time, by SDLC stage · waits, gaps and unknown: see Activity<\/span>/, 'no stage took any wait here, so no amount is named');
  assert.equal(row('operate'), '', 'a stage with no time and no records is hidden');
  const shares = [...body.matchAll(/data-lc="([a-z_]+)"[^]*?<span class="share num">([^<]*)<\/span>/g)].map(m => parseFloat(m[2]));
  const activity = [...body.matchAll(/data-phase="([a-z_]+)"[^]*?<span class="share num">([^<]*)<\/span>/g)].map(m => [m[1], parseFloat(m[2])]);
  const waitShare = activity.find(([k]) => k === 'wait_user')[1];
  assert.ok(Math.abs(shares.reduce((n, x) => n + x, 0) + waitShare - 100) < 0.2, `the stages, the model output and the wait listed under Activity add up to 100: ${shares} + ${waitShare}`);
  assert.ok(Math.abs(activity.reduce((n, [, x]) => n + x, 0) - 100) < 0.2, `activity shares still sum to 100: ${activity}`);
  assert.match(row('review'), /<span class="count num">2<\/span>/, 'the two operations inside the review turn');
});

test('a wait a turn signal booked to its stage is listed under Outside stages only by its remainder, and says so; Unknown carries its model share', async () => {
  const h = await harness().ready();
  const model = lifecycleSession();
  const lane = model.lanes[0];
  // a hook wait in the plain turn (no signal: it passes through) and a sub-agent wait inside
  // the review turn, which the skill run books to Code review with the rest of that turn; an
  // unknown command in the plain turn, whose model output before it is unknown too
  lane.ops.push(
    { id: 'op-H', lane: 'lane', turn: 'turn', title: 'hook', phase: 'wait_worker', kind: 'hook', sub: 'hooks', status: 'completed', start: 40000, end: 50000, lc: 'wait_worker', lc_rule: 'phase wait_worker' },
    { id: 'op-U', lane: 'lane', turn: 'turn', title: 'frobnicate', phase: 'unknown', kind: 'frobnicate', sub: 'command', status: 'completed', start: 30000, end: 31000, lc: 'unknown', lc_rule: 'phase unknown' },
    { id: 'op-W', lane: 'lane', turn: 'turn2', title: 'Agent', phase: 'wait_worker', kind: 'agent', sub: 'agents', status: 'completed', start: 201000, end: 241000, lc: 'review', lc_rule: 'skill code-review-cc' },
  );
  lane.segments = lane.segments.flatMap(sg => {
    if (sg.s === 20000) return [{ s: 20000, e: 25000, p: 'llm', lc: 'llm' }, { s: 25000, e: 30000, p: 'llm', lc: 'unknown' }, { s: 30000, e: 31000, p: 'unknown', lc: 'unknown', op: 'op-U', sub: 'command' }, { s: 31000, e: 40000, p: 'llm', lc: 'llm' }, { s: 40000, e: 50000, p: 'wait_worker', lc: 'wait_worker', op: 'op-H', sub: 'hooks' }];
    if (sg.s === 101000) return [{ s: 101000, e: 201000, p: 'llm', lc: 'review' }, { s: 201000, e: 241000, p: 'wait_worker', lc: 'review', op: 'op-W', sub: 'agents' }, { s: 241000, e: 301000, p: 'llm', lc: 'review' }];
    return [sg];
  });
  await h.open('session', model);
  const body = h.node('breakdownBody').innerHTML;
  const outsideAt = body.indexOf('>Outside stages<'), activityAt = body.indexOf('>Activity<');
  const lcRow = (body.match(/data-lc="wait_worker"[^]*?<\/button>/) || [''])[0];
  const phaseRow = (body.match(/data-phase="wait_worker"[^]*?<\/button>/) || [''])[0];
  assert.ok(body.indexOf('data-lc="wait_worker"') > outsideAt && body.indexOf('data-lc="wait_worker"') < activityAt, 'the remainder sits under Outside stages');
  assert.match(lcRow, /<span class="time num">10s</, 'the 10 s the review turn did not claim');
  assert.match(lcRow, /<small class="row-sub">40s more inside the stages above<\/small>/, 'the row reconciles with the 50 s under Activity by a number');
  assert.match(body, /<span class="mini-note">same time, by SDLC stage · waits, gaps and unknown: see Activity · the stages hold 40s of them<\/span>/, 'the heading totals what the stages took');
  assert.match(lcRow, /<span class="count num">1<\/span>/, 'one operation outside a stage');
  assert.match(phaseRow, /<span class="time num">50s</, 'the Activity row keeps the whole wait');
  assert.match(phaseRow, /<span class="count num">2<\/span>/);
  // the review stage grew by the 40 s wait it took, as its tools time
  assert.match((body.match(/data-lc="review"[^]*?<\/button>/) || [''])[0], /model 3m · tools 51s/);
  // Unknown: 1 s of command under Activity, 6 s here with the model output before it — split, not "remainder"
  const unknownLc = (body.match(/data-lc="unknown"[^]*?<\/button>/) || [''])[0];
  assert.ok(body.indexOf('data-lc="unknown"') > outsideAt && body.indexOf('data-lc="unknown"') < activityAt, 'Unknown sits under Outside stages');
  assert.match(unknownLc, /<span class="time num">6s</);
  assert.match(unknownLc, /<small class="row-sub">model 5s · tools 1s<\/small>/);
  assert.match((body.match(/data-phase="unknown"[^]*?<\/button>/) || [''])[0], /<span class="time num">1s</);
  assert.equal(h.errors.length, 0);
});

test('a wait a stage took whole leaves no Outside stages row, and the heading still says the stages hold it', async () => {
  const h = await harness().ready();
  const model = lifecycleSession();
  const lane = model.lanes[0];
  lane.ops.push({ id: 'op-W', lane: 'lane', turn: 'turn2', title: 'Agent', phase: 'wait_worker', kind: 'agent', sub: 'agents', status: 'completed', start: 201000, end: 241000, lc: 'review', lc_rule: 'skill code-review-cc' });
  lane.segments = lane.segments.flatMap(sg => sg.s === 101000
    ? [{ s: 101000, e: 201000, p: 'llm', lc: 'review' }, { s: 201000, e: 241000, p: 'wait_worker', lc: 'review', op: 'op-W', sub: 'agents' }, { s: 241000, e: 301000, p: 'llm', lc: 'review' }]
    : [sg]);
  await h.open('session', model);
  const body = h.node('breakdownBody').innerHTML;
  assert.equal(body.indexOf('data-lc="wait_worker"'), -1, 'nothing of the wait is outside a stage: no row');
  assert.match((body.match(/data-phase="wait_worker"[^]*?<\/button>/) || [''])[0], /<span class="time num">40s</, 'Activity keeps the whole wait');
  assert.match(body, /the stages hold 40s of them<\/span>/, 'the heading names the 40 s the stages took, so the wait is not read twice');
  assert.equal(h.errors.length, 0);
});

test('Development lists sub-rows by what its calls did, and a sub-row filters the operations list', async () => {
  const h = await harness().ready();
  await h.open('session', lifecycleSession());
  const body = h.node('breakdownBody').innerHTML;
  const dev = body.indexOf('data-phase="code"'), sub = body.indexOf('data-action="filter-sub" data-sub-phase="code" data-sub="read"');
  assert.ok(dev >= 0 && sub > dev, 'the Reading files sub-row follows the Development row');
  const row = (body.match(/<button class="breakdown-item sub"[^]*?data-sub="read"[^]*?<\/button>/) || [''])[0];
  assert.match(row, /<span class="label">Reading files<\/span>/);
  assert.match(row, /<span class="count num">1<\/span>/);
  assert.match(row, /<span class="time num">1s/);
  assert.doesNotMatch(body, /data-sub="edit"/, 'a sub-row with neither time nor calls is hidden');
  assert.doesNotMatch(body, /data-sub-phase="test"/, 'a phase without subgroups has no sub-rows');
  h.action('filter-sub', { subPhase: 'code', sub: 'read' });
  assert.equal(h.run('state.phase'), 'code');
  assert.equal(h.run('state.sub'), 'read');
  assert.match(h.node('operationCount').textContent, /^1 operation /);
  assert.match(h.node('operationList').innerHTML, /Operation A/);
  assert.match(h.node('roleFilter').innerHTML, /data-action="clear-sub"[^]*?Reading files/);
  assert.match(h.node('breakdownBody').innerHTML, /<button class="breakdown-item sub active"[^]*?data-sub="read"/);
  assert.match(h.node('breakdownBody').innerHTML, /data-phase="code"[^]*?<span class="count num">1<\/span>/, 'the Development count ignores the sub-row filter');
  h.action('filter', { phase: 'test' });
  assert.equal(h.run('state.sub'), 'all', 'a phase filter clears the sub-row');
  h.action('filter-sub', { subPhase: 'code', sub: 'read' });
  h.action('filter-sub', { subPhase: 'code', sub: 'read' });
  assert.equal(h.run('state.phase'), 'all', 'a second click clears both');
  h.action('inspect', { id: 'op-A' });
  assert.match(h.node('inspector').innerHTML, /Development › Reading files · read/);
});

test('a compound command is listed under every phase it shares, the breakdown names the estimate, the inspector lists the split', async () => {
  const h = await harness().ready();
  const model = lifecycleSession();
  const lane = model.lanes[0];
  // op-T (10 s, test) becomes `go build ./... && go test ./...`: 5 s build (compile) + 5 s test
  const op = lane.ops.find(o => o.id === 'op-T');
  Object.assign(op, { title: 'go build ./... && go test ./...', shares: [
    { phase: 'build', kind: 'go build', segment: 'go build ./...', ms: 5000, lc: 'implement', sub: 'compile' },
    { phase: 'test', kind: 'go test', segment: 'go test ./...', ms: 5000, lc: 'test' },
  ] });
  const i = lane.segments.findIndex(sg => sg.op === 'op-T');
  lane.segments.splice(i, 1, { s: 10000, e: 15000, p: 'build', lc: 'implement', op: 'op-T', sub: 'compile', shared: true }, { s: 15000, e: 20000, p: 'test', lc: 'test', op: 'op-T', shared: true });
  lane.by_phase = { llm: 248000, code: 1000, test: 15000, build: 5000, infra: 1000, wait_user: 20000 };
  lane.by_lifecycle = { implement: 7000, test: 12000, llm: 30000, wait_user: 20000, review: 231000 };
  await h.open('session', model);
  const body = h.node('breakdownBody').innerHTML;
  const build = (body.match(/<button class="breakdown-item"[^]*?data-phase="build"[^]*?<\/button>/) || [''])[0];
  assert.match(build, /<small class="row-sub">≈ 5s shared from compound commands<\/small>/, 'the Build row names its estimated part in the label, not only in the tooltip');
  assert.match(build, /<span class="time num">≈ 5s/, 'the number itself reads as an estimate');
  // the stage rows the shares landed in say so the same way: Implementation took the build share, Verification the test share
  const implement = (body.match(/data-lc="implement"[^]*?<\/button>/) || [''])[0];
  assert.match(implement, /<small class="row-sub">model 1s · tools 6s · ≈ 5s shared from compound commands<\/small>/);
  assert.match(implement, /<span class="time num">≈ 7s/);
  assert.match((body.match(/data-lc="test"[^]*?<\/button>/) || [''])[0], /· ≈ 5s shared from compound commands<\/small>[^]*?<span class="time num">≈ 12s/);
  assert.doesNotMatch((body.match(/data-lc="review"[^]*?<\/button>/) || [''])[0], /≈/, 'a stage with no share carries no mark');
  assert.match(h.node('chartSvg').innerHTML, /<rect class="est" [^>]*fill="url\(#estHatch\)"/, 'the estimated span is hatched in the lane fill');
  assert.match(body, /data-sub-phase="build" data-sub="compile"/, 'the share lands in the compile sub-row');
  h.action('filter', { phase: 'build' });
  assert.match(h.node('operationList').innerHTML, /go build \.\/\.\.\. &amp;&amp; go test/, 'a Build filter lists the compound command');
  h.action('inspect', { id: 'op-T' });
  assert.match(h.node('inspector').innerHTML, /wall clock shared: Build 5s \(1\/2\) · Testing 5s \(1\/2\) — an equal split, not measured/);
});

test('Build splits into compiling and installing dependencies, and the empty sub-row is hidden', async () => {
  const h = await harness().ready();
  const model = lifecycleSession();
  const lane = model.lanes[0];
  const op = lane.ops.find(o => o.id === 'op-P');
  Object.assign(op, { title: 'npm ci', phase: 'build', kind: 'npm install', sub: 'deps', lc: 'review' });
  Object.assign(lane.segments.find(sg => sg.op === 'op-P'), { p: 'build', sub: 'deps' });
  lane.by_phase = { llm: 248000, code: 1000, test: 20000, build: 1000, wait_user: 20000 };
  await h.open('session', model);
  const body = h.node('breakdownBody').innerHTML;
  const build = body.indexOf('data-phase="build"');
  const deps = body.indexOf('data-action="filter-sub" data-sub-phase="build" data-sub="deps"');
  assert.ok(build >= 0 && deps > build, 'the Installing dependencies sub-row follows the Build row');
  const row = (body.match(/<button class="breakdown-item sub"[^]*?data-sub="deps"[^]*?<\/button>/) || [''])[0];
  assert.match(row, /<span class="label">Installing dependencies<\/span>/);
  assert.match(row, /<span class="count num">1<\/span>/);
  assert.doesNotMatch(body, /data-sub="compile"/, 'no compile call: the sub-row is hidden');
  h.action('filter-sub', { subPhase: 'build', sub: 'deps' });
  assert.match(h.node('operationCount').textContent, /^1 operation /);
  assert.match(h.node('operationList').innerHTML, /npm ci/);
  h.action('inspect', { id: 'op-P' });
  assert.match(h.node('inspector').innerHTML, /Build › Installing dependencies · npm install/);
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

test('the timeline draws a stage band above the fill for work stages only, and the inspector names the stage and its rule', async () => {
  const h = await harness().ready();
  await h.open('session', lifecycleSession());
  const svg = timelineHTML(h);
  const bands = [...svg.matchAll(/data-band="1"[^>]*data-lc="([a-z_]+)"/g)].map(m => m[1]);
  assert.ok(bands.includes('review') && bands.includes('implement'), `bands: ${bands}`);
  assert.ok(!bands.includes('llm') && !bands.includes('wait_user'), 'time outside the stages leaves the band empty');
  assert.doesNotMatch(svg, /data-bracket=|lc-strip/, 'no activity brackets and no rail: one band, one fill');
  assert.match(svg, /<title>Code review · /);
  // the tiles are part of the page markup; the metrics container is not a tracked node
  assert.match(h.node('main').innerHTML, /Code review<\/span><strong class="metric-value num">3m</);
  assert.doesNotMatch(h.node('main').innerHTML, /Planning<\/span><strong class="metric-value/, 'a stage with no time gets no metric tile');
  h.action('inspect', { id: 'op-P' });
  assert.match(h.node('inspector').innerHTML, /Lifecycle stage<\/dt><dd class="mono">Code review · skill code-review-cc/);
});

test('a live update keeps the reader in place: the window offset is restored after the page is rebuilt', async () => {
  const h = await harness().ready();
  await h.open('session', lifecycleSession());
  h.run("window.scrollY = 1234; window.__to = null; window.scrollTo = (x, y) => { window.__to = [x, y]; };");
  h.run("keepPlace(() => { document.querySelector('#main').innerHTML = '<div id=\"rebuilt\"></div>'; })");
  assert.equal(h.run('JSON.stringify(window.__to)'), '[0,1234]', 'the window is put back where it was');
  assert.ok(h.node('rebuilt'), 'the page was actually rebuilt inside keepPlace');
});

test('the overview SDLC band shows each work stage in its colour with a label, user-wait grey, other time dim', async () => {
  const h = await harness().ready();
  await h.open('session', lifecycleSession());
  const band = h.node('overviewLc').innerHTML;
  const stages = [...new Set([...band.matchAll(/data-lc="([a-z_]+)"/g)].map(m => m[1]))];
  for (const k of ['review', 'implement', 'test', 'llm', 'wait_user']) assert.ok(stages.includes(k), `band stage ${k} (${stages})`);
  // a work stage is coloured and classed 'work'; a wide one shows its name in the strip
  assert.match(band, /<div class="lc-band-seg work"[^>]*background:#cf5e9e[^>]*data-lc="review"[^>]*>\s*<span>Review<\/span>/);
  // user-input time is grey (class 'user'); other pass-through (model output) is the dim neutral
  assert.match(band, /<div class="lc-band-seg user"[^>]*data-lc="wait_user"/);
  assert.match(band, /<div class="lc-band-seg idle"[^>]*data-lc="llm"/);
});

test('the SDLC ring shows every stage: a work stage with time is filled and shows its % of the session, an unused stage is a pale outline showing 0, and it redraws on update', async () => {
  const h = await harness().ready();
  await h.open('session', lifecycleSession());
  const svg = () => { const main = h.node('main').innerHTML; return main.slice(main.indexOf('lifecycle-ring'), main.indexOf('</figure>')); };
  let ring = svg();
  // all eight stages are present as nodes (names + a %/0 each)
  for (const name of ['Plan', 'Reqs', 'Design', 'Impl', 'Review', 'Test', 'Rel', 'Ops']) assert.match(ring, new RegExp('>' + name + '<'), `${name} node`);
  // review has time (by_lifecycle.review = 231000 of 301000 ≈ 77%): filled magenta with a % inside
  // the disc fills bottom-up in proportion to the share: a pale remainder plus the filled level,
  // and at 77 % the waterline is above the centre so the arc takes the large-arc flag
  assert.match(ring, /<circle[^>]*fill="#cf5e9e" fill-opacity="\.18"/, 'review keeps a pale remainder disc');
  assert.match(ring, /<path class="lc-fill" d="M[^"]*A18 18 0 1 0 [^"]*Z" fill="#cf5e9e"\/>/, 'review fills from the bottom, past the half-way line');
  assert.match(ring, /class="lc-pct"[^>]*>77%</, 'review node shows its share of the session');
  // a stage with no time (e.g. release) is a pale-grey fill outlined in its stage colour, showing 0
  assert.match(ring, /fill="var\(--raised\)" stroke="#4aa65f"[^>]*stroke-dasharray="3 2"/, 'release is a pale outline in its colour');
  assert.match(ring, /class="lc-pct"[^>]*>77%</, 'baseline present');
  // a live update runs render(); metricsHTML rebuilds, so the ring redraws with the new totals
  h.run("state.model.totals.elapsed_ms = 600000; state.model.totals.by_lifecycle = { implement: 2000, test: 17000, llm: 30000, wait_user: 20000, review: 231000, idle: 300000 }; render();");
  ring = svg();
  assert.doesNotMatch(ring, /class="lc-pct"[^>]*>77%</, 'the ring redrew with the new totals');
  assert.match(ring, /class="lc-pct"[^>]*>39%</, 'review is now 231000/600000 ≈ 39%');
});


// Hue = stage, tint = activity: the legend names the fill (activity) as a grid of squares and the
// stages as the pipeline itself — steps in lifecycle order, each on its own hue. An activity
// that serves a stage by default (Development → Implementation, Testing → Verification,
// Release & deploy → Deployment, Infrastructure → Maintenance) is a lighter tint of that stage's
// hue, never its hex; every other activity keeps a hue no stage has. The contract is over the
// palette tables — the legend of one session shows only the part of them the session drew.
// The breakdown keeps the shapes: a rail for a stage row, a square for an activity row.
test('the legend separates activity fills from the stage pipeline; every stage has its own hue and its home activity is a lighter tint of it', async () => {
  const h = await harness().ready();
  await h.open('session', lifecycleSession());
  const main = h.node('main').innerHTML;
  const legend = main.slice(main.indexOf('id="legend"'), main.indexOf('<div class="timeline-toolbar"'));
  // a grid: the caption column names the partition and where it shows, then the group's entries
  // in order of kind — what the agent did, then waiting, overhead and gaps
  assert.match(legend, /<div class="legend-caption"><b>Activity<\/b>the fill of a lane<\/div><div class="legend-group"><span class="row"><i class="color-square"/);
  assert.match(legend, /Infrastructure<\/span><span class="row"><i class="color-square" style="background:#d3d8dc"><\/i>Waiting for user/);
  assert.match(legend, /<div class="legend-caption"><b>Lifecycle stage<\/b>the band above each lane, the strip under the overview<\/div><div class="legend-pipeline" id="legendLifecycle"><span class="lc-step"/);
  assert.equal((legend.match(/class="legend-group"/g) || []).length, 1, 'one activity group; the stages are a pipeline, not a second group');
  const steps = [...legend.matchAll(/<span class="lc-step" style="background:(#[0-9a-f]{6})" data-lc="([a-z_]+)">([^<]+)<\/span>/g)].map(m => ({ color: m[1], key: m[2], name: m[3] }));
  assert.deepEqual(steps.map(x => x.name), ['Implementation', 'Code review', 'Verification'], 'the stages this session served, in lifecycle order');
  for (const step of steps) assert.equal(step.color, h.run(`LIFECYCLES[${JSON.stringify(step.key)}].color`), `${step.name} is drawn in its own hue`);
  // the palette contract: every work stage has its own hue; its home activity is the same hue,
  // lighter, and never the same hex; every other activity keeps a hue of its own
  const LIFECYCLES = h.run('LIFECYCLES'), PHASES = h.run('PHASES');
  const stages = Object.fromEntries(Object.values(LIFECYCLES).filter(d => d.work).map(d => [d.name, d.color]));
  assert.equal(Object.keys(stages).length, 8, 'eight work stages');
  assert.equal(new Set(Object.values(stages)).size, 8, 'every stage has its own colour');
  const fills = Object.fromEntries(Object.entries(PHASES).filter(([k]) => k !== 'idle').map(([, d]) => [d.name, d.color]));
  const rgb = c => [1, 3, 5].map(i => parseInt(c.slice(i, i + 2), 16) / 255);
  const lum = c => { const [r, g, b] = rgb(c).map(v => v <= .04045 ? v / 12.92 : ((v + .055) / 1.055) ** 2.4); return .2126 * r + .7152 * g + .0722 * b; };
  const hue = c => { const [r, g, b] = rgb(c), max = Math.max(r, g, b), min = Math.min(r, g, b), d = max - min; const x = max === r ? (g - b) / d : max === g ? 2 + (b - r) / d : 4 + (r - g) / d; return (x * 60 + 360) % 360; };
  const home = { Development: 'Implementation', Testing: 'Verification', 'Release & deploy': 'Deployment / release', Infrastructure: 'Maintenance / operations' };
  for (const [activity, stage] of Object.entries(home)) {
    const a = fills[activity], st = stages[stage];
    assert.ok(a && st, `${activity} and ${stage} are in the palette`);
    assert.notEqual(a, st, `${activity} is not the hex of ${stage}`);
    assert.ok(Math.abs(hue(a) - hue(st)) <= 8, `${activity} ${a} keeps the hue of ${stage} ${st}`);
    assert.ok(lum(a) > lum(st) * 1.15, `${activity} ${a} is the lighter tint of ${stage} ${st}`);
  }
  for (const [activity, color] of Object.entries(fills)) if (!home[activity]) assert.ok(!Object.values(stages).includes(color), `${activity} ${color} is not a stage colour`);
  assert.ok(!Object.values(stages).includes(PHASES.build.color), 'no stage is Build purple');
  // the breakdown uses the shapes: a rail for a stage row, a square for an activity row
  h.run('renderLower()');
  const body = h.node('breakdownBody').innerHTML;
  assert.match(body, /data-lc="review"[^]*?<span class="name"><i class="color-rail" style="background:#cf5e9e">/);
  assert.match(body, /data-phase="test"[^]*?<span class="name"><i class="color-square" style="background:#f0d76c">/);
  assert.match(body, /data-lc="llm"[^]*?<span class="name"><i class="color-square" style="background:#348989">/, 'a row outside the stages is its activity: a square');
});

// The legend is the session's, not the palette's: an activity or a stage no lane spent time in
// is not a colour on the page, so it is not an entry. Every lane counts (a sub-agent's fill and
// band too), the order stays the palette's, a live update adds the entry its new segment
// needs, and a session with no stage says so instead of showing an empty pipeline.
test('the legend lists only the activities and stages the session has', async () => {
  const h = await harness().ready();
  const legendOf = () => { const main = h.node('main').innerHTML; return main.slice(main.indexOf('id="legend"'), main.indexOf('<div class="timeline-toolbar"')); };
  const activities = legend => [...legend.matchAll(/<i class="color-square(?: hatch)?"[^>]*><\/i>([^<]+)</g)].map(m => m[1]);
  const stages = legend => [...legend.matchAll(/<span class="lc-step"[^>]*data-lc="([a-z_]+)">/g)].map(m => m[1]);
  const model = lifecycleSession();
  // a sub-agent lane brings an activity and a stage the root never had
  model.lanes.push({ id: 'child', parent: 'lane', path: '/root/child', depth: 1, started: 4000, ended: 9000, turns: [], ops: [], markers: [], segments: [{ s: 4000, e: 9000, p: 'build', lc: 'release' }], by_phase: { build: 5000, idle: 0 }, by_lifecycle: { release: 5000 } });
  await h.open('session', model);
  let legend = legendOf();
  assert.deepEqual(activities(legend), ['LLM', 'Development', 'Build', 'Testing', 'Infrastructure', 'Waiting for user'], 'the phases with time, in palette order; a zero entry is absent');
  assert.deepEqual(stages(legend), ['implement', 'review', 'test', 'release'], 'the stages with time, in lifecycle order');
  assert.ok(!legend.includes('No telemetry'), 'a phase this session never drew is not an entry');
  assert.ok(!legend.includes('data-lc="plan"'), 'a stage this session never served is not a step');
  // a live update that brings a new phase and stage rebuilds the legend with them
  h.run("state.model.lanes[0].by_phase.no_telemetry = 3000; state.model.lanes[0].by_lifecycle.operate = 3000; render();");
  legend = legendOf();
  assert.ok(activities(legend).includes('No telemetry'), 'the entry appears with the first segment that needs it');
  assert.deepEqual(stages(legend), ['implement', 'review', 'test', 'release', 'operate']);
  // a session that served no stage (model output and the user's wait only) says so
  const chat = session('chat');
  chat.lanes[0].segments = [{ s: 1000, e: 61000, p: 'llm', lc: 'llm' }, { s: 61000, e: 121000, p: 'wait_user', lc: 'wait_user' }];
  chat.lanes[0].by_phase = { llm: 60000, wait_user: 60000 };
  chat.lanes[0].by_lifecycle = { llm: 60000, wait_user: 60000 };
  await h.open('chat', chat);
  legend = legendOf();
  assert.deepEqual(activities(legend), ['LLM', 'Waiting for user']);
  assert.deepEqual(stages(legend), []);
  assert.match(legend, /<div class="legend-pipeline" id="legendLifecycle"><span class="row note">No stage recorded/);
});

// The product version is one literal (APP_VERSION): the sidebar shows it once the app boots, the
// page footer names it on every page.
test('the sidebar and the footer show the one product version', async () => {
  const h = await harness().ready();
  const version = h.run('APP_VERSION');
  assert.match(version, /^\d+\.\d+$/);
  assert.equal(h.node('appVersion').textContent, `todobem / ${version}`);
  assert.ok(h.node('main').innerHTML.includes(`todobem / ${version}</span></footer>`), 'the footer names the version');
});

// The marks legend under the chart follows the same rule: a row for a glyph only when some lane
// draws it — a marker of that kind, a turn end of that status, a background bar, a failure the
// lane marks with the red ×, a stage band. The rows keep their order; no glyph, no legend.
test('the marks legend lists only the glyphs the timeline draws for the session', async () => {
  const h = await harness().ready();
  const marks = () => { const main = h.node('main').innerHTML, at = main.indexOf('id="legendMarks"'); return at < 0 ? null : [...main.slice(at, main.indexOf('</div></section>', at)).matchAll(/<\/svg>([^<]+)</g)].map(m => m[1]); };
  // two completed turns, four ops that passed, a stage band: nothing else is drawn
  await h.open('session', lifecycleSession());
  assert.deepEqual(marks(), ['Turn completed', 'Stage band (the SDLC stage the time served, above the raw fill; colours above)']);
  // a live update brings the user's message and the answer, a background op, an aborted turn in
  // a sub-agent lane, a tool failure and an LLM failure — each adds its row, in legend order
  h.run(`const l = state.model.lanes[0];
    l.markers.push({ t: 1000, kind: 'user_message', lane: 'lane', text: 'go' }, { t: 300000, kind: 'final_answer', lane: 'lane', text: 'done' }, { t: 95000, kind: 'llm_error', lane: 'lane', text: 'invalid args' });
    l.ops.push({ id: 'op-BG', lane: 'lane', turn: 'turn2', title: 'tail -f log', phase: 'infra', kind: 'tail', status: 'completed', start: 102000, end: 200000, background: true });
    l.ops.push({ id: 'op-F', lane: 'lane', turn: 'turn2', title: 'go test', phase: 'test', kind: 'go test', status: 'failed', start: 110000, end: 111000 });
    state.model.lanes.push({ id: 'child', parent: 'lane', path: '/root/child', depth: 1, started: 4000, ended: 9000, turns: [{ id: 'ct', start: 4000, end: 9000, status: 'aborted' }], ops: [], markers: [], segments: [], by_phase: {}, by_lifecycle: {} });
    render();`);
  assert.deepEqual(marks(), ['User message', 'Final answer', 'Background process', 'Turn completed', 'Interrupted', 'Stage band (the SDLC stage the time served, above the raw fill; colours above)', 'Tool failure (non-zero exit)', 'LLM failure: invalid tool call / broken exec script / API error']);
  // a query miss is not a failure, and an LLM failure op is the orange marker, not the red ×
  h.run(`state.model.lanes[0].ops.find(o => o.id === 'op-F').query_miss = true; render();`);
  assert.ok(!marks().includes('Tool failure (non-zero exit)'), 'a query miss draws no red ×, so no row');
  h.run(`state.model.lanes[0].ops.find(o => o.id === 'op-F').query_miss = false; state.model.lanes[0].ops.find(o => o.id === 'op-F').kind = 'llm-invalid-args|go test'; render();`);
  assert.ok(!marks().includes('Tool failure (non-zero exit)'), 'an invalid tool call is the LLM failure row');
  // a session whose only turn never closed: the one glyph it draws
  const orphan = session('orphan');
  orphan.lanes[0].turns[0].status = 'orphaned';
  orphan.lanes[0].segments = [{ s: 1000, e: 121000, p: 'llm', lc: 'llm' }];
  orphan.lanes[0].by_phase = { llm: 120000 };
  await h.open('orphan', orphan);
  assert.deepEqual(marks(), ['Never closed']);
});

// A retry role is neither an activity nor a stage, so its row carries no colour: every role
// square and bar is the one neutral grey, which no activity and no stage uses, and a retry-group
// member in the inspector keeps the square of its activity.
test('retry-role rows are neutral grey, never an activity or stage colour', async () => {
  const h = await harness().ready();
  const model = session();
  model.lanes[0].ops.push(
    { id: 'try1', lane: 'lane', turn: 'turn', title: 'go test ./...', phase: 'test', kind: 'go test|first', status: 'failed', exit: 1, start: 8000, end: 9000, group: 'G01', attempt: 1 },
    { id: 'try2', lane: 'lane', turn: 'turn', title: 'go test ./...', phase: 'test', kind: 'go test|retry_after_failure', status: 'completed', start: 9500, end: 10500, group: 'G01', attempt: 2 },
  );
  await h.open('session', model);
  h.run('renderLower()');
  const body = h.node('breakdownBody').innerHTML;
  const roleRows = [...body.matchAll(/data-action="filter-role" data-role="([a-z_]+)"[^>]*><span class="name"><i class="color-square" style="background:(#[0-9a-f]{6})"><\/i>[^]*?<span class="bar-fill" style="width:[^;]*;background:(#[0-9a-f]{6})"/g)].map(m => ({ role: m[1], square: m[2], bar: m[3] }));
  assert.ok(roleRows.length >= 2, `role rows listed (${roleRows.length})`);
  for (const r of roleRows) { assert.equal(r.square, '#6b7f90', `${r.role} square is the role grey`); assert.equal(r.bar, '#6b7f90', `${r.role} bar is the role grey`); }
  const used = h.run('JSON.stringify(Object.values(PHASES).map(p => p.color).concat(Object.values(LIFECYCLES).map(l => l.color)))');
  assert.ok(!JSON.parse(used).includes('#6b7f90'), 'the role grey is no activity or stage colour');
});

// The brush handles are sliders: a focused one answers the keyboard — an arrow steps its edge
// by 1% of the session, Shift makes it 10%, Home and End are the bounds — and the window keeps
// its one-minute floor, as under the pointer. Near a bound of the plot a handle slides inward
// (it overhangs its edge by 5px), so the plot never cuts it; elsewhere it sits astride.
test('the brush handles answer the keyboard and slide inward at the bounds of the plot', async () => {
  const h = await harness().ready();
  await h.open('session');
  const window_ = () => [h.run('state.a'), h.run('state.b')];
  assert.deepEqual(window_(), [1000, 121000], 'the whole two-minute session');
  const handle = which => { const t = { id: '', dataset: { handle: which } }; t.closest = selector => selector === '[data-handle]' ? t : null; return t; };
  let prevented = 0;
  const key = (which, key, shiftKey = false) => h.document.emit('keydown', { target: handle(which), key, shiftKey, preventDefault: () => prevented++ });
  key('start', 'ArrowRight');
  assert.deepEqual(window_(), [2200, 121000], 'one step is 1% of 120 s');
  key('end', 'ArrowLeft', true);
  assert.deepEqual(window_(), [2200, 109000], 'a shifted step is 10%');
  assert.equal(prevented, 2, 'the page does not scroll on a handled key');
  key('end', 'Tab');
  assert.deepEqual(window_(), [2200, 109000], 'other keys pass');
  assert.equal(prevented, 2);
  // the floor: the end handle cannot cross the start by less than a minute
  for (let i = 0; i < 12; i++) key('end', 'ArrowLeft', true);
  assert.deepEqual(window_(), [2200, 62200]);
  key('start', 'End');
  assert.deepEqual(window_(), [2200, 62200], 'End on the start handle stops a minute short of the end');
  key('start', 'Home'); key('end', 'End');
  assert.deepEqual(window_(), [1000, 121000]);
  // the plot is 1000px wide in the harness: at the bounds both handles are flush with the plot's
  // edge; a window that starts 4px in slides the start handle 4px, one 12px in leaves it astride
  const style = id => h.node(id).style;
  assert.equal(style('brushStart').left, '0.0px'); assert.equal(style('brushEnd').right, '0.0px');
  h.run('setWindow(1000 + 120000 * .004, 121000)');
  assert.equal(style('brushStart').left, '-4.0px');
  h.run('setWindow(1000 + 120000 * .012, 121000 - 120000 * .5)');
  assert.equal(style('brushStart').left, '-5.0px'); assert.equal(style('brushEnd').right, '-5.0px');
  assert.equal(h.errors.length, 0);
});

test('the guide lists every lifecycle stage with its rule sources and says which have no detector', async () => {
  const h = await harness().ready();
  await h.open('session', lifecycleSession());
  h.action('guide');
  h.take('/api/rules').resolve({ rules: [{ match: 'go test', phase: 'test', kind: 'go test' }], priority: {}, builtin_rules: 1, review_skills: ['(?i)code-review'], lifecycle: { stages: ['plan', 'requirements', 'design', 'implement', 'review', 'test', 'release', 'operate'], defaults: { code: 'implement', build: 'implement', infra: 'implement', test: 'test', release: 'release' }, pins: { 'pr review': 'review', journalctl: 'operate' }, matchers: { skills: { review: ['(?i)code-review'] }, roles: { review: ['^pragmatic$'] }, paths: {} } } });
  await flush();
  const table = h.node('lifecycleTable').innerHTML;
  for (const name of ['Planning', 'Requirements', 'Design', 'Implementation', 'Code review', 'Verification', 'Deployment / release', 'Maintenance / operations']) assert.match(table, new RegExp(name));
  assert.match(table, /Requirements[^]*?no built-in detector/);
  assert.match(table, /command kinds: pr review/);
  assert.match(table, /agent roles: <code>\^pragmatic\$<\/code>/);
  assert.match(table, /after this lane's first release only/);
});

test('the session heading names the source next to the id, model and CLI version', async () => {
  const h = await harness().ready();
  await h.open('session', { ...session(), source: 'codex', model: 'gpt-6-astra', cli: '0.153.4' });
  assert.match(h.node('main').innerHTML, /<span class="source-tag source-codex">Codex<\/span> Session session · gpt-6-astra · Codex 0\.153\.4/);
  await h.open('claude-session', { ...session('claude-session'), source: 'claude', model: 'claude-synthetic-1', cli: '2.1.270' });
  assert.match(h.node('main').innerHTML, /<span class="source-tag source-claude">Claude Code<\/span> Session claude-s · claude-synthetic-1 · Claude Code 2\.1\.270/);
});

test('the recorded request and the recorded answer share one prose style and one header height', async () => {
  const h = await harness().ready();
  const model = session();
  model.lanes[0].markers = [{ kind: 'user_message', t: 1000, text: 'Do the thing.' }, { kind: 'final_answer', t: 60000, text: 'Done.' }];
  model.lanes[0].turns = [{ id: 'turn', start: 1000, end: 60000, status: 'completed', final: 'Done.' }];
  await h.open('session', model);
  const main = h.node('main').innerHTML;
  assert.match(main, /<div id="firstMessage"><div class="answer-head"><span class="eyebrow">First user message · [^<]*<\/span><div class="answer-actions"><button [^>]*data-action="text-view"[^>]*>Show raw<\/button><\/div><\/div><div class="prose-block scroll-fade prose-md"><p>Do the thing\.<\/p><\/div>/);
  assert.match(main, /<div class="answer-block prose-block scroll-fade prose-md" tabindex="0"><p>Done\.<\/p><\/div>/);
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

test('a stage band tooltip states the real split: model output attributed to the stage vs its tool time', async () => {
  const h = await harness().ready();
  const model = session();
  const lane = model.lanes[0];
  // 47 s of model output ending in a 2 s infra command, all implementation: the band says
  // Implementation 49 s, the fill under it is llm, and the tooltip says which is which
  lane.ops = [{ id: 'dk', lane: 'lane', turn: 'turn', title: 'docker run', phase: 'infra', kind: 'docker', status: 'completed', start: 48000, end: 50000, lc: 'implement' }];
  lane.segments = [{ s: 1000, e: 48000, p: 'llm', lc: 'implement' }, { s: 48000, e: 50000, p: 'infra', lc: 'implement', op: 'dk' }, { s: 50000, e: 121000, p: 'llm', lc: 'llm' }];
  lane.by_phase = { llm: 118000, infra: 2000 };
  await h.open('session', model);
  h.run("const band = { dataset: { band: '1', stage: '1', ta: '1000', tb: '50000', lc: 'implement', lane: 'lane' }, classList: { contains: () => false } }; band.closest = () => band; tooltip({ target: band, clientX: 10, clientY: 10 })");
  const tip = h.node('tooltip').innerHTML;
  assert.match(tip, /Implementation · 49s/);
  assert.match(tip, /model 47s · tools 2s · 1 tool call</);
  assert.match(tip, /The stage this time served; the fill below shows what ran/);
});

test('the session list marks a session whose agent is waiting for an answer, with how long it has waited', async () => {
  const h = await harness().ready();
  h.run("go('sessions')");
  const now = Date.now();
  h.take('/api/sessions').resolve([
    { id: 'ask', title: 'Needs you', cwd: '/synthetic', started: now - 3 * 3600e3, updated: now - 2 * 3600e3, bytes: 10, agents: 0, question: now - (2 * 3600e3 + 13 * 60e3) },
    { id: 'done', title: 'Finished', cwd: '/synthetic', started: now - 3 * 3600e3, updated: now - 3 * 3600e3, bytes: 10, agents: 0, last_answer: 'Done.' },
  ]);
  await flush();
  const rows = h.node('fleetRows').innerHTML;
  const row = id => (rows.match(new RegExp(`<tr><td><button class="session-link" data-action="session" data-id="${id}"[^]*?</tr>`)) || [''])[0];
  // the chip sits in the Updated cell, on the status row under the timestamp, where the
  // "active" chip goes — never inline after the stamp, whose trailing space browsers place
  // differently row by row
  assert.match(row('ask'), /<td class="mono">\d\d [A-Z][a-z]{2} \d\d:\d\d<div class="status"><span class="chip ask" title="[^"]*"><i class="qmark" aria-hidden="true">\?<\/i>waiting 2h 13m<\/span><\/div><\/td>/);
  assert.doesNotMatch(row('done'), /class="status"/, 'no chips, no status row');
  assert.doesNotMatch(row('ask'), /<button[^>]*>[^]*?qmark[^]*?<\/button>/, 'not inside the title link');
  assert.doesNotMatch(row('done'), /chip ask/, 'a session with no recorded question carries no chip');
  // the mobile tile carries it too
  assert.equal((rows.match(/class="qmark"/g) || []).length, 2);
});

test('an active session wears its chip on the same status row, next to the waiting chip', async () => {
  const h = await harness().ready();
  h.run("go('sessions')");
  const now = Date.now();
  h.take('/api/sessions').resolve([
    { id: 'both', title: 'Running and asking', cwd: '/synthetic', started: now - 3600e3, updated: now - 60e3, bytes: 10, agents: 0, question: now - 5 * 60e3 },
    { id: 'live', title: 'Running', cwd: '/synthetic', started: now - 3600e3, updated: now - 60e3, bytes: 10, agents: 0 },
  ]);
  await flush();
  const rows = h.node('fleetRows').innerHTML;
  const row = id => (rows.match(new RegExp(`<tr><td><button class="session-link" data-action="session" data-id="${id}"[^]*?</tr>`)) || [''])[0];
  assert.match(row('both'), /\d\d:\d\d<div class="status"><span class="chip live" style="[^"]*"><i class="dot"><\/i>active<\/span><span class="chip ask" [^]*?waiting 5m<\/span><\/div><\/td>/);
  assert.match(row('live'), /\d\d:\d\d<div class="status"><span class="chip live" style="[^"]*"><i class="dot"><\/i>active<\/span><\/div><\/td>/);
});

test('an unanswered question stays pending however old it is: the source signal, not its duration, controls the chip and rank', async () => {
  const h = await harness().ready();
  h.run("go('sessions')");
  const now = Date.now();
  const day = 24 * 3600e3;
  h.take('/api/sessions').resolve([
    { id: 'fresh', title: 'Fresh', cwd: '/synthetic', started: now - 3 * day, updated: now - 2 * day, bytes: 10, agents: 0 },
    { id: 'stale', title: 'Asked long ago', cwd: '/synthetic', started: now - 12 * day, updated: now - 11 * day, bytes: 10, agents: 0, question: now - (11 * day + 8 * 3600e3) },
    { id: 'recent', title: 'Asked yesterday', cwd: '/synthetic', started: now - 2 * day, updated: now - day + 3600e3, bytes: 10, agents: 0, question: now - day + 3600e3 },
  ]);
  await flush();
  const rows = h.node('fleetRows').innerHTML;
  const row = id => (rows.match(new RegExp(`<tr><td><button class="session-link" data-action="session" data-id="${id}"[^]*?</tr>`)) || [''])[0];
  assert.match(row('stale'), /chip ask[^]*?waiting 11d 8h<\/span>/, 'an old unanswered question remains literal pending state');
  assert.match(row('recent'), /chip ask[^]*?waiting 23h<\/span>/);
  assert.equal((rows.match(/class="qmark"/g) || []).length, 4, 'both pending sessions appear in the table and mobile tiles');
  // the rank follows the source signal: both unanswered questions stay above ordinary rows
  const order = [...rows.matchAll(/<tr><td><button class="session-link" data-action="session" data-id="([^"]+)"/g)].map(m => m[1]);
  assert.deepEqual(order, ['recent', 'stale', 'fresh']);
  // the session page's top bar reads the same summary
  assert.match(h.run(`askChip(state.sessions.find(s => s.id === 'stale'), ${now})`), /^<span class="chip ask"/);
  assert.match(h.run(`askChip(state.sessions.find(s => s.id === 'recent'), ${now})`), /^<span class="chip ask"/);
});

test('an age reads in seconds, minutes, hours, and in days from two days on', async () => {
  const h = await harness().ready();
  const now = Date.now();
  assert.equal(h.run(`ago(${now - 20e3})`), '20s ago');
  assert.equal(h.run(`ago(${now - 59 * 60e3})`), '59m ago');
  assert.equal(h.run(`ago(${now - 47 * 3600e3})`), '47h ago');
  assert.equal(h.run(`ago(${now - 245 * 3600e3})`), '10d ago');
});

test('the default order puts active sessions first, then those waiting for an answer, then the rest by update time; an explicit sort ignores the rank', async () => {
  const h = await harness().ready();
  h.run("go('sessions')");
  const now = Date.now();
  h.take('/api/sessions').resolve([
    { id: 'fresh', title: 'Fresh', cwd: '/synthetic', started: now - 3600e3, updated: now - 20 * 60e3, bytes: 30, agents: 0 },
    { id: 'ask', title: 'Needs you', cwd: '/synthetic', started: now - 5 * 3600e3, updated: now - 2 * 3600e3, bytes: 10, agents: 0, question: now - 2 * 3600e3 },
    { id: 'live', title: 'Running', cwd: '/synthetic', started: now - 3600e3, updated: now - 60e3, bytes: 20, agents: 0 },
    { id: 'old', title: 'Old', cwd: '/synthetic', started: now - 9 * 3600e3, updated: now - 8 * 3600e3, bytes: 40, agents: 0 },
  ]);
  await flush();
  const order = () => [...h.node('fleetRows').innerHTML.matchAll(/<tr><td><button class="session-link" data-action="session" data-id="([^"]+)"/g)].map(m => m[1]);
  assert.deepEqual(order(), ['live', 'ask', 'fresh', 'old']);
  h.run("state.fleetSort = 'size'; renderFleetRows()");
  assert.deepEqual(order(), ['old', 'fresh', 'live', 'ask']);
});

test('the session page wears the waiting chip in its top bar while the question has no answer, and drops it once the summaries say it was answered', async () => {
  const h = harness({ hash: '#session/ask' });
  const now = Date.now();
  const summary = { id: 'ask', title: 'Needs you', cwd: '/synthetic', started: now - 3 * 3600e3, updated: now - 2 * 3600e3, bytes: 10, agents: 0, question: now - (2 * 3600e3 + 13 * 60e3) };
  h.take('/api/auth').resolve({ enabled: false, authenticated: true }); await flush();
  h.take('/api/sessions').resolve([summary]); await flush();
  h.take('/api/sessions/ask').resolve(session('ask')); await flush();
  assert.equal(h.errors.length, 0);
  assert.match(h.node('liveLabel').innerHTML, /Session closed · [^<]*<\/span><span class="chip ask" title="[^"]*"><i class="qmark" aria-hidden="true">\?<\/i>waiting 2h 13m<\/span>$/);
  // the file grew (the answer): the session reloads, then the summaries, and the chip is gone
  const polling = h.poll();
  h.take('/api/sessions/ask/version').resolve({ version: 'v2' }); await flush();
  h.take('/api/sessions/ask').resolve({ ...session('ask'), version: 'v2' }); await flush();
  h.take('/api/sessions').resolve([{ ...summary, question: undefined }]); await flush();
  await polling;
  assert.equal(h.errors.length, 0);
  assert.doesNotMatch(h.node('liveLabel').innerHTML, /chip ask/);
});

test('the session list shows the last completed answer as its description, verbatim; the session page leaves it to the Last answer panel', async () => {
  const h = await harness().ready();
  // the list: from the index's tail read (last_answer), first paragraph only, markdown stripped
  h.run("go('sessions')");
  h.run("state.fleetFilter.kind = 'all'"); // the fixture's timestamps predate the 30-day default
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
    rule, group, title: rule, exposure: { time_ms, main_ms: time_ms, count: 2, tokens }, sessions: 2, of: sessions, no_data: 0,
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

test('a check card is ranked by sessions, wears its chip, counts sessions and reaches the top findings', async () => {
  const h = await harness().ready();
  const report = insightsReport();
  report.top_checks = ['D17'];
  report.groups.push({ id: 'verification', time_ms: 0, tokens: { input: 0, cached: 0, output: 0 }, order_time: 2, order_tokens: 2, cards: [{
    rule: 'D17', group: 'verification', title: 'D17', class: 'check', exposure: { time_ms: 0, count: 3, tokens: null }, sessions: 3, of: 20, no_data: 1, reason: 'an unknown command after the last edit',
    stats: { edit_turns: 12, edit_turns_unverified: 7, tests: 30, tests_failed: 4, verified_by_hook: 2, hook_sessions: 5, last_verdict_failed: 1, 'verified_kind:go test': 9, 'verified_kind:lint': 2, verified_static_only: 1 },
    distribution: [{ label: 'Codex', n: 2, time_ms: 0, sessions: 2, of: 12 }, { label: 'Claude Code', n: 1, time_ms: 0, sessions: 1, of: 8 }],
    evidence: [{ session: 'S1', title: 'First session', lane: '/root', lane_id: 'L1', a: 2000, b: 62000, time_ms: 0, note: '4 edits; no test ran in this session' }],
  }] });
  await openInsights(h, report);
  const html = h.node('main').innerHTML;
  assert.match(html, /Verification loop/);
  assert.match(html, /Were the last changes tested and reviewed before the answer\?/);
  // the chip, a rank number (not "info"), the denominator wording with the not-applicable sessions left out
  assert.match(html, /<span class="chip info-chip check-chip">A check<\/span>/);
  assert.match(html, /D17 · 0[0-9]<\/span>/);
  assert.doesNotMatch(html, /D17 · info/);
  assert.match(html, /In 3 of 20 sessions with changes, no test passed after the last edit\. 7 of 12 turns with edits had no passing test after their last edit\. 4 of 30 test runs failed\. In 1 session the last test failed\. First passing check after the last edit, in the verified sessions: go test 9, lint 2\. 1 session had a lint, type check or syntax check alone\. 5 sessions ran stop hooks; 2 were verified by one\. No data in 1 session \(an unknown command after the last edit\)\./);
  // distribution rows count sessions over their own denominator, the evidence row shows the interval's length
  assert.match(html, /<span class="label" title="Codex">Codex<\/span>[\s\S]*?2 of 12/);
  assert.match(html, /<span class="label" title="Claude Code">Claude Code<\/span>[\s\S]*?1 of 8/);
  assert.match(html, /data-a="2000" data-b="62000"[\s\S]*?<span class="mono">1m<\/span>/);
  // the top findings strip lists the check after the time-ranked cards, valued in sessions
  const strip = html.slice(html.indexOf('class="top-findings"'), html.indexOf('class="insight-groups"'));
  assert.ok(strip.indexOf('data-rule="D4"') < strip.indexOf('data-rule="D17"'));
  assert.match(strip, /data-rule="D17"[\s\S]*?3 of 20 sessions/);
  assert.equal(h.errors.length, 0);
});

test('a top finding scrolls its card to just under the top bar and opens a collapsed group first', async () => {
  const h = await harness().ready();
  await openInsights(h);
  // the page is scrolled and the sticky top bar is 63px tall; the card sits 400px into the viewport
  // (pinned by selector: reopening the group re-renders the page and replaces the card node)
  h.window.scrollY = 5000;
  const topbar = h.document.querySelector('.topbar');
  topbar.getBoundingClientRect = () => ({ left: 0, top: 0, width: 1000, height: 63 });
  const card = h.node('card-D4');
  card.getBoundingClientRect = () => ({ left: 0, top: 400, width: 1000, height: 300 });
  const query = h.document.querySelector;
  h.document.querySelector = selector => selector === '.topbar' ? topbar : selector === '#card-D4' ? card : query(selector);
  const scrolls = [];
  h.window.scrollTo = options => scrolls.push(options);
  assert.match(h.node('main').innerHTML, /data-action="ins-top" data-rule="D4" data-ins-group="sub_agents"/);
  h.action('ins-group', { insGroup: 'sub_agents' });
  assert.equal(h.run("state.insights.openGroups.has('sub_agents')"), false, 'the group collapses on its header');
  h.action('ins-top', { rule: 'D4', insGroup: 'sub_agents' });
  assert.equal(h.run("state.insights.openGroups.has('sub_agents')"), true, 'the strip reopens the group its card sits in');
  // 5000 + 400 - 63 - 12: the card's title lands below the bar, not behind it
  assert.equal(scrolls.length, 1);
  assert.equal(scrolls[0].top, 5325);
  assert.equal(scrolls[0].behavior, 'smooth');
  assert.equal(h.errors.length, 0);
});

test('the push card counts pushes without a passing test and the D7 card names every recovery path', async () => {
  const h = await harness().ready();
  const report = insightsReport();
  report.top_checks = ['D25'];
  report.groups.push({ id: 'verification', time_ms: 0, tokens: { input: 0, cached: 0, output: 0 }, order_time: 2, order_tokens: 2, cards: [{
    rule: 'D25', group: 'verification', title: 'D25', class: 'check', exposure: { time_ms: 0, count: 4, tokens: null }, sessions: 3, of: 9, no_data: 1, no_data_items: 2, not_applicable: 4, reason: 'an unknown command or a telemetry gap between the last edit and the push',
    stats: { pushes: 12, pushes_unverified: 4, pushes_after_failed_test: 1, sessions_with_edits_after_last_push: 2, edits_after_last_push: 7 },
    distribution: [{ label: 'no test ran between the last edit and the push', n: 3, time_ms: 0, sessions: 2 }, { label: 'tests ran between, none passed', n: 1, time_ms: 0, sessions: 1 }],
    evidence: [{ session: 'S1', title: 'First session', lane: '/root', lane_id: 'L1', a: 2000, b: 62000, time_ms: 0, note: 'no test ran between the last edit and the push' }],
  }] });
  report.groups.push({ id: 'failures_and_retries', time_ms: 60e3, tokens: { input: 0, cached: 0, output: 0 }, order_time: 3, order_tokens: 3, cards: [{
    rule: 'D7', group: 'failures_and_retries', title: 'D7', exposure: { time_ms: 60e3, main_ms: 60e3, count: 2, tokens: null }, sessions: 2, of: 9, no_data: 0,
    stats: { groups: 2, attempts: 6, windows: 4, windows_blind: 2, windows_blind_passed: 1, windows_fix: 1, windows_other: 1 },
    distribution: [{ label: 'test go test', n: 2, time_ms: 60e3, sessions: 2 }], evidence: [],
  }] });
  await openInsights(h, report);
  const html = h.node('main').innerHTML;
  assert.match(html, /In 3 of 9 sessions with a push after a change, a push had no passing test since the last edit\. 4 of 12 pushes had no passing test between the last edit and the push\. 1 push came after a failed test\. 2 sessions ended with edits after the last push \(7 edits in all\)\. No data in 1 session \(an unknown command or a telemetry gap between the last edit and the push\)\. 4 sessions pushed nothing after a change\./);
  assert.match(html, /data-rule="D25"[\s\S]*?3 of 9 sessions/, 'the check reaches the top findings');
  // a check row counts sessions, not findings: three pushes in two sessions read "2 of 9"
  assert.match(html, /<span class="label" title="no test ran between the last edit and the push">[^<]*<\/span>[\s\S]*?<span class="n">3<\/span><span class="val">2 of 9/);
  assert.match(html, /2 pushes were not measurable: an unknown command or a telemetry gap between the last edit and the push\./, 'the unmeasurable pushes are worded by the rule, not as usage records');
  assert.match(html, /2 of 4 retries ran again with only reads recorded between the failure and the retry\. 1 of those was a test that passed on the retry\. Between the failure and the retry: 1 after a fix, 1 after another step\./);
  assert.equal(h.errors.length, 0);
});

test('the model-time card names lifecycle stages by their readable label, not the raw key', async () => {
  const h = await harness().ready();
  const report = insightsReport();
  // an M1 card carrying model-output time split across stages, incl. the text-only-turn `llm` key
  report.groups.push({ id: 'models_and_effort', time_ms: 90e3, tokens: { input: 0, cached: 0, output: 0 }, order_time: 2, order_tokens: 2, cards: [{
    rule: 'M1', group: 'models_and_effort', title: 'M1', class: 'info', exposure: { time_ms: 90e3, main_ms: 90e3, count: 3, tokens: { input: 0, cached: 0, output: 0 } }, sessions: 1, of: 3, no_data: 0,
    distribution: [], evidence: [],
    stats: { stage_implement: 60e3, stage_review: 20e3, stage_llm: 10e3 },
  }] });
  await openInsights(h, report);
  const html = h.node('main').innerHTML;
  assert.match(html, /By stage: Implement 1m, Review \d+s, Model \d+s\./, 'stages read as their short labels');
  assert.doesNotMatch(html, /By stage:[^.]*implement/, 'the raw stage key never reaches the reader');
  assert.doesNotMatch(html, /stage_implement/);
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

test('an evidence row opens the session and focuses the interval once the insights card above the timeline is in', async () => {
  const h = await harness().ready();
  await openInsights(h);
  const scrolls = [];
  h.window.scrollTo = options => scrolls.push(options);
  h.action('ins-evidence', { id: 'S1', a: '2000', b: '3000' });
  h.take('/api/sessions/S1').resolve(session('S1')); await flush();
  assert.equal(h.run('state.page'), 'session');
  assert.equal(h.run('state.id'), 'S1');
  // the session's insights card is still loading: it will push the timeline down by its height,
  // so the focus (and its scroll to the timeline) waits for it
  assert.equal(h.run('state.focus'), null);
  assert.equal(scrolls.length, 0);
  h.take('/api/insights/report?period=session&session=S1').resolve(insightsReport()); await flush();
  assert.equal(h.run('JSON.stringify(state.focus)'), '{"a":2000,"b":3000}');
  assert.equal(h.run('state.pendingFocus'), null);
  assert.equal(scrolls.length, 1, 'one scroll, after the layout above the timeline settled');
  assert.equal(scrolls[0].behavior, 'smooth');
});

test('a focus parameter in the session hash highlights the interval after the load, even when the insights card fails', async () => {
  const h = await harness().ready();
  h.location.hash = '#session/S9?focus=5000-6000';
  h.run('route()');
  h.take('/api/sessions/S9').resolve(session('S9')); await flush();
  assert.equal(h.run('state.id'), 'S9');
  h.take('/api/insights/report?period=session&session=S9').resolve({ error: 'synthetic' }, 500); await flush();
  assert.equal(h.run('JSON.stringify(state.focus)'), '{"a":5000,"b":6000}');
  assert.equal(h.errors.length, 0);
});

// A filter as an address: filterToLink writes one base64url word, filterFromLink reads it back
// field by field; what it does not know it drops, what is not a filter is null.
test('a filter round-trips through its link word; unknown fields are dropped and garbage is null', async () => {
  const h = await harness().ready();
  const f = { kind: 'custom', from: '2026-09-01', to: '2026-09-19', cwd: '/home/dev/проект', host: 'web-01', sources: { claude: false } };
  const word = h.run(`filterToLink(${JSON.stringify(f)})`);
  assert.match(word, /^[A-Za-z0-9_-]+$/, 'base64url: safe in a hash, no padding');
  const read = w => JSON.parse(h.run(`JSON.stringify(filterFromLink(${JSON.stringify(w)}))`));
  assert.deepEqual(read(word), f);
  // the defaults are left out of the word, and read back as the defaults
  const plain = h.run("filterToLink({ kind: '30d', from: '', to: '', cwd: '', host: '', sources: { codex: true } })");
  assert.deepEqual(read(plain), { kind: '30d', from: '', to: '', cwd: '', host: '', sources: {} });
  assert.ok(plain.length < word.length);
  // a session travels with the word for the page that accepts that period
  const withSession = h.run("filterToLink({ kind: 'session', session: 'S1', from: '', to: '', cwd: '', host: '', sources: {} })");
  assert.equal(h.run(`filterFromLink(${JSON.stringify(withSession)}, [...FILTER_KINDS, 'session'])`).session, 'S1');
  assert.equal(h.run(`filterFromLink(${JSON.stringify(withSession)})`), null, 'a period the page does not accept is not a filter for it');
  // a hand-written word: an unknown kind, a malformed date, an unknown source and a number for a path are dropped or refused
  const hand = payload => h.run(`btoa(${JSON.stringify(JSON.stringify(payload))})`);
  assert.equal(h.run(`filterFromLink(${JSON.stringify(hand({ kind: 'yesterday' }))})`), null);
  assert.deepEqual(read(hand({ kind: 'custom', from: '1 Sep', to: '2026-09-19', cwd: 42, off: ['gemini', 'codex'] })), { kind: 'custom', from: '', to: '2026-09-19', cwd: '', host: '', sources: { codex: false } });
  assert.equal(h.run("filterFromLink('not base64!')"), null);
  assert.equal(h.run("filterFromLink('')"), null);
  assert.equal(h.run(`filterFromLink(${JSON.stringify(h.run("btoa('[1,2]')"))})`), null, 'JSON that is not an object');
  assert.equal(h.errors.length, 0);
});

// The report has an address: #session/<id>/insights opens one session's report, #insights?f=<word>
// opens a set of filters, and the address bar follows every change of the filters (replaced, not
// pushed), so whatever is on screen can be copied as a link.
test('#session/<id>/insights opens the session report; the address follows the filters and a filter link opens them', async () => {
  const h = await harness().ready();
  const sessions = [{ id: 'S1', title: 'First', cwd: '/proj', updated: 5000, started: 1 }, { id: 'S2', title: 'Second', cwd: '/other', updated: 4000, started: 1 }];
  // the session form, typed into the address bar
  h.location.hash = '#session/S1/insights';
  h.run('route()');
  h.take('/api/sessions').resolve(sessions); await flush();
  h.take('/api/insights/report?period=session&session=S1').resolve(insightsReport()); await flush();
  assert.equal(h.run('state.page'), 'insights');
  assert.equal(h.run('state.insights.params.kind'), 'session');
  assert.equal(h.location.hash, '#session/S1/insights', 'the address stays the session form');
  assert.match(h.node('main').innerHTML, /id="insSession"[^>]*>(?:(?!<\/select>).)*value="S1" selected/, 'the picker shows the session');
  // picking another session in the picker moves the address to that session
  h.document.emit('change', { target: { id: 'insSession', value: 'S2' } });
  h.take('/api/sessions').resolve(sessions); await flush();
  h.take('/api/insights/report?period=session&session=S2').resolve(insightsReport()); await flush();
  assert.equal(h.location.hash, '#session/S2/insights');
  // switching to a period writes the filter form: the word reads back as the filters in force
  // (the project resolves to the default, as it always did when none was chosen)
  h.document.emit('change', { target: { id: 'insPeriod', value: '7d' } });
  h.take('/api/sessions').resolve(sessions); await flush();
  const first = h.requests.find(r => !r.done && r.url.startsWith('/api/insights/report?period=7d'));
  assert.ok(first, 'a 7-day report is requested');
  first.resolve(insightsReport()); await flush();
  assert.match(h.location.hash, /^#insights\?f=[A-Za-z0-9_-]+$/);
  const inForce = JSON.parse(h.run(`JSON.stringify(filterFromLink(${JSON.stringify(h.location.hash.slice('#insights?f='.length))}))`));
  assert.deepEqual(inForce, { kind: '7d', from: '', to: '', cwd: h.run('state.insights.params.cwd'), host: '', sources: {} });
  h.document.emit('change', { target: { id: 'insProject', value: '/other' } });
  h.take('/api/sessions').resolve(sessions); await flush();
  h.take('/api/insights/report?period=7d&cwd=%2Fother').resolve(insightsReport()); await flush();
  const link = h.location.hash;
  assert.equal(h.run(`filterFromLink(${JSON.stringify(link.slice('#insights?f='.length))}).cwd`), '/other');
  // a filter link opens those filters: from the list page, and while the page is already shown
  h.run("go('sessions')");
  h.take('/api/sessions').resolve(sessions); await flush();
  assert.equal(h.run('state.page'), 'sessions');
  const other = h.run("filterToLink({ kind: '90d', from: '', to: '', cwd: '/proj', host: '', sources: { claude: false } })");
  h.location.hash = '#insights?f=' + other;
  h.run('route()');
  h.take('/api/sessions').resolve(sessions); await flush();
  h.take('/api/insights/report?period=90d&cwd=%2Fproj&sources=codex').resolve(insightsReport()); await flush();
  assert.equal(h.run('state.page'), 'insights');
  assert.equal(h.location.hash, '#insights?f=' + other, 'the address is the link as given');
  h.location.hash = link;
  h.run('route()');
  h.take('/api/sessions').resolve(sessions); await flush();
  h.take('/api/insights/report?period=7d&cwd=%2Fother').resolve(insightsReport()); await flush();
  assert.equal(h.run('state.insights.params.cwd'), '/other');
  assert.equal(h.location.hash, link);
  // an unreadable word: the page opens as it stands and says so
  h.location.hash = '#insights?f=***';
  h.run('route()');
  await flush();
  assert.equal(h.run('state.insights.params.cwd'), '/other', 'nothing applied');
  assert.match(h.node('toast').textContent, /could not read/);
  assert.equal(h.location.hash, link, 'the address names the report shown, not the bad word');
  // a plain #insights while on the page changes nothing and asks for nothing
  h.location.hash = '#insights';
  h.run('route()');
  await flush();
  assert.ok(!h.requests.some(r => !r.done), 'no request pending');
  assert.equal(h.errors.length, 0);
});

test('the session page links to the session report by its address', async () => {
  const h = await harness().ready();
  await h.open('S1@web-01', session('S1@web-01'));
  h.take('/api/insights/report?period=session&session=S1%40web-01').resolve(insightsReport()); await flush();
  assert.match(h.node('sessionInsights').innerHTML, /<a class="text-btn" href="#session\/S1%40web-01\/insights">Open the full report for this session<\/a>/);
  assert.equal(h.errors.length, 0);
});

test('changing the period or the project requests a new report', async () => {
  const h = await harness().ready();
  await openInsights(h);
  h.document.emit('change', { target: { id: 'insPeriod', value: '7d' } });
  h.take('/api/sessions').resolve([{ id: 'S1', title: 'One', cwd: '/proj', updated: Date.now(), started: 1 }]); await flush();
  h.take('/api/insights/report?period=7d&cwd=%2Fproj').resolve(insightsReport()); await flush();
  assert.equal(h.run('state.insights.params.kind'), '7d', 'the filter keeps the choice');
  assert.match(h.node('main').innerHTML, /id="insPeriod"[^>]*>(?:(?!<\/select>).)*value="7d" selected/);
  assert.match(h.node('main').innerHTML, /Report for <b>[^<]*<\/b>/, 'the chip states the period the report was built for');
  h.document.emit('change', { target: { id: 'insProject', value: '' } });
  h.take('/api/sessions').resolve([]); await flush();
  h.take('/api/insights/report?period=7d').resolve(insightsReport()); await flush();
  h.document.emit('change', { target: { id: 'insPeriod', value: 'custom' } });
  h.take('/api/sessions').resolve([]); await flush();
  const url = h.requests.find(r => !r.done && r.url.startsWith('/api/insights/report?period=custom')).url;
  assert.match(url, /period=custom&from=\d+&to=\d+$/);
  h.take(url).resolve(insightsReport()); await flush();
  // custom dates: the fields are shown, a typed date waits for Apply, Apply requests the report
  assert.match(h.node('main').innerHTML, /id="insFrom"[^>]*value="\d{4}-\d{2}-\d{2}"/);
  h.document.emit('change', { target: { id: 'insFrom', value: '2026-09-01' } });
  assert.equal(h.requests.filter(r => !r.done).length, 0, 'a date alone requests nothing');
  h.document.emit('change', { target: { id: 'insTo', value: '2026-09-07' } });
  h.action('filter-apply', { prefix: 'ins' });
  h.take('/api/sessions').resolve([]); await flush();
  const applied = h.requests.find(r => !r.done && r.url.startsWith('/api/insights/report?period=custom')).url;
  assert.equal(applied, `/api/insights/report?period=custom&from=${new Date(2026, 8, 1).getTime()}&to=${new Date(2026, 8, 8).getTime() - 1}`);
  // the status is its own row under the controls, never between them
  assert.match(h.node('main').innerHTML, /<div class="bar-actions">(?:(?!<\/section>).)*<\/div><div class="report-status">/);
  assert.equal(h.errors.length, 0);
});

test('the sessions list defaults to 30 days, remembers the whole bar in a cookie, restores it, drops a vanished project and clears to the defaults', async () => {
  const h = await harness().ready();
  const now = Date.now(), day = 24 * 3600e3;
  const list = [
    { id: 'A', source: 'codex', title: 'A', cwd: '/proj/a', updated: now - day, started: now - day, bytes: 1, agents: 0 },
    { id: 'B@web-01', host: 'web-01', source: 'claude', title: 'B', cwd: '/proj/b', updated: now - day, started: now - day, bytes: 1, agents: 0 },
  ];
  h.run("go('sessions')");
  h.take('/api/sessions').resolve(list);
  await flush();
  // default: 30 days, every project, host and source; Clear has nothing to do
  assert.equal(h.run('state.fleetFilter.kind'), '30d');
  assert.match(h.node('main').innerHTML, /data-action="fleet-clear"[^>]*disabled/);
  // every choice is written to the cookie the moment it changes
  h.document.emit('change', { target: { id: 'fleetPeriod', value: 'all' } });
  h.document.emit('change', { target: { id: 'fleetProject', value: '/proj/b' } });
  h.document.emit('change', { target: { id: 'fleetHost', value: 'web-01' } });
  h.document.emit('change', { target: { id: 'fleetSrcCodex', type: 'checkbox', checked: false, value: 'on' } });
  h.document.emit('input', { target: { id: 'sessionSearch', value: 'needle' } });
  h.document.emit('change', { target: { id: 'fleetSort', value: 'size' } });
  const saved = JSON.parse(decodeURIComponent(String(h.document.cookie).match(/todobem_fleet=([^;]*)/)[1]));
  assert.deepEqual(saved, { kind: 'all', from: '', to: '', cwd: '/proj/b', host: 'web-01', sources: { codex: false }, search: 'needle', sort: 'size' });
  assert.doesNotMatch(h.node('main').innerHTML, /data-action="fleet-clear"[^>]*disabled/);
  // a fresh session (state reset) restores everything from the cookie
  h.run("state.fleetFilter = { kind: '30d', from: '', to: '', cwd: '', host: '', sources: {} }; state.search = ''; state.fleetSort = 'updated'");
  h.run('restoreFleetFilter()');
  assert.equal(h.run("JSON.stringify(fleetPrefs())"), JSON.stringify(saved));
  // the remembered project and host vanish from the list: back to All, the rest kept
  h.run("go('sessions')");
  h.take('/api/sessions').resolve([list[0]]);
  await flush();
  assert.equal(h.run('state.fleetFilter.cwd'), '');
  assert.equal(h.run('state.fleetFilter.host'), '');
  assert.equal(h.run('state.fleetFilter.kind'), 'all');
  assert.equal(h.run('state.search'), 'needle');
  // Clear: the defaults, saved
  h.action('fleet-clear');
  assert.equal(h.run("JSON.stringify(fleetPrefs())"), JSON.stringify({ kind: '30d', from: '', to: '', cwd: '', host: '', sources: {}, search: '', sort: 'updated' }));
  assert.match(decodeURIComponent(String(h.document.cookie)), /todobem_fleet=.*"kind":"30d"/);
  assert.match(h.node('main').innerHTML, /data-action="fleet-clear"[^>]*disabled/);
  // the Project control is the bar's primary one
  assert.match(h.node('main').innerHTML, /<label class="ctl primary">Project<select class="select" id="fleetProject"/);
  assert.equal(h.errors.length, 0);
});

// The list has an address too: #sessions while everything is the default, #sessions?f=<word>
// once the filter, the search or the sort is off it — the address bar follows every change
// (replaced, never pushed), Clear puts #sessions back, and a filter link opens that view over
// the defaults, from a fresh load and from the list itself.
test('#sessions?f=<word> names the list as filtered and opens it', async () => {
  const list = [
    { id: 'A', title: 'Alpha', cwd: '/proj/a', updated: Date.now(), started: 1, bytes: 10, agents: 0, source: 'codex' },
    { id: 'B', title: 'Beta', cwd: '/proj/b', updated: Date.now(), started: 1, bytes: 10, agents: 0, source: 'claude', host: 'web-01' },
  ];
  let h = await harness().ready();
  h.run("go('sessions')");
  h.take('/api/sessions').resolve(list); await flush();
  assert.equal(h.location.hash, '#sessions', 'the defaults have the plain address');
  h.document.emit('change', { target: { id: 'fleetPeriod', value: 'all' } });
  h.document.emit('change', { target: { id: 'fleetProject', value: '/proj/b' } });
  h.document.emit('change', { target: { id: 'fleetSrcCodex', type: 'checkbox', checked: false, value: 'on' } });
  h.document.emit('input', { target: { id: 'sessionSearch', value: 'beta' } });
  h.document.emit('change', { target: { id: 'fleetSort', value: 'size' } });
  assert.match(h.location.hash, /^#sessions\?f=[A-Za-z0-9_-]+$/);
  const word = h.location.hash.slice('#sessions?f='.length);
  assert.deepEqual(JSON.parse(h.run(`JSON.stringify(filterFromLink(${JSON.stringify(word)}))`)), { kind: 'all', from: '', to: '', cwd: '/proj/b', host: '', sources: { codex: false }, search: 'beta', sort: 'size' });
  // the default sort is left out of the word
  h.document.emit('change', { target: { id: 'fleetSort', value: 'updated' } });
  assert.equal(JSON.parse(h.run(`JSON.stringify(filterFromLink(${JSON.stringify(h.location.hash.slice('#sessions?f='.length))}))`)).sort, undefined);
  h.action('fleet-clear');
  assert.equal(h.location.hash, '#sessions');
  // a fresh load on the address: the view it names, over the defaults, and the address kept
  h = harness({ hash: '#sessions?f=' + word });
  h.take('/api/auth').resolve({ enabled: false, authenticated: true }); await flush();
  h.take('/api/sessions').resolve(list); await flush();
  assert.ok(!h.requests.some(r => !r.done), 'the boot load is the list; the address needs no second one');
  assert.equal(h.run('state.page'), 'sessions');
  assert.equal(h.run('JSON.stringify(fleetPrefs())'), JSON.stringify({ kind: 'all', from: '', to: '', cwd: '/proj/b', host: '', sources: { codex: false }, search: 'beta', sort: 'size' }));
  assert.equal(h.location.hash, '#sessions?f=' + word);
  assert.match(h.node('main').innerHTML, /id="sessionSearch"[^>]*value="beta"/);
  assert.match(h.node('fleetRows').innerHTML, /data-id="B"/);
  assert.doesNotMatch(h.node('fleetRows').innerHTML, /data-id="A"/);
  // a link pasted while the list is shown: applied and drawn, no reload of the list
  const other = h.run("filterToLink({ kind: '7d', from: '', to: '', cwd: '/proj/a', host: '', sources: {} })");
  h.location.hash = '#sessions?f=' + other;
  h.run('route()');
  await flush();
  assert.equal(h.run('state.fleetFilter.cwd'), '/proj/a');
  assert.equal(h.run('state.search'), '', 'an address describes the whole view: the search it does not carry is off');
  assert.ok(!h.requests.some(r => !r.done), 'no request pending');
  assert.match(h.node('fleetRows').innerHTML, /data-id="A"/);
  // an unreadable word: the list as it stands, a note, and the address rewritten to what is shown
  h.location.hash = '#sessions?f=%%%';
  h.run('route()');
  await flush();
  assert.equal(h.run('state.fleetFilter.cwd'), '/proj/a');
  assert.match(h.node('toast').textContent, /could not read/);
  assert.equal(h.location.hash, '#sessions?f=' + other);
  assert.equal(h.errors.length, 0);
});

test('the session list filters by period and project with the same widget as the report', async () => {
  const h = await harness().ready();
  const now = Date.now(), day = 24 * 3600e3;
  const list = [
    { id: 'S-new', title: 'Fresh', cwd: '/proj/a', updated: now - day, started: now - 2 * day, bytes: 1e6, agents: 0 },
    { id: 'S-old', title: 'Old', cwd: '/proj/a', updated: now - 40 * day, started: now - 41 * day, bytes: 1e6, agents: 0 },
    { id: 'S-b', title: 'Other', cwd: '/proj/b', updated: now - 3 * day, started: now - 3 * day, bytes: 1e6, agents: 0 },
    { id: 'S-skew', title: 'Clock skew', cwd: '/proj/c', updated: now + day, started: now, bytes: 1e6, agents: 0 },
  ];
  h.run("go('sessions')");
  h.take('/api/sessions').resolve(list); await flush();
  let main = h.node('main').innerHTML;
  assert.match(main, /id="fleetPeriod"/);
  // the sessions list defaults to the last 30 days: the 40-day-old session is hidden
  assert.match(main, /<option value="30d" selected>/, 'period defaults to 30 days');
  assert.equal(h.node('fleetCount').textContent, '2 of 4 sessions', '30 days hides both the old row and a future timestamp');
  h.document.emit('change', { target: { id: 'fleetPeriod', value: 'all' } });
  main = h.node('main').innerHTML;
  assert.match(main, /id="fleetProject"[^>]*>(?:(?!<\/select>).)*a \(2\)(?:(?!<\/select>).)*b \(1\)/, 'all time: projects carry their full counts');
  assert.equal(h.node('fleetCount').textContent, '4 sessions', 'all time includes a remote clock that is ahead');
  h.document.emit('change', { target: { id: 'fleetPeriod', value: '30d' } });
  let rows = h.node('fleetRows').innerHTML;
  assert.match(rows, /data-id="S-new"/);
  assert.doesNotMatch(rows, /data-id="S-old"/);
  assert.equal(h.node('fleetCount').textContent, '2 of 4 sessions');
  h.document.emit('change', { target: { id: 'fleetProject', value: '/proj/b' } });
  rows = h.node('fleetRows').innerHTML;
  assert.doesNotMatch(rows, /data-id="S-new"/);
  assert.match(rows, /data-id="S-b"/);
  assert.match(h.node('fleetSummary').innerHTML, /<strong class="num">1<\/strong><span>Sessions in view/);
  // custom dates around the old session only
  h.document.emit('change', { target: { id: 'fleetProject', value: '' } });
  h.document.emit('change', { target: { id: 'fleetPeriod', value: 'custom' } });
  assert.match(h.node('main').innerHTML, /id="fleetFrom"/);
  h.document.emit('change', { target: { id: 'fleetFrom', value: h.run(`dateInputValue(${now - 45 * day})`) } });
  h.document.emit('change', { target: { id: 'fleetTo', value: h.run(`dateInputValue(${now - 35 * day})`) } });
  h.action('filter-apply', { prefix: 'fleet' });
  rows = h.node('fleetRows').innerHTML;
  assert.match(rows, /data-id="S-old"/);
  assert.doesNotMatch(rows, /data-id="S-new"/);
  // the search still narrows within the period
  h.document.emit('change', { target: { id: 'fleetPeriod', value: 'all' } });
  h.document.emit('input', { target: { id: 'sessionSearch', value: 'other' } });
  rows = h.node('fleetRows').innerHTML;
  assert.match(rows, /data-id="S-b"/);
  assert.doesNotMatch(rows, /data-id="S-new"/);
  assert.equal(h.errors.length, 0);
});

test('sessions of both sources share one list: a colour mark and the harness name per row, source toggles with counts that narrow the list and the project counts', async () => {
  const h = await harness().ready();
  const now = Date.now(), day = 24 * 3600e3;
  const list = [
    { id: 'C-1', source: 'codex', title: 'Codex one', cwd: '/proj/a', updated: now - day, started: now - 2 * day, bytes: 1e6, agents: 0, cli: '0.154.0', model: 'gpt-6' },
    { id: 'A-1', source: 'claude', title: 'Claude one', cwd: '/proj/a', updated: now - 2 * day, started: now - 2 * day, bytes: 1e6, agents: 1, cli: '2.1.270', model: 'claude-synthetic-1' },
    { id: 'A-2', source: 'claude', title: 'Claude two', cwd: '/proj/b', updated: now - 3 * day, started: now - 3 * day, bytes: 1e6, agents: 0 },
  ];
  h.run("go('sessions')");
  h.take('/api/sessions').resolve(list); await flush();
  const main = h.node('main').innerHTML;
  // the toggles: one chip per source with its mark and its count in the period, both on
  assert.match(main, /<div class="ctl source-toggles" role="group" aria-label="Sources"><label class="chip toggle source-toggle on"><input type="checkbox" id="fleetSrcCodex" checked aria-label="Codex"><i class="source-mark source-codex"><\/i>Codex<span class="count">1<\/span><\/label><label class="chip toggle source-toggle on"><input type="checkbox" id="fleetSrcClaude" checked aria-label="Claude Code"><i class="source-mark source-claude"><\/i>Claude Code<span class="count">2<\/span><\/label><\/div>/);
  // the rows: the mark before the title, the harness named with its version in the meta line
  let rows = h.node('fleetRows').innerHTML;
  assert.match(rows, /data-id="C-1"><strong><i class="source-mark source-codex" title="Codex" aria-label="Codex"><\/i>Codex one<\/strong><span>…\/proj\/a · gpt-6 · Codex 0\.154\.0<\/span>/);
  assert.match(rows, /data-id="A-1"><strong><i class="source-mark source-claude" title="Claude Code" aria-label="Claude Code"><\/i>Claude one<\/strong><span>…\/proj\/a · claude-synthetic-1 · Claude Code 2\.1\.270<\/span>/);
  assert.match(rows, /data-id="A-2"><strong>[^<]*<i class="source-mark source-claude"[^>]*><\/i>Claude two<\/strong><span>…\/proj\/b · Claude Code<\/span>/, 'no version known: the harness name alone');
  // switching Claude Code off hides its rows, its count stays on the chip, the project counts follow
  h.document.emit('change', { target: { id: 'fleetSrcClaude', type: 'checkbox', checked: false, value: 'on' } });
  rows = h.node('fleetRows').innerHTML;
  assert.match(rows, /data-id="C-1"/);
  assert.doesNotMatch(rows, /data-id="A-1"|data-id="A-2"/);
  assert.equal(h.node('fleetCount').textContent, '1 of 3 sessions');
  assert.match(h.node('main').innerHTML, /<label class="chip toggle source-toggle "><input type="checkbox" id="fleetSrcClaude"  aria-label="Claude Code"><i class="source-mark source-claude"><\/i>Claude Code<span class="count">2<\/span>/);
  assert.match(h.node('main').innerHTML, /id="fleetProject"[^>]*>(?:(?!<\/select>).)*a \(1\)(?:(?!<\/select>).)*<\/select>/);
  assert.doesNotMatch(h.node('main').innerHTML, /b \(1\)/, 'a project with only hidden sessions leaves the select');
  // both off: an honest empty list that names the source among the things to change
  h.document.emit('change', { target: { id: 'fleetSrcCodex', type: 'checkbox', checked: false, value: 'on' } });
  assert.match(h.node('fleetRows').innerHTML, /No sessions match\.<\/h3><p>Try another search, period, project or source\./);
  h.document.emit('change', { target: { id: 'fleetSrcCodex', type: 'checkbox', checked: true, value: 'on' } });
  h.document.emit('change', { target: { id: 'fleetSrcClaude', type: 'checkbox', checked: true, value: 'on' } });
  assert.equal(h.node('fleetCount').textContent, '3 sessions');
  // the search finds a session by its source's name
  h.document.emit('input', { target: { id: 'sessionSearch', value: 'claude code' } });
  assert.equal(h.node('fleetCount').textContent, '2 of 3 sessions');
  h.document.emit('input', { target: { id: 'sessionSearch', value: '' } });
  // one source alone needs no toggle
  h.run("go('sessions')");
  h.take('/api/sessions').resolve(list.filter(s => s.source === 'claude')); await flush();
  assert.doesNotMatch(h.node('main').innerHTML, /source-toggles/);
  assert.match(h.node('fleetRows').innerHTML, /source-mark source-claude/, 'the mark stays');
  assert.equal(h.errors.length, 0);
});

test('a source switched off narrows the Insights report through the sources parameter; all on is the default', async () => {
  const h = await harness().ready();
  await openInsights(h);
  const now = Date.now();
  const both = [{ id: 'S1', source: 'codex', title: 'One', cwd: '/proj', updated: now, started: 1 }, { id: 'S2', source: 'claude', title: 'Two', cwd: '/proj', updated: now, started: 1 }];
  h.document.emit('change', { target: { id: 'insPeriod', value: '7d' } });
  h.take('/api/sessions').resolve(both); await flush();
  h.take('/api/insights/report?period=7d&cwd=%2Fproj').resolve(insightsReport()); await flush();
  assert.match(h.node('main').innerHTML, /id="insSrcCodex" checked[\s\S]*id="insSrcClaude" checked/);
  h.document.emit('change', { target: { id: 'insSrcCodex', type: 'checkbox', checked: false, value: 'on' } });
  h.take('/api/sessions').resolve(both); await flush();
  h.take('/api/insights/report?period=7d&cwd=%2Fproj&sources=claude').resolve(insightsReport()); await flush();
  assert.match(h.node('main').innerHTML, /id="insSrcCodex"  aria-label="Codex"/);
  h.document.emit('change', { target: { id: 'insSrcCodex', type: 'checkbox', checked: true, value: 'on' } });
  h.take('/api/sessions').resolve(both); await flush();
  h.take('/api/insights/report?period=7d&cwd=%2Fproj').resolve(insightsReport()); await flush();
  assert.equal(h.errors.length, 0);
});

test('"All projects" stays chosen: the default project fills in only until the reader picks one', async () => {
  const h = await harness().ready();
  const list = [{ id: 'S1', title: 'One', cwd: '/proj', updated: Date.now(), started: 1 }, { id: 'S2', title: 'Two', cwd: '/proj', updated: Date.now(), started: 1 }];
  h.run("go('insights')");
  h.take('/api/sessions').resolve(list); await flush();
  // first load: the busiest project is the default
  h.take('/api/insights/report?period=30d&cwd=%2Fproj').resolve(insightsReport()); await flush();
  h.document.emit('change', { target: { id: 'insProject', value: '' } });
  h.take('/api/sessions').resolve(list); await flush();
  h.take('/api/insights/report?period=30d').resolve(insightsReport()); await flush();
  assert.equal(h.run('state.insights.params.cwd'), '');
  assert.match(h.node('main').innerHTML, /<option value="" selected>All projects<\/option>/);
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

/* ---------- settings page and the rollout path ---------- */
// settingsPayload is /api/settings: one section per source; `homes` overrides the Codex list,
// `claude` the Claude Code list.
function settingsPayload({ homes, claude, ...overrides } = {}) {
  const codexHomes = homes || [
    { path: '~/.codex', resolved: '/home/synthetic/.codex', status: 'ok', sessions: 12, elsewhere: 0 },
    { path: '/Volumes/work/codex', resolved: '/Volumes/work/codex', status: 'missing', sessions: 0, elsewhere: 0 },
  ];
  const claudeHomes = claude || [{ path: '~/.claude', resolved: '/home/synthetic/.claude', status: 'ok', sessions: 4, elsewhere: 0 }];
  return {
    path: '/home/synthetic/.todobem/settings.json', exists: true, pinned: false,
    sources: [{ name: 'codex', homes: codexHomes }, { name: 'claude', homes: claudeHomes }],
    ...overrides,
  };
}
async function openSettings(h, payload = settingsPayload()) {
  h.run("go('settings')");
  h.take('/api/settings').resolve(payload); await flush();
  assert.equal(h.run('state.page'), 'settings');
}

test('the settings page lists every folder with what the server found there', async () => {
  const h = await harness().ready();
  await openSettings(h);
  const main = h.node('main').innerHTML;
  assert.match(main, /<h1>Settings<\/h1>/);
  // one section per source, in source order, each with its mark
  assert.match(main, /<h2 id="set-codex"><i class="source-mark source-codex"[^>]*><\/i>Codex session folders<\/h2>[\s\S]*<h2 id="set-claude"><i class="source-mark source-claude"[^>]*><\/i>Claude Code session folders<\/h2>/);
  assert.match(main, /data-source="codex" data-home-index="0" value="~\/\.codex"/);
  assert.match(main, /\/home\/synthetic\/\.codex<\/span> · 12 sessions/);
  assert.match(main, /data-source="codex" data-home-index="1" value="\/Volumes\/work\/codex"/);
  assert.match(main, /home-status warn.*folder not found/);
  assert.match(main, /data-source="claude" data-home-index="0" value="~\/\.claude"/);
  assert.match(main, /\/home\/synthetic\/\.claude<\/span> · 4 sessions/);
  // a folder whose rollouts are also in an earlier folder says where they are shown from; a
  // source without a folder says it is off
  await openSettings(h, settingsPayload({ homes: [settingsPayload().sources[0].homes[0], { path: '/Volumes/laptop/codex', resolved: '/Volumes/laptop/codex', status: 'ok', sessions: 3, elsewhere: 9 }], claude: [] }));
  assert.match(h.node('main').innerHTML, /3 sessions · 9 more also in an earlier folder, shown from there/);
  assert.match(h.node('main').innerHTML, /No Claude Code folder: Claude Code sessions are not read\./);
  assert.match(main, /Saved to \/home\/synthetic\/\.todobem\/settings\.json\. Without the file todobem reads ~\/\.codex and ~\/\.claude\./);
  assert.match(main, /id="setSave"[^>]*disabled/, 'nothing to save yet');
  assert.match(h.node('breadcrumb').innerHTML, /Settings/);
  assert.equal(h.errors.length, 0);
});

test('editing a folder enables Save without re-rendering; add, remove and save post the draft as JSON and apply the answer', async () => {
  const h = await harness().ready();
  await openSettings(h);
  const before = h.node('main').innerHTML;
  h.document.emit('input', { target: { id: '', dataset: { source: 'codex', homeIndex: '1' }, value: '/Volumes/work/codex-2' } });
  assert.equal(h.node('main').innerHTML, before, 'typing must not re-render the rows');
  assert.equal(h.node('setSave').disabled, false, 'a changed draft enables Save');
  assert.equal(h.run('JSON.stringify(state.settings.draft)'), JSON.stringify({ codex: ['~/.codex', '/Volumes/work/codex-2'], claude: ['~/.claude'] }));
  h.action('set-add', { source: 'codex' });
  assert.match(h.node('main').innerHTML, /data-source="codex" data-home-index="2" value=""/);
  h.run("state.settings.draft.codex[1] = '/Volumes/work/codex'");
  assert.equal(h.run('settingsDirty(state.settings)'), false, 'an empty added row alone is not a change');
  h.run("state.settings.draft.codex[1] = '/Volumes/work/codex-2'");
  assert.match(h.node('main').innerHTML, /data-source="codex" data-home-index="2"[^>]*>[^<]*<button[^>]*data-action="set-remove" data-source="codex" data-index="2"/);
  h.action('set-remove', { source: 'codex', index: '0' });
  // the Claude folder can go too: the Codex list still holds a folder
  h.action('set-remove', { source: 'claude', index: '0' });
  assert.equal(h.run('JSON.stringify(state.settings.draft)'), JSON.stringify({ codex: ['/Volumes/work/codex-2', ''], claude: [] }));
  h.document.emit('submit', { target: { id: 'settingsForm' }, preventDefault() {} });
  const req = h.take('/api/settings');
  assert.equal(req.options.method, 'POST');
  assert.equal(req.options.headers['Content-Type'], 'application/json');
  assert.deepEqual(JSON.parse(req.options.body), { codex_homes: ['/Volumes/work/codex-2', ''], claude_homes: [] });
  assert.match(h.node('main').innerHTML, /Saving…/);
  req.resolve(settingsPayload({ homes: [{ path: '/Volumes/work/codex-2', resolved: '/Volumes/work/codex-2', status: 'no_sessions_dir', sessions: 0 }], claude: [] })); await flush();
  // the answer replaces the draft (the blank entry is gone) and the session list is reloaded
  assert.equal(h.run('JSON.stringify(state.settings.draft)'), JSON.stringify({ codex: ['/Volumes/work/codex-2'], claude: [] }));
  assert.match(h.node('main').innerHTML, /no sessions\/ folder here/);
  assert.match(h.node('main').innerHTML, /data-action="set-remove" data-source="codex" data-index="0" [^>]*disabled/, 'the last folder overall cannot be removed');
  assert.match(h.node('toast').textContent, /Settings saved · 1 folder/);
  h.take('/api/sessions').resolve([]); await flush();
  assert.equal(h.errors.length, 0);
});

test('a refused save keeps the draft and shows the server\'s reason', async () => {
  const h = await harness().ready();
  await openSettings(h);
  h.document.emit('input', { target: { id: '', dataset: { source: 'codex', homeIndex: '0' }, value: 'relative/codex' } });
  h.action('set-save');
  h.take('/api/settings').resolve('"relative/codex": use an absolute path or one starting with ~/', 400); await flush();
  assert.equal(h.run('JSON.stringify(state.settings.draft.codex)'), JSON.stringify(['relative/codex', '/Volumes/work/codex']));
  assert.match(h.node('main').innerHTML, /Could not save: .*use an absolute path/);
  assert.equal(h.node('setSave').disabled, false);
});

test('a failed settings request clears Saving, and a GET started before the save cannot overwrite its answer', async () => {
  const h = await harness().ready();
  await openSettings(h);
  h.document.emit('input', { target: { id: '', dataset: { source: 'codex', homeIndex: '0' }, value: '/srv/new' } });
  h.action('set-save');
  h.take('/api/settings').reject(new Error('connection lost'));
  await flush();
  assert.equal(h.run('state.settings.saving'), false);
  assert.match(h.node('main').innerHTML, /Could not save: connection lost/);
  assert.equal(h.node('setSave').disabled, false);

  const staleLoad = h.run('loadSettings()');
  const stale = h.take('/api/settings');
  h.document.emit('input', { target: { id: '', dataset: { source: 'codex', homeIndex: '0' }, value: '/srv/saved' } });
  h.action('set-save');
  const saved = h.requests.find(r => r.url === '/api/settings' && r.options.method === 'POST' && !r.done);
  assert.ok(saved);
  saved.resolve(settingsPayload({ homes: [{ path: '/srv/saved', resolved: '/srv/saved', status: 'ok', sessions: 1 }] }));
  await flush();
  h.take('/api/sessions').resolve([]);
  await flush();
  stale.resolve(settingsPayload({ homes: [{ path: '/srv/stale', resolved: '/srv/stale', status: 'ok', sessions: 1 }] }));
  await staleLoad;
  await flush();
  assert.equal(h.run('state.settings.draft.codex[0]'), '/srv/saved');
  assert.equal(h.run('state.settings.saving'), false);

  h.document.emit('input', { target: { id: '', dataset: { source: 'codex', homeIndex: '0' }, value: '/srv/again' } });
  h.action('set-save');
  const supersededSave = h.requests.find(r => r.url === '/api/settings' && r.options.method === 'POST' && !r.done);
  assert.ok(supersededSave);
  const newerLoad = h.run('loadSettings()');
  const newer = h.requests.find(r => r.url === '/api/settings' && (!r.options.method || r.options.method === 'GET') && !r.done);
  assert.ok(newer);
  newer.resolve(settingsPayload({ homes: [{ path: '/srv/reloaded', resolved: '/srv/reloaded', status: 'ok', sessions: 1 }] }));
  await newerLoad;
  await flush();
  supersededSave.resolve(settingsPayload({ homes: [{ path: '/srv/again', resolved: '/srv/again', status: 'ok', sessions: 1 }] }));
  await flush();
  assert.equal(h.run('state.settings.saving'), false, 'a superseded save still clears its busy state');
  assert.doesNotMatch(h.node('main').innerHTML, /Saving…/);
});

test('folders pinned by -codex / -claude are shown read-only', async () => {
  const h = await harness().ready();
  await openSettings(h, settingsPayload({ pinned: true, homes: [{ path: '/srv/rollouts', resolved: '/srv/rollouts', status: 'ok', sessions: 3 }], claude: [] }));
  const main = h.node('main').innerHTML;
  assert.match(main, /data-home-index="0" value="\/srv\/rollouts"[^>]*disabled/);
  assert.match(main, /Set by -codex \/ -claude on the command line/);
  assert.doesNotMatch(main, /id="setSave"/);
  assert.doesNotMatch(main, /data-action="set-add"/);
});

test('a #settings deep link opens the page on load and on a hash change', async () => {
  const h = harness({ hash: '#settings' });
  h.take('/api/auth').resolve({ enabled: false, authenticated: true }); await flush();
  h.take('/api/sessions').resolve([]); await flush();
  h.take('/api/settings').resolve(settingsPayload()); await flush();
  assert.equal(h.run('state.page'), 'settings');
  assert.match(h.node('main').innerHTML, /<h1>Settings<\/h1>/);
  h.run("go('sessions')");
  h.take('/api/sessions').resolve([]); await flush();
  h.location.hash = '#settings';
  h.window.emit('hashchange');
  h.take('/api/settings').resolve(settingsPayload()); await flush();
  assert.equal(h.run('state.page'), 'settings');
  assert.equal(h.errors.length, 0);
});

test('the session view names the log file of the main thread and of every agent', async () => {
  const h = await harness().ready();
  const model = agentsSession();
  model.lanes[0].file = '/home/synthetic/.codex/sessions/2026/09/12/rollout-2026-09-12T10-00-00-session.jsonl';
  model.lanes[1].file = '/home/synthetic/.codex/sessions/2026/09/12/rollout-2026-09-12T10-00-10-early.jsonl';
  await h.open('session', model);
  // the path is the request card's own last row (full width), not a cell of the summary list
  assert.match(h.node('main').innerHTML, /<\/div><dl class="session-summary rollout-path"><div><dt>Log file<\/dt><dd><span class="path" title="[^"]*rollout-2026-09-12T10-00-00-session\.jsonl">\/home\/synthetic\/\.codex\/sessions\/2026\/09\/12\/rollout-2026-09-12T10-00-00-session\.jsonl<\/span><\/dd><\/div><\/dl><\/section>/);
  assert.doesNotMatch(h.node('main').innerHTML, /<dt>Status<\/dt>(?:(?!<\/dl>).)*Log file/);
  h.run("inspectLane('early')");
  assert.match(h.node('inspector').innerHTML, /<dt>Log file<\/dt><dd class="mono">\/home\/synthetic\/\.codex\/sessions\/2026\/09\/12\/rollout-2026-09-12T10-00-10-early\.jsonl<\/dd>/);
  h.run("inspectLane('lane')");
  assert.match(h.node('inspector').innerHTML, /rollout-2026-09-12T10-00-00-session\.jsonl/);
});

test('the select list steps over disabled options, stops at the ends, jumps by letter and marks the opening value', async () => {
  const h = await harness().ready();
  h.run('var ddOptions = [{ index: 0, label: "Last 24 hours", disabled: false }, { index: 1, label: "Last 7 days", disabled: true }, { index: 2, label: "All time", disabled: false }, { index: 3, label: "Custom dates…", disabled: false }]');
  // arrows skip the disabled row and stay put at either end
  assert.equal(h.run('Dropdown.nextIndex(ddOptions, 0, 1)'), 2);
  assert.equal(h.run('Dropdown.nextIndex(ddOptions, 2, -1)'), 0);
  assert.equal(h.run('Dropdown.nextIndex(ddOptions, 3, 1)'), 3);
  assert.equal(h.run('Dropdown.nextIndex(ddOptions, 0, -1)'), 0);
  // Home and End are steps from outside the list
  assert.equal(h.run('Dropdown.nextIndex(ddOptions, -1, 1)'), 0);
  assert.equal(h.run('Dropdown.nextIndex(ddOptions, ddOptions.length, -1)'), 3);
  // type-ahead: the next match after the current row, wrapping, never a disabled one
  assert.equal(h.run('Dropdown.matchIndex(ddOptions, 0, "a")'), 2);
  assert.equal(h.run('Dropdown.matchIndex(ddOptions, 2, "L")'), 0);
  assert.equal(h.run('Dropdown.matchIndex(ddOptions, 0, "z")'), -1);
  // the row of the value the select opened with carries the check; the highlight is separate
  const html = h.run('Dropdown.render(ddOptions, 2, 3)');
  assert.match(html, /<div class="dropdown-option" role="option" data-index="2" aria-selected="true">All time<\/div>/);
  assert.match(html, /<div class="dropdown-option active" role="option" data-index="3" aria-selected="false">Custom dates…<\/div>/);
  assert.match(html, /data-index="1" aria-selected="false" aria-disabled="true">Last 7 days</);
  assert.equal(h.errors.length, 0);
});

/* ---------- markdown.js: the rendered view of a recorded message ---------- */

const md = (h, text) => h.run(`Markdown.render(${JSON.stringify(text)})`);

test('the renderer covers the GFM subset the agents write', async () => {
  const h = harness();
  assert.equal(md(h, '## Done\n\nSee **bold**, *em*, ~~old~~ and `code`.'), '<h2>Done</h2><p>See <strong>bold</strong>, <em>em</em>, <del>old</del> and <code>code</code>.</p>');
  // a newline inside a paragraph stays a newline (the block is pre-wrap), leading spaces stay
  assert.equal(md(h, 'line one\n  line two\n\npara two'), '<p>line one\n  line two</p><p>para two</p>');
  assert.equal(md(h, '- one\n- two\n  - nested\n- three'), '<ul><li>one</li><li>two<ul><li>nested</li></ul></li><li>three</li></ul>');
  assert.equal(md(h, '3. c\n4. d'), '<ol start="3"><li>c</li><li>d</li></ol>');
  // a blank line between items makes the list loose: the items' paragraphs are wrapped
  assert.equal(md(h, '1. first\n\n2. second\n   wrapped'), '<ol><li><p>first</p></li><li><p>second\nwrapped</p></li></ol>');
  assert.equal(md(h, '- first\n\n  second'), '<ul><li><p>first</p><p>second</p></li></ul>');
  assert.equal(md(h, '- [ ] todo\n- [x] done'), '<ul><li>☐ todo</li><li>☑ done</li></ul>');
  const unmatched = Array.from({ length: 6000 }, () => 'word*').join(' ');
  assert.equal(md(h, unmatched), `<p>${unmatched}</p>`, 'unmatched closers stay literal without rescanning every prior delimiter');
  // Codex style: a bold line, then bullets directly under it, then a list under a paragraph line
  assert.equal(md(h, '**Changes**\n- a\n- b\nNext:\n1. c'), '<p><strong>Changes</strong></p><ul><li>a</li><li>b\nNext:</li></ul><ol><li>c</li></ol>');
  assert.equal(md(h, 'Changes:\n- a\n- b'), '<p>Changes:</p><ul><li>a</li><li>b</li></ul>');
  // a number that is not 1 does not turn a paragraph line into a list
  assert.equal(md(h, 'In\n2024. A year'), '<p>In\n2024. A year</p>');
  assert.equal(md(h, '> quoted\n> more\n\nafter'), '<blockquote><p>quoted\nmore</p></blockquote><p>after</p>');
  assert.equal(md(h, '| a | b |\n|---|:-:|\n| 1 | 2 |\n| 3 |'), '<table><thead><tr><th>a</th><th style="text-align:center">b</th></tr></thead><tbody><tr><td>1</td><td style="text-align:center">2</td></tr><tr><td>3</td><td style="text-align:center"></td></tr></tbody></table>');
  assert.equal(md(h, 'Summary\n---\n* * *'), '<p>Summary</p><hr><hr>');
  assert.equal(md(h, '```go\nfunc main() {\n\tx := "<b>"\n}\n```\nafter'), '<pre><code class="lang-go">func main() {\n\tx := &quot;&lt;b&gt;&quot;\n}\n</code></pre><p>after</p>');
  // an unclosed fence runs to the end; a fence inside a list item is parsed with the item
  assert.equal(md(h, '```\nls'), '<pre><code>ls\n</code></pre>');
  assert.equal(md(h, '- run:\n  ```\n  ls\n  ```\n- next'), '<ul><li>run:<pre><code>ls\n</code></pre></li><li>next</li></ul>');
  assert.equal(md(h, 'a ``x`y`` b \\*not em\\*'), '<p>a <code>x`y</code> b *not em*</p>');
  // intraword underscores (paths, identifiers) and lone stars are literal; the rule of three holds
  assert.equal(md(h, '~/foo_bar_baz and session_test.go and ls *.go'), '<p>~/foo_bar_baz and session_test.go and ls *.go</p>');
  assert.equal(md(h, '***both*** and **bold *em* bold** and *a**'), '<p><strong><em>both</em></strong> and <strong>bold <em>em</em> bold</strong> and <em>a</em>*</p>');
  assert.equal(h.errors.length, 0);
});

test('links: web links open in a new tab, file references are a tooltip, images are never fetched, bare URLs are trimmed', async () => {
  const h = harness();
  const web = '<a href="https://example.com/a?b=1" target="_blank" rel="noopener noreferrer">docs</a>';
  assert.equal(md(h, '[docs](https://example.com/a?b=1 "title")'), `<p>${web}</p>`);
  assert.equal(md(h, '[mail](mailto:dev@example.com)'), '<p><a href="mailto:dev@example.com" target="_blank" rel="noopener noreferrer">mail</a></p>');
  // Claude Code's file reference: the text, the path on hover, no navigation
  assert.equal(md(h, 'see [app.js:12](/home/synthetic/app.js) and [rel](./x.md)'), '<p>see <span class="md-ref" title="/home/synthetic/app.js">app.js:12</span> and <span class="md-ref" title="./x.md">rel</span></p>');
  assert.equal(md(h, '![shot](https://example.com/s.png)'), '<p><a href="https://example.com/s.png" target="_blank" rel="noopener noreferrer">shot</a></p>');
  assert.equal(md(h, '**http://127.0.0.1:7799/** and (https://x.org/p(1)), <https://x.org/q>.'), '<p><strong><a href="http://127.0.0.1:7799/" target="_blank" rel="noopener noreferrer">http://127.0.0.1:7799/</a></strong> and (<a href="https://x.org/p(1)" target="_blank" rel="noopener noreferrer">https://x.org/p(1)</a>), <a href="https://x.org/q" target="_blank" rel="noopener noreferrer">https://x.org/q</a>.</p>');
  // link text never nests a link; a destination with a space is not a link at all
  assert.equal(md(h, '[see https://a.b](https://c.d) [x](not a url)'), '<p><a href="https://c.d" target="_blank" rel="noopener noreferrer">see https://a.b</a> [x](not a url)</p>');
  assert.equal(h.errors.length, 0);
});

// safeHTML is the structural guarantee: every tag is one of the renderer's own, every attribute
// one it writes, every href a web or mail scheme; no other `<` or `&` survives unescaped
function safeHTML(html) {
  const tags = new Set(['p', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'ul', 'ol', 'li', 'blockquote', 'pre', 'code', 'table', 'thead', 'tbody', 'tr', 'th', 'td', 'a', 'strong', 'em', 'del', 'hr', 'span']);
  const attrs = {
    a: /^href="(https?:\/\/|mailto:)[^"\s]*" target="_blank" rel="noopener noreferrer"$/i,
    span: /^class="md-ref" title="[^"]*"$/, code: /^class="lang-[\w.+-]+"$/, ol: /^start="\d+"$/,
    td: /^style="text-align:(left|right|center)"$/, th: /^style="text-align:(left|right|center)"$/,
  };
  for (const [, close, name, rest] of html.matchAll(/<(\/?)([a-z0-9]*)([^>]*)>/g)) {
    assert.ok(tags.has(name), `tag <${close}${name}> in ${html.slice(0, 80)}`);
    if (rest.trim()) assert.match(rest.trim(), attrs[name] || /^$/, `attributes on <${name}>: ${rest}`);
  }
  assert.doesNotMatch(html, /<(?![\/a-z])/, 'a bare <');
  assert.doesNotMatch(html, /&(?!(amp|lt|gt|quot|#39);)/, 'a bare &');
}

test('whatever the text tries, the output holds only the renderer\'s own tags and web links', async () => {
  const h = harness();
  const nasty = [
    '<script>alert(1)</script><img src=https://evil.example/p.png onerror=alert(1)>',
    '[x](javascript:alert(1)) [y](JavaScript:alert(1)) [z](java\tscript:alert(1)) [w](&#106;avascript:alert(1)) [v](data:text/html,x) [u](vbscript:x)',
    '![tracker](https://evil.example/t.gif) <https://a.b/"onmouseover="alert(1)> https://a.b/x"y',
    '[t](https://a.b/x"onclick="alert(1)) [t](https://a.b/x\'onclick=\'alert(1))',
    '```html\n<script>x</script>\n```\n`<b>` | <i> |\n|---|---|\n| <u> | & |',
    '# <h1 onclick=x>\n> <div>\n- <li>\n\n**<em>**',
    '*'.repeat(30000), '['.repeat(30000), '`'.repeat(30000), ('[a](b) ' + '**' + '_').repeat(4000),
    '[a]('.repeat(8000), '[x]'.repeat(10000) + ']('.repeat(5000),
  ];
  const started = Date.now();
  for (const text of nasty) safeHTML(md(h, text));
  assert.ok(Date.now() - started < 2000, 'pathological input renders in bounded time');
  // and the escapes are the right ones
  assert.equal(md(h, '<script>x</script> & "q"'), '<p>&lt;script&gt;x&lt;/script&gt; &amp; &quot;q&quot;</p>');
  assert.equal(md(h, '[x](javascript:alert(1))'), '<p><span class="md-ref" title="javascript:alert(1)">x</span></p>');
  assert.equal(h.errors.length, 0);
});

/* ---------- harness.js: the rendered view of a harness message ---------- */

const hm = (h, text, ref) => h.run(`Harness.render(${JSON.stringify(text)}, ${JSON.stringify(ref)})`);
// the harness view adds its own fixed tags to the renderer's
function safeHarnessHTML(html) {
  const own = { section: /^class="harness-section( unclosed)?"$/, h4: /^class="harness-tag"$/, dl: /^class="harness-fields"$/, dt: /^$/, dd: /^$/, span: /^class="(md-ref" title="[^"]*|harness-(attr|ref|empty))"$/, b: /^$/ };
  const rest = html.replace(/<\/?(section|h4|dl|dt|dd|span|b)([^>]*)>/g, (m, name, attrs) => {
    if (attrs.trim()) assert.match(attrs.trim(), own[name], `attributes on <${name}>: ${attrs}`);
    return '';
  });
  safeHTML(rest);
}

test('a harness message renders its tags as structure: fields, sections, chips, Markdown between them', async () => {
  const h = harness();
  // Claude Code's task notification: one-line values are fields, the result is Markdown
  const notice = 'task-notification\n<task-notification>\n<task-id>a0e7c</task-id>\n<status>completed</status>\n<summary>Agent "Stress-test plan" finished</summary>\n<result>## 1. Task type\nDesign review (no code changed).\n\n- one\n- two</result>\n</task-notification>';
  assert.equal(hm(h, notice, 'task-notification'),
    '<section class="harness-section"><h4 class="harness-tag">task-notification</h4>'
    + '<dl class="harness-fields"><dt>task-id</dt><dd><p>a0e7c</p></dd><dt>status</dt><dd><p>completed</p></dd><dt>summary</dt><dd><p>Agent &quot;Stress-test plan&quot; finished</p></dd></dl>'
    + '<section class="harness-section"><h4 class="harness-tag">result</h4><h2>1. Task type</h2><p>Design review (no code changed).</p><ul><li>one</li><li>two</li></ul></section></section>');
  // attributes are chips in the label; the body is Markdown
  assert.equal(hm(h, 'teammate-message\n<teammate-message teammate_id="rev-1" summary=\'done\' color=blue>The **review** is in.\nSee `x.go`.</teammate-message>', 'teammate-message'),
    '<section class="harness-section"><h4 class="harness-tag">teammate-message<span class="harness-attr">teammate_id=<b>rev-1</b></span><span class="harness-attr">summary=<b>done</b></span><span class="harness-attr">color=<b>blue</b></span></h4><p>The <strong>review</strong> is in.\nSee <code>x.go</code>.</p></section>');
  // Codex's environment context: nested elements, indented, and a whole subtree on one line —
  // a tag right after another tag is a tag; whitespace-only text is nothing
  assert.equal(hm(h, 'environment_context\n<environment_context>\n  <current_date>2026-09-21</current_date>\n  <filesystem><workspace_roots><root>/synthetic</root></workspace_roots><permission_profile type="disabled"><file_system type="unrestricted" /></permission_profile></filesystem>\n</environment_context>', 'environment_context'),
    '<section class="harness-section"><h4 class="harness-tag">environment_context</h4><dl class="harness-fields"><dt>current_date</dt><dd><p>2026-09-21</p></dd></dl>'
    + '<section class="harness-section"><h4 class="harness-tag">filesystem</h4><section class="harness-section"><h4 class="harness-tag">workspace_roots</h4><dl class="harness-fields"><dt>root</dt><dd><p>/synthetic</p></dd></dl></section>'
    + '<section class="harness-section"><h4 class="harness-tag">permission_profile<span class="harness-attr">type=<b>disabled</b></span></h4><section class="harness-section"><h4 class="harness-tag">file_system<span class="harness-attr">type=<b>unrestricted</b></span></h4></section></section></section></section>');
  // Codex's AGENTS.md: Markdown before the wrapper, Markdown inside it; a tag inside a fence is code
  assert.equal(hm(h, 'AGENTS.md\n# AGENTS.md instructions for /synthetic\n\n<INSTRUCTIONS>\n## Rules\n- run tests\n```html\n<b>literal</b>\n```\n</INSTRUCTIONS>', 'AGENTS.md'),
    '<span class="harness-ref">AGENTS.md</span><h1>AGENTS.md instructions for /synthetic</h1><section class="harness-section"><h4 class="harness-tag">INSTRUCTIONS</h4><h2>Rules</h2><ul><li>run tests</li></ul><pre><code class="lang-html">&lt;b&gt;literal&lt;/b&gt;\n</code></pre></section>');
  // a notice without a wrapper keeps its ref as an eyebrow and is Markdown
  assert.equal(hm(h, 'auto-continuation\nYour session *continues*.', 'auto-continuation'), '<span class="harness-ref">auto-continuation</span><p>Your session <em>continues</em>.</p>');
  // no ref (an older marker): the text is the text
  assert.equal(hm(h, '\nplain note', ''), '<p>plain note</p>');
  assert.equal(hm(h, 'plain note', undefined), '<p>plain note</p>');
  // an inline tag in prose is text; a closer with nothing open is text; an empty element says so
  assert.equal(hm(h, 'x\n<note>use <b>bold</b> here</note>\n</stray>\n<flag></flag>\n<solo/>', 'x'),
    '<span class="harness-ref">x</span><dl class="harness-fields"><dt>note</dt><dd><p>use &lt;b&gt;bold&lt;/b&gt; here</p></dd></dl><p>&lt;/stray&gt;</p><dl class="harness-fields"><dt>flag</dt><dd><span class="harness-empty">(empty)</span></dd><dt>solo</dt><dd><span class="harness-empty">(empty)</span></dd></dl>');
  // Codex's sub-agent notification: a line of JSON is fields and sections by its keys — the
  // sub-agent's completion text reads as Markdown; a clipped one does not parse and is as recorded
  assert.equal(hm(h, 'subagent_notification\n<subagent_notification>{"agent_path":"a_b","status":{"completed":"**Task**\\n\\nDone."},"tags":["x",2,null],"n":3}</subagent_notification>', 'subagent_notification'),
    '<section class="harness-section"><h4 class="harness-tag">subagent_notification</h4><dl class="harness-fields"><dt>agent_path</dt><dd><p>a_b</p></dd></dl>'
    + '<section class="harness-section"><h4 class="harness-tag">status</h4><section class="harness-section"><h4 class="harness-tag">completed</h4><p><strong>Task</strong></p><p>Done.</p></section></section>'
    + '<section class="harness-section"><h4 class="harness-tag">tags</h4><dl class="harness-fields"><dt>tags[0]</dt><dd><p>x</p></dd><dt>tags[1]</dt><dd><p>2</p></dd><dt>tags[2]</dt><dd><p>null</p></dd></dl></section><dl class="harness-fields"><dt>n</dt><dd><p>3</p></dd></dl></section>');
  assert.equal(hm(h, 'subagent_notification\n<subagent_notification>{"agent_path":"a_b","stat…', 'subagent_notification'),
    '<section class="harness-section unclosed"><h4 class="harness-tag">subagent_notification</h4><pre><code class="lang-json">{&quot;agent_path&quot;:&quot;a_b&quot;,&quot;stat…\n</code></pre></section>');
  // the nesting's indentation is not the text's: a list inside <subagents> is a list, deeper indentation stays
  assert.equal(hm(h, 'x\n<env>\n  <subagents>\n    - a: B\n    - c: D\n      more\n  </subagents>\n</env>', 'x'),
    '<span class="harness-ref">x</span><section class="harness-section"><h4 class="harness-tag">env</h4><section class="harness-section"><h4 class="harness-tag">subagents</h4><ul><li>a: B</li><li>c: D\nmore</li></ul></section></section>');
  // a value that merely starts with a bracket is text, not JSON: a field, a link, a task box
  assert.equal(hm(h, 'x\n<summary>[Reviewer] finished</summary>\n[see the plan](https://example.com/p)\n<n>[x] done</n>', 'x'),
    '<span class="harness-ref">x</span><dl class="harness-fields"><dt>summary</dt><dd><p>[Reviewer] finished</p></dd></dl><p><a href="https://example.com/p" target="_blank" rel="noopener noreferrer">see the plan</a></p><dl class="harness-fields"><dt>n</dt><dd><p>[x] done</p></dd></dl>');
  // an integer JSON cannot hold refuses the structured view: the line as recorded, not a wrong number
  assert.equal(hm(h, 'x\n<n>{"id":12345678901234567890}</n>', 'x'), '<span class="harness-ref">x</span><section class="harness-section"><h4 class="harness-tag">n</h4><pre><code class="lang-json">{&quot;id&quot;:12345678901234567890}\n</code></pre></section>');
  // an autolink is not a tag
  assert.equal(hm(h, 'x\n<https://example.com/a>', 'x'), '<span class="harness-ref">x</span><p><a href="https://example.com/a" target="_blank" rel="noopener noreferrer">https://example.com/a</a></p>');
  // clipped at 1,500 characters: the open elements run to the end and are marked
  assert.equal(hm(h, 'task-notification\n<task-notification>\n<status>completed</status>\n<result>## Findings\nThe plan…', 'task-notification'),
    '<section class="harness-section unclosed"><h4 class="harness-tag">task-notification</h4><dl class="harness-fields"><dt>status</dt><dd><p>completed</p></dd></dl><section class="harness-section unclosed"><h4 class="harness-tag">result</h4><h2>Findings</h2><p>The plan…</p></section></section>');
  // an outer closer closes what is still open inside it
  assert.equal(hm(h, 'x\n<a>\n<b>one\n</a>\nafter', 'x'), '<span class="harness-ref">x</span><section class="harness-section"><h4 class="harness-tag">a</h4><dl class="harness-fields"><dt>b</dt><dd><p>one</p></dd></dl></section><p>after</p>');
  assert.equal(h.errors.length, 0);
});

test('whatever a harness message tries, the output holds only the two renderers\' own tags', async () => {
  const h = harness();
  const nasty = [
    '<script>alert(1)</script>\n<img src=https://evil.example/p.png onerror=alert(1)>\n<a href="javascript:alert(1)">x</a>',
    '<task-notification onclick="alert(1)">\n<summary onload=x>"quoted" & <i>em</i></summary>\n<result>[x](javascript:alert(1))</result>',
    '<div id="a" class="b">\n<style>body{display:none}</style>\n</div>\n<svg><use href="x"/></svg>',
    '<' + 'a>\n'.repeat(5000), '</a>\n'.repeat(5000), '<a b="' + '"'.repeat(5000) + '">', ('<x>' + '`'.repeat(50)).repeat(500),
  ];
  const started = Date.now();
  for (const text of nasty) safeHarnessHTML(hm(h, text, 'ref'));
  assert.ok(Date.now() - started < 2000, 'pathological input renders in bounded time');
  // nesting is capped: a 20,000-deep one-line nest renders (the openers past the cap are text) instead of running the stack out
  const deep = hm(h, 'ref\n' + '<a>'.repeat(20000) + 'x', 'ref');
  safeHarnessHTML(deep);
  assert.equal((deep.match(/<section /g) || []).length + (deep.match(/<dt>/g) || []).length, 64, '64 elements: 63 sections and the innermost, a one-line field');
  assert.match(deep, /&lt;a&gt;&lt;a&gt;/);
  const deepJSON = hm(h, 'ref\n<j>' + '['.repeat(20000) + ']'.repeat(20000) + '</j>', 'ref');
  safeHarnessHTML(deepJSON);
  // the tag and attribute names are escaped like any text; an event handler is a chip, not an attribute
  assert.equal(hm(h, 'ref\n<task onclick="alert(1)">x</task>', 'ref'), '<span class="harness-ref">ref</span><section class="harness-section"><h4 class="harness-tag">task<span class="harness-attr">onclick=<b>alert(1)</b></span></h4><p>x</p></section>');
  assert.equal(hm(h, 'ref\n<script>alert(1)</script>', 'ref'), '<span class="harness-ref">ref</span><dl class="harness-fields"><dt>script</dt><dd><p>alert(1)</p></dd></dl>');
  assert.equal(h.errors.length, 0);
});

test('a harness block inside a user prompt is structure too, and a plain prompt keeps every space of the record', async () => {
  const h = await harness().ready();
  const model = session();
  // Claude Code closes a paste with the id repeated on the closer
  const prompt = '<system-reminder>\nThe file was edited outside the session.\nRe-read it before editing.\n</system-reminder>\nFix the **flaky** test.\n\n<pasted_content id="a1">\n    indented paste\n</pasted_content id="a1">\nand after';
  const indented = '    four spaces lead\n    every line';
  model.lanes[0].markers = [{ kind: 'user_message', t: 1000, text: prompt }, { kind: 'user_message', t: 2000, text: indented }, { kind: 'final_answer', t: 60000, text: 'Done: see <details>\n<summary>log</summary>\nok\n</details>' }];
  model.lanes[0].turns = [{ id: 'turn', start: 1000, end: 60000, status: 'completed', final: 'Done.' }];
  await h.open('session', model);
  const conv = h.node('conversation').innerHTML;
  assert.ok(conv.includes('<div class="conv-text prose-md "><section class="harness-section"><h4 class="harness-tag">system-reminder</h4><p>The file was edited outside the session.\nRe-read it before editing.</p></section><p>Fix the <strong>flaky</strong> test.</p><section class="harness-section"><h4 class="harness-tag">pasted_content<span class="harness-attr">id=<b>a1</b></span></h4><p>indented paste</p></section><p>and after</p></div>'), 'the reminder and the paste are sections, the prompt between them Markdown, the paste dedented and closed by its attributed closer');
  // the first-message card is the same view
  assert.match(h.node('main').innerHTML, /<div class="prose-block scroll-fade prose-md"><section class="harness-section"><h4 class="harness-tag">system-reminder<\/h4>/);
  // a prompt that is indented as a whole keeps its indentation: outside every tag, spaces are the record's
  assert.ok(conv.includes(`<div class="conv-text prose-md "><p>${indented}</p></div>`), 'no dedent of top-level text');
  // an inline tag in an answer follows text and stays text; the block after it is structure
  assert.ok(conv.includes('<p>Done: see &lt;details&gt;</p><dl class="harness-fields"><dt>summary</dt><dd><p>log</p></dd></dl><p>ok\n&lt;/details&gt;</p>'), 'a closer with nothing open is text');
  assert.equal(h.errors.length, 0);
});

test('a clipped harness message offers its dialog, which completes it from the source event', async () => {
  const h = await harness().ready();
  const model = session();
  model.source = 'claude';
  const full = '<task-notification>\n<status>completed</status>\n<result>## Findings\n\n' + 'x'.repeat(2000) + '\n\nThe **end**.</result>\n</task-notification>';
  const text = 'task-notification\n' + full.slice(0, 1480) + '…';
  model.lanes[0].markers = [{ kind: 'system_message', t: 1500, ref: 'task-notification', text, src: { file: '/synthetic/s.jsonl', off: 40, len: 9 } }];
  await h.open('session', model);
  const conv = h.node('conversation').innerHTML;
  assert.match(conv, /<section class="harness-section unclosed"><h4 class="harness-tag">result<\/h4>/, 'the panel shows the clipped text, its open element marked');
  assert.match(conv, /<button class="text-btn" data-action="marker" data-mk="lane:1500:system_message"[^>]*>Full text<\/button>/);
  const opening = h.action('marker', { mk: 'lane:1500:system_message' });
  assert.match(h.node('inspector').innerHTML, /<section class="harness-section unclosed"><h4 class="harness-tag">result<\/h4>/, 'the dialog opens on the clipped text');
  // a Claude Code line: the text is message.content, one string
  h.take('/api/event?session=session&file=%2Fsynthetic%2Fs.jsonl&off=40&len=9').resolve({ type: 'user', message: { role: 'user', content: full } });
  await opening; await flush();
  const completed = h.node('mkText').innerHTML;
  assert.match(completed, /^<section class="harness-section"><h4 class="harness-tag">task-notification<\/h4>/, 'completed: the wrapper is closed');
  assert.match(completed, /<p>The <strong>end<\/strong>\.<\/p><\/section><\/section>$/);
  // the switch shows the completed text as recorded, ref line first like every harness marker
  h.action('text-view');
  assert.match(h.node('inspector').innerHTML, /<div class="prose-block prose-log prose-raw" id="mkText">task-notification\n&lt;task-notification&gt;/);
  h.take('/api/event?session=session&file=%2Fsynthetic%2Fs.jsonl&off=40&len=9').resolve({ type: 'user', message: { role: 'user', content: [{ type: 'text', text: full }] } });
  await flush();
  assert.match(h.node('mkText').innerHTML, /The \*\*end\*\*\.&lt;\/result&gt;\n&lt;\/task-notification&gt;$/, 'text blocks are joined too');
  assert.equal(h.errors.length, 0);
});

test('every prose panel renders Markdown and one switch shows every text as recorded', async () => {
  const h = await harness().ready();
  const model = session();
  const request = 'Fix `session_test.go`:\n- first\n- second';
  const answer = '## Done\n\nSee [app.js:1](/synthetic/app.js).';
  const notice = 'task-notification\n<task-notification>\n<status>completed</status>\n<result>## Done\n\nAll **good**.</result>\n</task-notification>';
  model.lanes[0].markers = [
    { kind: 'user_message', t: 1000, text: request }, { kind: 'system_message', t: 1500, ref: 'task-notification', text: notice },
    { kind: 'question', t: 30000, text: 'Ship **now**?' }, { kind: 'final_answer', t: 60000, text: answer },
  ];
  model.lanes[0].turns = [{ id: 'turn', start: 1000, end: 60000, status: 'completed', final: answer }];
  await h.open('session', model);
  const rendered = () => h.node('main').innerHTML;
  assert.match(rendered(), /<div class="prose-block scroll-fade prose-md"><p>Fix <code>session_test.go<\/code>:<\/p><ul><li>first<\/li><li>second<\/li><\/ul><\/div>/);
  assert.match(rendered(), /<div class="answer-block prose-block scroll-fade prose-md" tabindex="0"><h2>Done<\/h2><p>See <span class="md-ref" title="\/synthetic\/app.js">app.js:1<\/span>\.<\/p><\/div>/);
  const conv = () => h.node('conversation').innerHTML;
  assert.match(conv(), /<div class="conv-text prose-md "><p>Ship <strong>now<\/strong>\?<\/p><\/div>/);
  // a harness message is its tags as structure: the wrapper a section, a one-line value a
  // field, a longer one Markdown; the ref line is the label, not text
  const noticeHTML = '<section class="harness-section"><h4 class="harness-tag">task-notification</h4><dl class="harness-fields"><dt>status</dt><dd><p>completed</p></dd></dl><section class="harness-section"><h4 class="harness-tag">result</h4><h2>Done</h2><p>All <strong>good</strong>.</p></section></section>';
  assert.ok(conv().includes(`<div class="conv-text prose-md ">${noticeHTML}</div>`), 'the harness message is rendered as structure');
  assert.doesNotMatch(conv(), /Full text/, 'a message that is not clipped offers no dialog');
  // the clamp button hides for a short text and shows for a long one
  assert.match(conv(), /<button class="text-btn" data-action="conv-toggle" data-key="60000:final_answer" style="margin-top:4px" hidden>Show all<\/button>/);
  const long = { ...model, lanes: [{ ...model.lanes[0], markers: [{ kind: 'user_message', t: 1000, text: 'x'.repeat(400) }] }] };
  await h.open('long', long);
  assert.match(conv(), /data-key="1000:user_message" style="margin-top:4px" >Show all</);
  await h.open('session', model);
  // the switch: every block turns raw and says so, the buttons flip, the marker dialog follows
  h.action('text-view');
  assert.equal(h.run('state.rawText'), true);
  const blocks = () => h.node('firstMessage').innerHTML + h.node('lastRecorded').innerHTML + h.node('convActions').innerHTML;
  assert.match(blocks(), /<div class="prose-block scroll-fade prose-raw">Fix `session_test.go`:\n- first\n- second<\/div>/);
  assert.match(blocks(), /<div class="answer-block prose-block scroll-fade prose-raw" tabindex="0">## Done\n\nSee \[app.js:1\]\(\/synthetic\/app.js\)\.<\/div>/);
  assert.match(conv(), /<div class="conv-text prose-raw ">Ship \*\*now\*\*\?<\/div>/);
  assert.doesNotMatch(blocks(), /Show raw/);
  assert.equal((blocks().match(/>Show rendered</g) || []).length, 3, 'first message, last answer, conversation');
  h.run("inspectMarker('lane:30000:question')");
  assert.match(h.node('inspector').innerHTML, /<div class="prose-block prose-log prose-raw" id="mkText">Ship \*\*now\*\*\?<\/div>/);
  h.action('text-view');
  assert.match(h.node('inspector').innerHTML, /<div class="prose-block prose-log prose-md" id="mkText"><p>Ship <strong>now<\/strong>\?<\/p><\/div>/);
  assert.match(conv(), /<div class="conv-text prose-md "><p>Ship <strong>now<\/strong>\?<\/p><\/div>/);
  // the harness message follows the same switch: as recorded in the raw view, structure in the other
  h.action('text-view');
  assert.ok(conv().includes(`<div class="conv-text prose-raw ">${notice.replace(/</g, '&lt;').replace(/>/g, '&gt;')}</div>`), 'raw: the recorded text, ref line included');
  h.run("inspectMarker('lane:1500:system_message')");
  assert.match(h.node('inspector').innerHTML, /<div class="prose-block prose-log prose-raw" id="mkText">task-notification\n&lt;task-notification&gt;/);
  assert.match(h.node('inspector').innerHTML, /text-view/, 'the marker dialog of a harness message carries the switch');
  h.action('text-view');
  assert.ok(h.node('inspector').innerHTML.includes(`<div class="prose-block prose-log prose-md" id="mkText">${noticeHTML}</div>`));
  // a marker that is not a message stays as recorded and offers no switch
  const other = { ...model, id: 'other', lanes: [{ ...model.lanes[0], markers: [{ kind: 'compaction', t: 45000, text: 'summary <b>kept</b>' }] }] };
  await h.open('other', other);
  h.run("inspectMarker('lane:45000:compaction')");
  assert.match(h.node('inspector').innerHTML, /<div class="prose-block prose-log prose-raw" id="mkText">summary &lt;b&gt;kept&lt;\/b&gt;<\/div>/);
  assert.doesNotMatch(h.node('inspector').innerHTML, /text-view/);
  assert.equal(h.errors.length, 0);
});

test('the grain rasters for a dense screen are laid exactly as app.css lays the SVG tile', async () => {
  const h = harness();
  await h.ready();
  // a 1x screen (the harness reports none) keeps the stylesheet's own value
  assert.equal(h.run('Grain.density()'), 1);
  const css = readFileSync(join(__dirname, 'web/app.css'), 'utf8');
  const declared = css.match(/--grain:([^;]+);/)[1];
  assert.equal(h.run('Grain.value(["grain.svg", "grain.svg"])'), declared);
  // the rasters take the SVG's place layer by layer: same offsets, same sizes
  assert.equal(h.run('Grain.value(["blob:a", "blob:b"])'), declared.replace('url("grain.svg")', 'url("blob:a")').replace('url("grain.svg")', 'url("blob:b")'));
});

test('the session list description keeps underscores inside identifiers', async () => {
  const h = harness();
  assert.equal(h.run("descriptionOf('Fix **session_test.go** and __init__.py, see _notes_.\\n\\nMore.', 100)"), 'Fix session_test.go and init.py, see notes.');
});

test('sessions of paired servers share the list: a host chip per remote row, a Host select that narrows the list, and one project entry per path counting its hosts', async () => {
  const h = await harness().ready();
  const now = Date.now(), day = 24 * 3600e3;
  const list = [
    { id: 'L-1', source: 'codex', title: 'Local one', cwd: '/srv/app', updated: now - day, started: now - 2 * day, bytes: 1e6, agents: 0 },
    { id: 'R-1@web-01', host: 'web-01', source: 'codex', title: 'Remote one', cwd: '/srv/app', updated: now - 2 * day, started: now - 2 * day, bytes: 1e6, agents: 0 },
    { id: 'R-2@web-02', host: 'web-02', source: 'claude', title: 'Remote two', cwd: '/srv/other', updated: now - 3 * day, started: now - 3 * day, bytes: 1e6, agents: 0 },
  ];
  h.run("go('sessions')");
  h.take('/api/sessions').resolve(list); await flush();
  let main = h.node('main').innerHTML;
  // the Host select: this machine first, then the agents, each with its count in the period
  assert.match(main, /<label class="ctl">Host<select class="select" id="fleetHost" aria-label="Host"><option value="" selected>All hosts<\/option><option value="\." >This machine \(1\)<\/option><option value="web-01" >web-01 \(1\)<\/option><option value="web-02" >web-02 \(1\)<\/option><\/select><\/label>/);
  // the projects in name order, whatever their session counts
  assert.match(main, /id="fleetProject"[^>]*>(?:(?!<\/select>).)*srv\/app \(2\)(?:(?!<\/select>).)*srv\/other \(1\)(?:(?!<\/select>).)*<\/select>/);
  // one project entry for the path seen on two hosts, saying so; a path seen on one agent
  // only is labelled with that agent; no "…/" before a path
  assert.match(main, /id="fleetProject"[^>]*>(?:(?!<\/select>).)*<option value="\/srv\/app" >srv\/app \(2\) · 2 hosts<\/option>(?:(?!<\/select>).)*<\/select>/);
  assert.match(main, /<option value="\/srv\/other"  data-host="web-02" data-name="srv\/other \(1\)">web-02 · srv\/other \(1\)<\/option>/);
  assert.match(main, /<option value="\/srv\/app" >srv\/app \(2\) · 2 hosts<\/option>/, 'no host attributes on a project of several hosts');
  assert.doesNotMatch(main, /…\/srv/);
  // the rows: the host is the second column — the agent's chip for a remote row, empty for a
  // local one; the tile (no columns) carries the chip before the title
  let rows = h.node('fleetRows').innerHTML;
  assert.match(rows, /<th>Session<\/th><th>Host<\/th><th>Started<\/th>/);
  // each host in its own hue, 60° apart: web-01 (first) at 200, web-02 at 260
  assert.match(rows, /data-id="R-1@web-01"><strong><i class="source-mark source-codex"[^>]*><\/i>Remote one<\/strong>[\s\S]*?<\/button><\/td><td><span class="host-mark" style="--host-hue:200" title="Read on web-01 by its todobem agent">web-01<\/span><\/td>/);
  assert.match(rows, /data-id="R-2@web-02"[\s\S]*?<td><span class="host-mark" style="--host-hue:260"[^>]*>web-02<\/span><\/td>/);
  assert.match(rows, /data-id="L-1"><strong><i class="source-mark source-codex"[^>]*><\/i>Local one<\/strong>[\s\S]*?<\/button><\/td><td><\/td>/);
  assert.match(rows, /<article class="session-tile"><button class="session-link" data-action="session" data-id="R-1@web-01"><strong><i class="source-mark source-codex"[^>]*><\/i><span class="host-mark"[^>]*>web-01<\/span>Remote one/);
  // the footer names the paired servers
  assert.match(main, /Local files and 2 paired servers\. Nothing leaves this machine except to them\./);
  // narrowing to one host keeps its rows only; the project select follows
  h.document.emit('change', { target: { id: 'fleetHost', value: 'web-01' } });
  rows = h.node('fleetRows').innerHTML;
  assert.match(rows, /data-id="R-1@web-01"/);
  assert.doesNotMatch(rows, /data-id="L-1"|data-id="R-2@web-02"/);
  assert.equal(h.node('fleetCount').textContent, '1 of 3 sessions');
  assert.match(h.node('main').innerHTML, /web-01 · srv\/app \(1\)<\/option>/, 'one host in view: the project names it, no host count');
  // this machine only
  h.document.emit('change', { target: { id: 'fleetHost', value: '.' } });
  rows = h.node('fleetRows').innerHTML;
  assert.match(rows, /data-id="L-1"/);
  assert.doesNotMatch(rows, /@web-0/);
  // the search finds a session by its host
  h.document.emit('change', { target: { id: 'fleetHost', value: '' } });
  h.document.emit('input', { target: { id: 'sessionSearch', value: 'web-02' } });
  assert.equal(h.node('fleetCount').textContent, '1 of 3 sessions');
  // no remote rows: no Host select, the footer as before
  h.document.emit('input', { target: { id: 'sessionSearch', value: '' } });
  h.run("go('sessions')");
  h.take('/api/sessions').resolve([list[0]]); await flush();
  main = h.node('main').innerHTML;
  assert.doesNotMatch(main, /id="fleetHost"/);
  assert.match(main, /Local files only\. Nothing leaves this machine\./);
});

test('the drawn project list puts a host badge in its own column and the names under each other', async () => {
  const h = await harness().ready();
  const now = Date.now(), day = 24 * 3600e3;
  const list = [
    { id: 'L-1', source: 'codex', title: 'Local', cwd: '/srv/app', updated: now - day, started: now - day, bytes: 1, agents: 0 },
    { id: 'R-2@web-02', host: 'web-02', source: 'claude', title: 'Remote', cwd: '/srv/other', updated: now - day, started: now - day, bytes: 1, agents: 0 },
  ];
  h.run("go('sessions')");
  h.take('/api/sessions').resolve(list); await flush();
  // the rows as dropdown.js reads them from the select's options (the harness has no DOM
  // options: the same shape optionsOf produces)
  h.run('var withHosts = [{ index: 0, label: "All projects", disabled: false, host: "", name: "" }, { index: 1, label: "web-02 · srv/other (1)", disabled: false, host: "web-02", name: "srv/other (1)" }, { index: 2, label: "srv/app (1)", disabled: false, host: "", name: "" }]');
  const rows = h.run('Dropdown.render(withHosts, 0, 0)');
  assert.match(rows, /<div class="dropdown-option active"[^>]*><span class="dropdown-host"><\/span><span class="dropdown-text">All projects<\/span><\/div>/);
  assert.match(rows, /<span class="dropdown-host"><span class="host-mark" style="--host-hue:200"[^>]*>web-02<\/span><\/span><span class="dropdown-text">srv\/other \(1\)<\/span>/);
  assert.match(rows, /<span class="dropdown-host"><\/span><span class="dropdown-text">srv\/app \(1\)<\/span>/);
  assert.equal(h.run('Dropdown.hostColumn(withHosts)'), 'web-02'.length);
  // type-ahead matches the name, not the badge
  assert.equal(h.run('Dropdown.matchIndex(withHosts, 0, "s")'), 1);
  // the filter: a fragment of the name or of the host keeps a row; the rest are hidden in the
  // drawing and stepped over by the arrows
  assert.equal(h.run('JSON.stringify(Dropdown.filtered(withHosts, "app").map(o => o.disabled))'), '[true,true,false]');
  assert.equal(h.run('JSON.stringify(Dropdown.filtered(withHosts, "web-0").map(o => o.disabled))'), '[true,false,true]');
  assert.equal(h.run('JSON.stringify(Dropdown.filtered(withHosts, "").map(o => o.disabled))'), '[false,false,false]');
  const drawn = h.run('Dropdown.render(withHosts, 0, 0, "other")');
  assert.match(drawn, /data-index="0" aria-selected="true" hidden>/);
  assert.match(drawn, /data-index="1" aria-selected="false">/);
  assert.equal(h.run('Dropdown.nextIndex(Dropdown.filtered(withHosts, "other"), -1, 1)'), 1);
  // the project select asks for the search field; the period select does not
  assert.match(h.node('main').innerHTML, /<select class="select" id="fleetProject" aria-label="Project" data-filter="Type to filter by name or host">/);
  assert.match(h.node('main').innerHTML, /<select class="select" id="fleetPeriod" aria-label="Period">/);
  // a list without hosts draws plain rows
  h.run('var plain = [{ index: 0, label: "Last 7 days", disabled: false, host: "", name: "" }]');
  assert.doesNotMatch(h.run('Dropdown.render(plain, 0, 0)'), /dropdown-host/);
  assert.equal(h.run('Dropdown.hostColumn(plain)'), 0);
});

test('the Insights query names the host in scope, and the session page shows the host of a remote session', async () => {
  const h = await harness().ready();
  assert.equal(h.run("insightsQuery({ kind: '30d', cwd: '/srv/app', host: 'web-01', sources: {} })"), 'period=30d&cwd=%2Fsrv%2Fapp&hosts=web-01');
  assert.equal(h.run("insightsQuery({ kind: '30d', cwd: '', host: '.', sources: {} })"), 'period=30d&hosts=.');
  assert.equal(h.run("insightsQuery({ kind: 'session', session: 'x@web-01', host: 'web-01', sources: {} })"), 'period=session&session=x%40web-01');
  const model = session('R-1@web-01');
  model.host = 'web-01';
  model.lanes[0].markers.push({ t: 5000, kind: 'user_message', lane: 'lane', text: 'Build the thing', src: { file: '/srv/app/rollout.jsonl', off: 10, len: 20 } });
  await h.open('R-1@web-01', model);
  assert.match(h.node('main').innerHTML, /<p class="subtitle"><span class="host-mark" style="--host-hue:\d+" title="Read on web-01 by its todobem agent">web-01<\/span>/);
  // the inspector asks the hub for a marker's source with the session named, so a remote span is routed to its agent
  h.run("inspectMarker('lane:5000:user_message')");
  h.take('/api/event?session=R-1%40web-01&file=%2Fsrv%2Fapp%2Frollout.jsonl&off=10&len=20');
});

test('a remote session whose agent did not answer says so and offers a retry', async () => {
  const h = await harness().ready();
  h.run("go('session', 'R-1@web-01')");
  h.take('/api/sessions/R-1%40web-01').resolve('agent web-01: Get "https://10.0.0.5:7789/agent/v1/sessions/R-1": read tcp: connection reset by peer', 502);
  await flush();
  const main = h.node('main').innerHTML;
  assert.match(main, /<h3>The agent did not answer<\/h3>/);
  assert.match(main, /connection reset by peer/);
  assert.match(main, /data-action="retry-session"/);
  // the retry asks for the same session again
  h.action('retry-session');
  h.take('/api/sessions/R-1%40web-01').resolve(session('R-1@web-01'));
  await flush();
  assert.equal(h.run('state.id'), 'R-1@web-01');
  // any other failure keeps the plain message
  h.run("go('session', 'gone')");
  h.take('/api/sessions/gone').resolve('unknown session gone', 404);
  await flush();
  assert.match(h.node('main').innerHTML, /<h3>Could not load session<\/h3>/);
  assert.doesNotMatch(h.node('main').innerHTML, /retry-session/);
  assert.equal(h.errors.length, 2, 'each failed load is logged to the console, nothing else');
});
