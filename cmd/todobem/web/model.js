'use strict';
// model.js — normalize and index one derived session for the viewer. Remote models cross a
// trust boundary: unknown activity/stage enum values stay honest as `unknown`, and never become
// markup attributes or crash lookups in the fixed local palettes.

function normalizeModelEnums(m) {
  for (const l of m.lanes) {
    for (const o of l.ops) {
      if (!PHASES[o.phase]) o.phase = 'unknown';
      if (o.lc && !LIFECYCLES[o.lc]) o.lc = 'unknown';
      for (const sh of o.shares || []) {
        if (!PHASES[sh.phase]) sh.phase = 'unknown';
        if (sh.lc && !LIFECYCLES[sh.lc]) sh.lc = 'unknown';
      }
    }
    for (const sg of l.segments) {
      if (!PHASES[sg.p]) sg.p = 'unknown';
      if (sg.lc && !LIFECYCLES[sg.lc]) sg.lc = 'unknown';
    }
  }
}

function prepare(m) {
  m.groups = m.groups || [];
  m.lanes = m.lanes || [];
  m.totals = m.totals || {};
  m.totals.by_kind = m.totals.by_kind || {};
  m.totals.by_phase = m.totals.by_phase || {};
  for (const l of m.lanes) {
    l.turns = l.turns || [];
    l.ops = l.ops || [];
    l.markers = l.markers || [];
    l.segments = l.segments || [];
    l.active = l.active || [];
    l.by_phase = l.by_phase || {};
  }
  normalizeModelEnums(m);
  for (const l of m.lanes) for (const mk of l.markers) mk.text = mk.text ?? '';
  m.opById = new Map();
  m.laneById = new Map();
  for (const l of m.lanes) {
    m.laneById.set(l.id, l);
    l.ops.sort((a, b) => a.start - b.start);
    for (const o of l.ops) m.opById.set(o.id, o);
    l.opsByEnd = [...l.ops].sort((a, b) => b.end - a.end);
  }
  m.groupById = new Map(m.groups.map(g => [g.id, g]));
  // Identical tool calls that follow each other in one lane are one display run. Model output
  // between calls does not break it; a background call does.
  for (const l of m.lanes) {
    let run = null;
    for (const o of l.ops) {
      if (o.phase === 'llm') continue;
      if (o.background) {
        run = null;
        continue;
      }
      if (run && run.kind === o.kind && run.title === o.title) {
        o.run = run.id;
        continue;
      }
      run = { id: `${l.id}:${o.id}`, kind: o.kind, title: o.title };
      o.run = run.id;
    }
  }
  const runSize = new Map();
  for (const l of m.lanes) for (const o of l.ops) runSize.set(o.run, (runSize.get(o.run) || 0) + 1);
  for (const l of m.lanes) for (const o of l.ops) if (runSize.get(o.run) < 2) delete o.run;
  // Spawn/complete markers live on the parent lane; index them by child lane id.
  m.agentMarks = new Map();
  for (const l of m.lanes) {
    for (const mk of l.markers) {
      if (!mk.kind.startsWith('agent_') || !mk.ref) continue;
      if (!m.agentMarks.has(mk.ref)) m.agentMarks.set(mk.ref, []);
      m.agentMarks.get(mk.ref).push(mk);
    }
  }
  return m;
}
