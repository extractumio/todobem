'use strict';
// harness.js — the rendered view of a recorded message: the tags the harness put in it become
// structure, the text between them is Markdown (markdown.js). A harness message (a
// `system_message` marker, the text the harness — not the human — put into the user role) is
// all tags: Claude Code's <task-notification> with <task-id>, <status>, <summary> and a Markdown
// <result>, a <teammate-message teammate_id="…">; Codex's AGENTS.md inside <INSTRUCTIONS>, an
// <environment_context> of nested fields, a <recommended_plugins> list. A user's prompt carries
// them too: the <system-reminder>, <pasted_content> or <ide_selection> block Claude Code slips
// into it. Shown as recorded, these read as a wall of angle brackets; a message without tags is
// exactly its Markdown. Here the tags become the structure they carry: an element
// whose value is one line is a field of a definition list, one with a longer body or nested
// elements is a labelled section, an attribute a chip in that label, a line of JSON (Codex's
// <subagent_notification>) the same fields and sections by its keys, and every other run of
// text is Markdown through markdown.js. Its guarantees hold here: a tag name or attribute reaches the
// page through esc(), the HTML tags are this renderer's own fixed set, and nothing is fetched.
// The recorded text is one toggle away (the Raw / rendered switch of every prose panel).
// What is a tag: an opener <name …> with nothing but blanks between it and the start of its
// line or the tag before it (Codex writes <filesystem><workspace_roots><root>…</root>… on one
// line; an inline <b> in prose follows text and stays text), never inside a Markdown fence; a
// closer </name> anywhere — attributes allowed, Claude Code writes </pasted_content id="…"> —
// for the innermost open element of that name; an
// element left open when the text ends — the adapters clip a harness message at 1,500
// characters — runs to the end. A harness marker's first line is its ref (the tag name the
// adapter recognised); it is dropped here and shown as an eyebrow when the text itself does not
// open with that tag. prose() in markdown.js is the one caller: the memo, the Raw switch and
// the fallback to the escaped text live there.
// Load order does not matter: esc() and state are app.js globals, Markdown is markdown.js's,
// all resolved at call time.

const Harness = (() => {
  // deeper than this, an opener is text: the view recurses per level and a harness message
  // nests a handful deep; the cap keeps a hostile text from running the stack out
  const MAX_DEPTH = 64;
  const NAME = '[A-Za-z][\\w.:-]*';
  const ATTR = '[\\w.:-]+(?:=(?:"[^"]*"|\'[^\']*\'|[^\\s"\'>]+))?';
  const OPEN = new RegExp(`^<(${NAME})((?:\\s+${ATTR})*)\\s*(/?)>`);
  const CLOSE = new RegExp(`^</(${NAME})(?:\\s+${ATTR})*\\s*>`); // Claude Code closes a paste with </pasted_content id="…">
  const ATTRS = /([\w.:-]+)(?:=("[^"]*"|'[^']*'|[^\s"'>]+))?/g;
  const FENCE = /^ {0,3}(`{3,}|~{3,})/;
  const FENCE_CLOSE = /^ {0,3}(`{3,}|~{3,})[ \t]*$/;

  /* ---------- the tree ---------- */

  // attributes reads `name="value"` pairs (quoted either way, or bare; a name alone is empty)
  function attributes(s) {
    const out = [];
    for (const m of (s || '').matchAll(ATTRS)) {
      const raw = m[2] || '';
      out.push([m[1], /^["']/.test(raw) ? raw.slice(1, -1) : raw]);
    }
    return out;
  }

  // parse turns the text into a tree: a node is { text } or { tag, attrs, children, unclosed }.
  // One pass over the lines; a fenced code block is copied whole, text is buffered until a tag
  // opens or closes.
  function parse(text) {
    const root = { tag: '', attrs: [], children: [] };
    const stack = [root];
    const top = () => stack[stack.length - 1];
    let buf = '';
    const flush = () => {
      if (buf) top().children.push({ text: buf });
      buf = '';
    };
    const lines = text.split('\n');
    for (let i = 0; i < lines.length; i++) {
      const line = lines[i];
      const nl = i < lines.length - 1 ? '\n' : '';
      const fence = FENCE.exec(line);
      if (fence) {
        buf += line + nl;
        for (i++; i < lines.length; i++) {
          buf += lines[i] + (i < lines.length - 1 ? '\n' : '');
          const close = FENCE_CLOSE.exec(lines[i]);
          if (close && close[1][0] === fence[1][0] && close[1].length >= fence[1].length) break;
        }
        continue;
      }
      let pos = 0;
      let edge = true; // nothing but blanks since the start of the line or the last tag
      while (pos < line.length) {
        const lt = line.indexOf('<', pos);
        if (lt < 0) {
          buf += line.slice(pos);
          break;
        }
        const before = line.slice(pos, lt);
        if (before.trim()) edge = false;
        buf += before;
        const rest = line.slice(lt);
        let m = CLOSE.exec(rest);
        if (m && stack.some(n => n.tag === m[1])) {
          flush();
          while (top().tag !== m[1]) stack.pop(); // an outer closer closes what is still open inside it
          stack.pop();
          pos = lt + m[0].length;
          edge = true;
          continue;
        }
        m = edge && stack.length - 1 < MAX_DEPTH ? OPEN.exec(rest) : null; // stack[0] is the root
        if (m) {
          flush();
          const node = { tag: m[1], attrs: attributes(m[2]), children: [] };
          top().children.push(node);
          if (!m[3]) stack.push(node);
          pos = lt + m[0].length;
          continue;
        }
        buf += '<';
        pos = lt + 1;
        edge = false;
      }
      buf += nl;
    }
    flush();
    while (stack.length > 1) stack.pop().unclosed = true;
    return root;
  }

  /* ---------- the view ---------- */

  // trimBlank drops the blank lines around a run of text (the newline after an opener and
  // before a closer); inside an element it also drops the indentation the nesting gave every
  // line, so a list written inside <subagents> is a list (a line's own deeper indentation
  // stays). Text outside every element — a whole user message — keeps its spaces: they are
  // the record's.
  function trimBlank(s, nested) {
    const t = s.replace(/^(?:[ \t]*\n)+/, '').replace(/(?:\n[ \t]*)+$/, '');
    if (!nested) return t;
    const lines = t.split('\n');
    const indent = l => /^[ \t]*/.exec(l)[0].length;
    const common = Math.min(...lines.filter(l => l.trim()).map(indent));
    return common > 0 && common < Infinity ? lines.map(l => l.slice(Math.min(common, indent(l)))).join('\n') : t;
  }
  const textOf = n => n.children.map(c => c.text || '').join('').trim();
  // Codex writes one harness message as a line of JSON: <subagent_notification>{"agent_path":…,
  // "status":{"completed":"…"}}</subagent_notification>. Parsed, it is the same structure the
  // tags carry — a key an element named after it, a string its text, an array one element per
  // item — and is rendered the same way, so the sub-agent's completion text reads as Markdown.
  // A line is JSON by its opening shape only ({" or [ then " [ {): a value that merely starts
  // with a bracket — [Reviewer] finished, a [link](…) — is text. Clipped, JSON does not parse
  // and is shown as recorded, in a code block. Strings pass verbatim; a number is re-serialised
  // by JSON.parse (1.0 reads 1) and one beyond the safe integer range refuses the structured
  // view rather than read wrong; of duplicate keys the last is kept — the Raw view has the line.
  const JSONISH = /^(?:\{\s*"|\[\s*["[{])/;
  const jsonLine = t => JSONISH.test(t) && !t.includes('\n');
  function jsonTree(value, name, depth) {
    const node = { tag: name, attrs: [], children: [] };
    if (typeof value === 'number' && !Number.isSafeInteger(value) && Number.isInteger(value)) throw new RangeError('integer beyond the safe range');
    if (value === null || typeof value !== 'object' || depth >= MAX_DEPTH) node.children.push({ text: typeof value === 'string' ? value : JSON.stringify(value) });
    else if (Array.isArray(value)) value.forEach((item, i) => node.children.push(jsonTree(item, `${name}[${i}]`, depth + 1)));
    else for (const [k, v] of Object.entries(value)) node.children.push(jsonTree(v, k, depth + 1));
    return node;
  }
  function jsonHTML(t) {
    try {
      return nodes(jsonTree(JSON.parse(t), '', 0).children, true);
    } catch (e) {
      return `<pre><code class="lang-json">${esc(t)}\n</code></pre>`;
    }
  }
  // a field is an element with no element inside and a one-line value that is not a line of JSON
  const isField = n => !n.children.some(c => c.tag) && !textOf(n).includes('\n') && !jsonLine(textOf(n));

  const chips = attrs => attrs.map(([k, v]) => `<span class="harness-attr">${esc(k)}${v ? `=<b>${esc(v)}</b>` : ''}</span>`).join('');

  // nodes renders a list of siblings: consecutive fields share one definition list; a text
  // run is Markdown; anything else is a section with the children rendered the same way
  function nodes(list, nested) {
    let out = '';
    let fields = '';
    const flushFields = () => {
      if (fields) out += `<dl class="harness-fields">${fields}</dl>`;
      fields = '';
    };
    for (const n of list) {
      if (!n.tag) {
        const t = trimBlank(n.text, nested);
        if (!t.trim()) continue;
        flushFields();
        out += jsonLine(t.trim()) ? jsonHTML(t.trim()) : Markdown.render(t);
        continue;
      }
      if (isField(n) && !n.attrs.length) {
        const v = textOf(n);
        fields += `<dt>${esc(n.tag)}</dt><dd>${v ? Markdown.render(v) : '<span class="harness-empty">(empty)</span>'}</dd>`;
        continue;
      }
      flushFields();
      out += `<section class="harness-section${n.unclosed ? ' unclosed' : ''}"><h4 class="harness-tag">${esc(n.tag)}${chips(n.attrs)}</h4>${nodes(n.children, true)}</section>`;
    }
    flushFields();
    return out;
  }

  // render turns a harness marker's text into HTML. The first line is the adapter's ref and is
  // dropped when it says so; the ref is shown above the body when the body does not open with
  // an element of that name (a notice without a wrapper: `auto-continuation`, `peer`).
  function render(text, ref) {
    const t = String(text ?? '').replace(/\r\n?/g, '\n');
    const nlAt = t.indexOf('\n');
    const first = nlAt < 0 ? t : t.slice(0, nlAt);
    const body = first === (ref || '') ? (nlAt < 0 ? '' : t.slice(nlAt + 1)) : t;
    const tree = parse(body);
    const opener = tree.children.find(n => n.tag || n.text.trim());
    const eyebrow = ref && !(opener && opener.tag === ref) ? `<span class="harness-ref">${esc(ref)}</span>` : '';
    return eyebrow + nodes(tree.children, false);
  }

  return { render, parse };
})();
