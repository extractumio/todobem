'use strict';
// markdown.js — the rendered view of a recorded message. The user's prompts and the model's
// answers are Markdown, and the page shows them as such by default; the Raw toggle shows the
// text as recorded. This is a deliberately small renderer, not a CommonMark implementation: the
// GFM subset the agents write — paragraphs, ATX headings, fenced code, lists (nested, numbered,
// task boxes), block quotes, pipe tables, rules, code spans, emphasis, strikethrough, links.
// What it guarantees, by construction rather than by a sanitizer:
//   - every character of the text reaches the page through esc(); the tags are the renderer's
//     own, from a fixed set, and the only attributes it writes are a link's href/target/rel,
//     a reference's title, a fence's language class, a list's start and a cell's alignment;
//   - a link is only http, https or mailto, checked on the raw destination before escaping;
//     any other destination — Claude Code's `[app.js:12](/abs/path)` file references, file:
//     and relative targets — is a reference: the text with the destination as its tooltip,
//     never a navigation;
//   - an image is a link (or a reference) to its source and never an <img>: nothing is fetched;
//   - a newline inside a paragraph or a list item stays a newline and leading spaces stay
//     (paragraphs and items are drawn pre-wrap): these are chat messages, whose line structure
//     is part of the record, so no soft-break collapsing and no setext headings ("---" under a
//     line is a rule, not a title); a 4-space indent is text, never an indented code block;
//   - an unclosed fence runs to the end of the text.
// Load order does not matter: esc() is an app.js global resolved at call time.

const Markdown = (() => {
  const FENCE = /^ {0,3}(`{3,}|~{3,})[ \t]*([^`\s]*)/;
  const FENCE_CLOSE = /^ {0,3}(`{3,}|~{3,})[ \t]*$/;
  const HEADING = /^ {0,3}(#{1,6})(?:[ \t]+(.*?))?[ \t]*#*[ \t]*$/;
  const RULE = /^ {0,3}([-*_])(?:[ \t]*\1){2,}[ \t]*$/;
  const QUOTE = /^ {0,3}> ?(.*)$/;
  const ITEM = /^( {0,3})([-*+]|\d{1,9}[.)])( {1,4}|$)(.*)$/;
  const TABLE_ROW = /\|/;
  const TABLE_DELIM = /^[ \t]*\|?[ \t]*:?-+:?[ \t]*(\|[ \t]*:?-+:?[ \t]*)*\|?[ \t]*$/;
  const TASK = /^\[([ xX])\][ \t]+/;
  const SCHEME = /^(https?:\/\/|mailto:)/i;
  const URL = /^https?:\/\/[^\s<>]+/i;
  const AUTOLINK = /^<(https?:\/\/[^\s<>]+|mailto:[^\s<>]+)>/i;
  const DESTINATION = /^(<[^>]*>|\S+)(?:\s+("[^"]*"|'[^']*'|\([^)]*\)))?$/;
  const PUNCT = /[!-\/:-@\[-`{-~\p{P}]/u;
  const ASCII_PUNCT = /[!-\/:-@\[-`{-~]/;
  const ESCAPED = /\\([!-\/:-@\[-`{-~])/g;
  // a run of characters that start no inline construct, appended in one piece
  const PLAIN = /[^\\`\[!<hH*_~]+/y;

  /* ---------- blocks ---------- */

  const blank = line => !line.trim();
  const ordered = marker => /\d/.test(marker);

  // fenceEnd finds the closing fence of the block opened at `from`; the end of the text closes
  // an open fence
  function fenceEnd(lines, from, fence) {
    for (let i = from; i < lines.length; i++) {
      const m = FENCE_CLOSE.exec(lines[i]);
      if (m && m[1][0] === fence[0] && m[1].length >= fence.length) return i;
    }
    return lines.length;
  }

  // tableAt says whether line i opens a pipe table: a row whose next line is a delimiter row
  // with the same number of cells
  function tableAt(lines, i) {
    if (!TABLE_ROW.test(lines[i]) || i + 1 >= lines.length || !TABLE_DELIM.test(lines[i + 1])) return false;
    return cells(lines[i]).length === cells(lines[i + 1]).length;
  }

  // itemAt is the list marker of line i when the line is an item with content
  function itemAt(line) {
    const m = ITEM.exec(line);
    return m && (m[4] || m[3]) ? m : null;
  }

  // startsBlock says whether a line begins something other than a paragraph, which ends the
  // paragraph or the list item before it
  function startsBlock(lines, i) {
    const line = lines[i];
    return FENCE.test(line) || HEADING.test(line) || RULE.test(line) || QUOTE.test(line) || !!itemAt(line) || tableAt(lines, i);
  }

  // cells splits a table row on unescaped pipes; the outer pipes are the row's frame, not cells
  function cells(line) {
    const out = [];
    let cur = '';
    for (let i = 0; i < line.length; i++) {
      const c = line[i];
      if (c === '\\' && line[i + 1] === '|') {
        cur += '|';
        i++;
      } else if (c === '|') {
        out.push(cur);
        cur = '';
      } else cur += c;
    }
    out.push(cur);
    if (out.length && !out[0].trim()) out.shift();
    if (out.length && !out[out.length - 1].trim()) out.pop();
    return out.map(s => s.trim());
  }

  // aligns reads the delimiter row: a colon on the left, the right or both
  const aligns = line => cells(line).map(c => c.startsWith(':') && c.endsWith(':') ? 'center' : c.endsWith(':') ? 'right' : c.startsWith(':') ? 'left' : '');

  // table renders the rows from line i to the first blank line or line without a pipe; short
  // rows are padded to the header, long ones cut
  function table(lines, i) {
    const head = cells(lines[i]);
    const align = aligns(lines[i + 1]);
    const rows = [];
    let j = i + 2;
    for (; j < lines.length && !blank(lines[j]) && TABLE_ROW.test(lines[j]); j++) rows.push(cells(lines[j]));
    const cell = (tag, text, k) => `<${tag}${align[k] ? ` style="text-align:${align[k]}"` : ''}>${inline(text || '')}</${tag}>`;
    const row = (tag, cs) => `<tr>${head.map((_, k) => cell(tag, cs[k], k)).join('')}</tr>`;
    const body = rows.length ? `<tbody>${rows.map(r => row('td', r)).join('')}</tbody>` : '';
    return { html: `<table><thead>${row('th', head)}</thead>${body}</table>`, next: j };
  }

  // list gathers the items of one list (bullets or numbers, not mixed) from line i. An item's
  // body is every following line indented to its content — blank lines included — plus the
  // lazily wrapped lines of its paragraph; the body is parsed as blocks again, so a nested
  // list, a fence or a quote inside an item is the same code. A list is loose when a blank
  // line separates two of its items: then the items' paragraphs are wrapped.
  function list(lines, i) {
    const first = itemAt(lines[i]);
    const numbered = ordered(first[2]);
    const items = [];
    let loose = false;
    let j = i;
    while (j < lines.length) {
      const m = itemAt(lines[j]);
      if (!m || ordered(m[2]) !== numbered) break;
      const indent = m[1].length + m[2].length + (m[3].length || 1);
      const body = [m[4]];
      let k = j + 1;
      let sawBlank = false;
      for (; k < lines.length; k++) {
        const line = lines[k];
        if (blank(line)) {
          body.push('');
          sawBlank = true;
          continue;
        }
        const lead = line.length - line.trimStart().length;
        if (lead >= indent) {
          body.push(line.slice(indent));
          continue;
        }
        // a wrapped paragraph line continues the item; anything else ends it
        if (!sawBlank && body[body.length - 1] !== '' && !startsBlock(lines, k)) {
          body.push(line);
          continue;
        }
        break;
      }
      while (body.length > 1 && body[body.length - 1] === '') body.pop();
      const following = k < lines.length ? itemAt(lines[k]) : null;
      const next = !!following && ordered(following[2]) === numbered;
      if (sawBlank && next) loose = true;
      items.push(body);
      j = k;
      if (!next) break;
    }
    const li = body => {
      const task = TASK.exec(body[0]);
      const head = task ? (task[1] === ' ' ? '☐ ' : '☑ ') + body[0].slice(task[0].length) : body[0];
      return `<li>${blocks([head, ...body.slice(1)], !loose)}</li>`;
    };
    const start = numbered ? parseInt(first[2], 10) : 1;
    const open = numbered ? `<ol${start !== 1 ? ` start="${start}"` : ''}>` : '<ul>';
    return { html: `${open}${items.map(li).join('')}</${numbered ? 'ol' : 'ul'}>`, next: j };
  }

  // blocks renders a run of lines; `tight` leaves paragraphs unwrapped (a tight list's items)
  function blocks(lines, tight = false) {
    let html = '';
    let para = [];
    const flush = () => {
      if (!para.length) return;
      const text = inline(para.join('\n'));
      html += tight ? text : `<p>${text}</p>`;
      para = [];
    };
    for (let i = 0; i < lines.length; i++) {
      const line = lines[i];
      let m;
      if (blank(line)) {
        flush();
        continue;
      }
      if ((m = FENCE.exec(line))) {
        flush();
        const end = fenceEnd(lines, i + 1, m[1]);
        const lang = m[2] ? ` class="lang-${esc(m[2].replace(/[^\w.+-]/g, ''))}"` : '';
        html += `<pre><code${lang}>${esc(lines.slice(i + 1, end).join('\n'))}\n</code></pre>`;
        i = end;
        continue;
      }
      if ((m = HEADING.exec(line))) {
        flush();
        html += `<h${m[1].length}>${inline(m[2] || '')}</h${m[1].length}>`;
        continue;
      }
      if (RULE.test(line)) {
        flush();
        html += '<hr>';
        continue;
      }
      if (QUOTE.test(line)) {
        flush();
        const inner = [];
        for (; i < lines.length && (m = QUOTE.exec(lines[i])); i++) inner.push(m[1]);
        i--;
        html += `<blockquote>${blocks(inner)}</blockquote>`;
        continue;
      }
      // a numbered list interrupts a paragraph only when it starts at 1 ("2024. A year" is text)
      if ((m = itemAt(line)) && !(para.length && ordered(m[2]) && parseInt(m[2], 10) !== 1)) {
        flush();
        const r = list(lines, i);
        html += r.html;
        i = r.next - 1;
        continue;
      }
      if (tableAt(lines, i)) {
        flush();
        const r = table(lines, i);
        html += r.html;
        i = r.next - 1;
        continue;
      }
      para.push(line);
    }
    flush();
    return html;
  }

  /* ---------- inlines ---------- */

  // link renders a destination: a web link when its scheme is http, https or mailto (checked
  // on the raw destination, control characters and spaces removed, before escaping), else a
  // reference with the destination as its tooltip
  function link(dest, text) {
    const clean = dest.replace(/[\x00-\x20]/g, '');
    if (SCHEME.test(clean)) return anchor(esc(clean), text);
    return `<span class="md-ref" title="${esc(clean)}">${text}</span>`;
  }
  const anchor = (href, text) => `<a href="${href}" target="_blank" rel="noopener noreferrer">${text}</a>`;

  // trimURL drops the trailing punctuation a sentence puts after a bare URL; a closing paren
  // only when the URL does not open it
  function trimURL(url) {
    url = url.replace(/[?!.,:;*_~'"\]]+$/, '');
    while (url.endsWith(')') && url.split('(').length < url.split(')').length) url = url.slice(0, -1);
    return url;
  }

  // codeSpan reads a backtick run closed by a run of the same length; nothing inside is parsed
  function codeSpan(s, i) {
    let n = 0;
    while (s[i + n] === '`') n++;
    for (let j = i + n; j < s.length;) {
      if (s[j] !== '`') {
        j++;
        continue;
      }
      let run = 0;
      while (s[j + run] === '`') run++;
      if (run === n) {
        const code = s.slice(i + n, j).replace(/\n/g, ' ').replace(/^ (.*[^ ].*) $/, '$1');
        return { html: `<code>${esc(code)}</code>`, next: j + n };
      }
      j += run;
    }
    return null;
  }

  // bracketLink parses `[text](destination)` or `![alt](destination)` at i, the bracket; the
  // text is rendered inline without links of its own; a title after the destination is
  // accepted and dropped. Anything that does not parse stays literal text.
  function bracketLink(s, i) {
    let depth = 0;
    let j = i;
    for (; j < s.length; j++) {
      if (s[j] === '\\') {
        j++;
        continue;
      }
      if (s[j] === '[') depth++;
      else if (s[j] === ']' && --depth === 0) break;
    }
    if (j >= s.length || s[j + 1] !== '(') return null;
    let k = j + 2;
    let open = 1;
    for (; k < s.length; k++) {
      if (s[k] === '\\') {
        k++;
        continue;
      }
      if (s[k] === '(') open++;
      else if (s[k] === ')' && --open === 0) break;
    }
    if (k >= s.length) return null;
    const m = DESTINATION.exec(s.slice(j + 2, k).trim());
    if (!m) return null;
    const dest = m[1].replace(/^<|>$/g, '').replace(ESCAPED, '$1');
    return { html: link(dest, inline(s.slice(i + 1, j), true)), next: k + 1 };
  }

  const isSpace = c => c === undefined || /\s/.test(c);
  const isPunct = c => c !== undefined && PUNCT.test(c);

  // inline renders inline Markdown: code spans and links are read in one pass, which records
  // the emphasis delimiter runs; emphasis is resolved from them afterwards
  function inline(s, noLinks = false) {
    const out = [];
    const delims = [];
    let text = '';
    const emit = () => {
      if (text) out.push({ text });
      text = '';
    };
    for (let i = 0; i < s.length;) {
      const c = s[i];
      PLAIN.lastIndex = i;
      const plain = PLAIN.exec(s);
      if (plain) {
        text += esc(plain[0]);
        i += plain[0].length;
        continue;
      }
      if (c === '\\') {
        const n = s[i + 1];
        // a backslash before punctuation escapes it; before a newline it is a hard break
        if (n === '\n') {
          text += '\n';
          i += 2;
          continue;
        }
        if (n !== undefined && ASCII_PUNCT.test(n)) {
          text += esc(n);
          i += 2;
          continue;
        }
        text += '\\';
        i++;
        continue;
      }
      if (c === '`') {
        const r = codeSpan(s, i);
        if (r) {
          emit();
          out.push({ text: r.html });
          i = r.next;
          continue;
        }
        let n = 0;
        while (s[i + n] === '`') n++;
        text += esc(s.slice(i, i + n));
        i += n;
        continue;
      }
      if (!noLinks && (c === '[' || (c === '!' && s[i + 1] === '['))) {
        const r = bracketLink(s, c === '!' ? i + 1 : i);
        if (r) {
          emit();
          out.push({ text: r.html });
          i = r.next;
          continue;
        }
      }
      if (!noLinks && c === '<') {
        const m = AUTOLINK.exec(s.slice(i));
        if (m) {
          emit();
          out.push({ text: anchor(esc(m[1]), esc(m[1])) });
          i += m[0].length;
          continue;
        }
      }
      if (!noLinks && (c === 'h' || c === 'H') && (i === 0 || !/[\w/]/.test(s[i - 1]))) {
        const m = URL.exec(s.slice(i));
        if (m) {
          const url = trimURL(m[0]);
          emit();
          out.push({ text: anchor(esc(url), esc(url)) });
          i += url.length;
          continue;
        }
      }
      if (c === '*' || c === '_' || c === '~') {
        let n = 0;
        while (s[i + n] === c) n++;
        const before = s[i - 1];
        const after = s[i + n];
        const left = !isSpace(after) && (!isPunct(after) || isSpace(before) || isPunct(before));
        const right = !isSpace(before) && (!isPunct(before) || isSpace(after) || isPunct(after));
        // an underscore run inside a word (snake_case) opens and closes nothing
        const canOpen = c === '_' ? left && (!right || isPunct(before)) : left;
        const canClose = c === '_' ? right && (!left || isPunct(after)) : right;
        emit();
        const d = { char: c, len: n, orig: n, canOpen, canClose, before: '', after: '' };
        out.push(d);
        delims.push(d);
        i += n;
        continue;
      }
      text += esc(c);
      i++;
    }
    emit();
    emphasis(delims);
    return out.map(t => t.char ? t.before + esc(t.char.repeat(t.len)) + t.after : t.text).join('');
  }

  // emphasis pairs each closing run with the nearest opener of the same character behind it
  // (CommonMark's rule of three for runs that could do both), two characters at a time for
  // strong and strikethrough, one for emphasis; the runs between a matched pair are used up
  // and whatever is left of a run stays literal
  function emphasis(delims) {
    for (let j = 0; j < delims.length; j++) {
      const closer = delims[j];
      if (!closer.canClose || !closer.len) continue;
      for (let i = j - 1; i >= 0; i--) {
        const opener = delims[i];
        if (opener.char !== closer.char || !opener.canOpen || !opener.len) continue;
        if ((opener.canClose || closer.canOpen) && (opener.orig + closer.orig) % 3 === 0 && !(opener.orig % 3 === 0 && closer.orig % 3 === 0)) continue;
        if (closer.char === '~' && (opener.len < 2 || closer.len < 2)) continue;
        const n = closer.char === '~' ? 2 : Math.min(2, opener.len, closer.len);
        const tag = closer.char === '~' ? 'del' : n === 2 ? 'strong' : 'em';
        opener.after += `<${tag}>`;
        closer.before = `</${tag}>` + closer.before;
        opener.len -= n;
        closer.len -= n;
        for (let k = i + 1; k < j; k++) delims[k].canOpen = delims[k].canClose = false;
        // a closer with characters left looks for another opener
        if (closer.len) j--;
        break;
      }
    }
  }

  // render turns a recorded message into HTML
  function render(text) {
    return blocks(String(text ?? '').replace(/\r\n?/g, '\n').split('\n'));
  }

  return { render };
})();

/* ---------- the prose view ---------- */
// How the app shows a recorded message: rendered by default, the text as recorded when the Raw
// view is on. One switch (`state.rawText`) drives every panel and dialog; each carries the
// same button. Rendered HTML is memoized per text — the conversation panel is drawn again on
// every zoom step — and forgotten when a session is installed (`resetProse`).
const renderedProse = new Map();
function resetProse() {
  renderedProse.clear();
}
// prose is the inner HTML of a prose block for a recorded text
function prose(text) {
  const t = String(text ?? '');
  if (state.rawText) return esc(t);
  let html = renderedProse.get(t);
  if (html === undefined) {
    html = Markdown.render(t);
    renderedProse.set(t, html);
  }
  return html;
}
// proseView is the class that tells the stylesheet which of the two the block holds
const proseView = () => state.rawText ? 'prose-raw' : 'prose-md';
// viewToggle is the button that switches between the two views, on every prose panel
function viewToggle() {
  const title = state.rawText ? 'Render the Markdown' : 'Show the text as recorded';
  return `<button class="btn tiny ghost" data-action="text-view" title="${title}">${state.rawText ? 'Show rendered' : 'Show raw'}</button>`;
}
