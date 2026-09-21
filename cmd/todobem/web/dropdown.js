'use strict';
// dropdown.js — the list of every <select class="select">, drawn by the page instead of the
// platform, so it wears the panel's finish. The native select stays the control: it keeps its
// value, renders its own label, sits in the tab order and fires the `change` event the rest of
// the app listens for on `document`; only its popup is replaced, by one shared listbox. While
// the list is open the select's value follows the highlight (assistive tech announces it), and
// `change` fires once, on commit — Enter, Space, a click on an option, tabbing away — and only
// if the value moved. Escape, a click outside, a page scroll or a lost window put the value back
// and fire nothing. A coarse pointer keeps the platform picker: a finger is better served by it.
// Nothing is created at load; the listbox is built on the first open. A select with a
// `data-filter` attribute (its placeholder) gets a search field above its rows: typing — on the
// select or in the field — keeps the rows whose name or host contains the text; the arrows
// move among them, Escape clears the text first and closes second.

const Dropdown = (() => {
  let list = null;
  let owner = null;
  let origin = -1;
  let watcher = null;
  let query = '';

  // optionsOf reads the select's options as the list draws them: the label, and for an option
  // that names a host (data-host, data-name: the project select) the badge and the bare name
  const optionsOf = select => [...select.options].map((o, index) => ({ index, label: o.textContent, disabled: o.disabled, host: o.dataset.host || '', name: o.dataset.name || '' }));

  // nextIndex steps from `from` by `delta` to the next enabled option; at the end it stays put
  function nextIndex(options, from, delta) {
    let i = from;
    for (;;) {
      i += delta;
      if (i < 0 || i >= options.length) return from;
      if (!options[i].disabled) return i;
    }
  }

  // matchIndex finds the next enabled option after `from` whose label starts with `char`,
  // wrapping around; -1 when none does
  function matchIndex(options, from, char) {
    const c = char.toLowerCase();
    for (let step = 1; step <= options.length; step++) {
      const o = options[(from + step) % options.length];
      if (!o.disabled && (o.name || o.label).trim().toLowerCase().startsWith(c)) return o.index;
    }
    return -1;
  }

  // matches: whether a row stays in view for a filter text (its name or its host contains it)
  const matches = (o, q) => !q || (o.name || o.label).toLowerCase().includes(q) || o.host.toLowerCase().includes(q);
  // filtered marks the rows the filter hides as disabled, so the arrows step over them
  const filtered = (options, q) => options.map(o => ({ ...o, disabled: o.disabled || !matches(o, q) }));

  // render draws the rows: `checked` is the value the select had when the list opened, `active`
  // the highlighted row (the value it has now), `q` the filter text (a row it hides is not
  // drawn). When any option names a host, every row gets a badge column of one width (the
  // longest host name), so the names line up under each other.
  const render = (options, checked, active, q = '') => {
    const withHosts = options.some(o => o.host);
    return options.map(o => {
      const cls = o.index === active ? 'dropdown-option active' : 'dropdown-option';
      const disabled = o.disabled ? ' aria-disabled="true"' : '';
      const hidden = matches(o, q) ? '' : ' hidden';
      const text = withHosts ? `<span class="dropdown-host">${o.host ? hostBadge(o.host) : ''}</span><span class="dropdown-text">${esc(o.name || o.label)}</span>` : esc(o.label);
      return `<div class="${cls}" role="option" data-index="${o.index}" aria-selected="${o.index === checked}"${disabled}${hidden}>${text}</div>`;
    }).join('');
  };
  // hostColumn is the badge column's width for a list: the longest host name, in characters
  const hostColumn = options => Math.max(0, ...options.map(o => o.host.length));
  // filterHTML is the search field above the rows of a filterable select
  const filterHTML = placeholder => `<div class="dropdown-filter"><input type="search" class="dropdown-search" placeholder="${esc(placeholder)}" aria-label="${esc(placeholder)}" autocomplete="off" spellcheck="false"></div>`;

  const isSelect = el => !!el && el.tagName === 'SELECT' && el.classList.contains('select') && !el.disabled;
  const enhanced = () => !window.matchMedia || window.matchMedia('(pointer:fine)').matches;
  const optionAt = target => {
    const row = target.closest('.dropdown-option');
    return row && !row.hasAttribute('aria-disabled') ? Number(row.dataset.index) : -1;
  };
  const rows = () => list.querySelector('.dropdown-rows');
  const search = () => list.querySelector('.dropdown-search');
  const filterable = select => !!(select.dataset && select.dataset.filter);

  function ensureList() {
    if (list) return list;
    list = document.createElement('div');
    list.className = 'dropdown';
    list.setAttribute('role', 'listbox');
    list.hidden = true;
    // a press on the list must not take the focus off the select — except into the search field
    list.addEventListener('mousedown', e => {
      if (!e.target.closest('.dropdown-search')) e.preventDefault();
    });
    list.addEventListener('mouseover', e => {
      const i = optionAt(e.target);
      if (i >= 0) highlight(i);
    });
    list.addEventListener('click', e => {
      const i = optionAt(e.target);
      if (i < 0) return;
      highlight(i);
      commit();
    });
    list.addEventListener('input', e => {
      if (e.target.closest('.dropdown-search')) applyFilter(e.target.value);
    });
    // keys in the search field drive the list like keys on the select
    list.addEventListener('keydown', e => {
      if (!owner || !e.target.closest('.dropdown-search')) return;
      if (e.key === 'Escape' && query) {
        e.target.value = '';
        applyFilter('');
        e.preventDefault();
        return;
      }
      if (e.key === 'Tab') {
        const select = owner;
        commit();
        select.focus();
        return;
      }
      if (e.key.length === 1) return; // typed into the field; `input` applies it
      navigate(e);
    });
    list.addEventListener('focusout', e => {
      if (owner && e.target.closest('.dropdown-search') && e.relatedTarget !== owner) commit();
    });
    return list;
  }

  function highlight(i) {
    if (!owner || i < 0) return;
    const all = rows().children;
    if (all[owner.selectedIndex]) all[owner.selectedIndex].classList.remove('active');
    owner.selectedIndex = i;
    if (!all[i]) return;
    all[i].classList.add('active');
    all[i].scrollIntoView({ block: 'nearest' });
  }

  // applyFilter hides the rows the text does not match and keeps the highlight on a visible one
  function applyFilter(text) {
    query = text.trim().toLowerCase();
    const options = optionsOf(owner);
    for (const o of options) rows().children[o.index].hidden = !matches(o, query);
    const shown = filtered(options, query);
    const none = shown.every(o => o.disabled);
    list.querySelector('.dropdown-empty').hidden = !none;
    if (!none && shown[owner.selectedIndex].disabled) highlight(nextIndex(shown, -1, 1));
    place();
  }

  // navigate moves the highlight or commits for a key from the select or its search field
  function navigate(e) {
    const key = e.key;
    const options = filtered(optionsOf(owner), query);
    const at = owner.selectedIndex;
    if (key === 'ArrowDown') highlight(nextIndex(options, at, 1));
    else if (key === 'ArrowUp') highlight(nextIndex(options, at, -1));
    else if (key === 'Home') highlight(nextIndex(options, -1, 1));
    else if (key === 'End') highlight(nextIndex(options, options.length, -1));
    else if (key === 'Enter' || key === ' ') commit();
    else if (key === 'Escape') cancel();
    else if (key.length === 1 && !e.metaKey && !e.ctrlKey && !e.altKey) {
      if (filterable(owner)) {
        // the first typed letter opens the search field with it; later ones land there
        const field = search();
        field.value = key;
        field.focus();
        applyFilter(key);
      } else highlight(matchIndex(options, at, key));
    } else return;
    e.preventDefault();
  }

  // place puts the list under the select, or above it when the room below is short
  function place() {
    const r = owner.getBoundingClientRect();
    const gap = 4;
    list.style.maxHeight = Math.min(360, window.innerHeight - 16) + 'px';
    list.style.minWidth = r.width + 'px';
    list.style.left = Math.max(8, Math.min(r.left, window.innerWidth - list.offsetWidth - 8)) + 'px';
    const below = r.bottom + gap;
    const fits = below + list.offsetHeight <= window.innerHeight - 8;
    list.style.top = (fits ? below : Math.max(8, r.top - gap - list.offsetHeight)) + 'px';
  }

  function open(select) {
    // a select inside a modal dialog lives in the top layer: the list must join it there
    const host = select.closest('dialog') || document.body;
    const l = ensureList();
    if (l.parentNode !== host) host.appendChild(l);
    owner = select;
    origin = select.selectedIndex;
    query = '';
    const options = optionsOf(select);
    const column = hostColumn(options);
    l.classList.toggle('with-hosts', column > 0);
    l.style.setProperty('--host-col', column ? `calc(${column}ch + 14px)` : '0px');
    const filter = filterable(select) ? filterHTML(select.dataset.filter) : '';
    l.innerHTML = `${filter}<div class="dropdown-rows">${render(options, origin, origin)}</div><div class="dropdown-empty" hidden>${esc(FILTER_TEXT.noMatch)}</div>`;
    l.hidden = false;
    select.setAttribute('aria-expanded', 'true');
    place();
    if (rows().children[origin]) rows().children[origin].scrollIntoView({ block: 'nearest' });
    // a re-render that replaces the select while its list is open takes the list with it
    if (typeof MutationObserver === 'function') {
      watcher = new MutationObserver(() => {
        if (owner && !owner.isConnected) cancel();
      });
      watcher.observe(document.body, { childList: true, subtree: true });
    }
  }

  function close(revert) {
    if (!owner) return;
    const select = owner;
    owner = null;
    query = '';
    list.hidden = true;
    select.removeAttribute('aria-expanded');
    if (watcher) watcher.disconnect();
    watcher = null;
    if (revert) select.selectedIndex = origin;
    else if (select.selectedIndex !== origin) select.dispatchEvent(new Event('change', { bubbles: true }));
  }
  const commit = () => close(false);
  const cancel = () => close(true);

  document.addEventListener('mousedown', e => {
    const target = e.target;
    if (owner) {
      if (list.contains(target)) return;
      const same = target === owner;
      cancel();
      if (same) {
        e.preventDefault();
        return;
      }
    }
    if (!isSelect(target) || !enhanced()) return;
    e.preventDefault();
    target.focus();
    open(target);
  });

  document.addEventListener('keydown', e => {
    const target = e.target;
    if (!isSelect(target) || !enhanced()) return;
    const key = e.key;
    if (owner !== target) {
      if (key === 'Enter' || key === ' ' || key === 'ArrowDown' || key === 'ArrowUp') {
        e.preventDefault();
        open(target);
      }
      return;
    }
    if (key === 'Tab') {
      commit();
      return;
    }
    navigate(e);
  });

  document.addEventListener('focusout', e => {
    // the focus moving into the list's search field keeps the list open
    if (owner && e.target === owner && !(e.relatedTarget && list.contains(e.relatedTarget))) commit();
  });
  // the page scrolling under an open list, or the window going away, closes it as a menu would
  document.addEventListener('scroll', e => {
    if (owner && e.target !== list) cancel();
  }, true);
  window.addEventListener('blur', () => cancel());
  window.addEventListener('resize', () => {
    if (owner) place();
  });

  return { optionsOf, nextIndex, matchIndex, render, hostColumn, matches, filtered };
})();
