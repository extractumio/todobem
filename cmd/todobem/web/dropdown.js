'use strict';
// dropdown.js — the list of every <select class="select">, drawn by the page instead of the
// platform, so it wears the panel's finish. The native select stays the control: it keeps its
// value, renders its own label, sits in the tab order and fires the `change` event the rest of
// the app listens for on `document`; only its popup is replaced, by one shared listbox. While
// the list is open the select's value follows the highlight (assistive tech announces it), and
// `change` fires once, on commit — Enter, Space, a click on an option, tabbing away — and only
// if the value moved. Escape, a click outside, a page scroll or a lost window put the value back
// and fire nothing. A coarse pointer keeps the platform picker: a finger is better served by it.
// Nothing is created at load; the listbox is built on the first open.

const Dropdown = (() => {
  let list = null;
  let owner = null;
  let origin = -1;
  let watcher = null;

  // optionsOf reads the select's options as the list draws them
  const optionsOf = select => [...select.options].map((o, index) => ({ index, label: o.textContent, disabled: o.disabled }));

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
      if (!o.disabled && o.label.trim().toLowerCase().startsWith(c)) return o.index;
    }
    return -1;
  }

  // render draws the rows: `checked` is the value the select had when the list opened, `active`
  // the highlighted row (the value it has now)
  const render = (options, checked, active) => options.map(o => {
    const cls = o.index === active ? 'dropdown-option active' : 'dropdown-option';
    const disabled = o.disabled ? ' aria-disabled="true"' : '';
    return `<div class="${cls}" role="option" data-index="${o.index}" aria-selected="${o.index === checked}"${disabled}>${esc(o.label)}</div>`;
  }).join('');

  const isSelect = el => !!el && el.tagName === 'SELECT' && el.classList.contains('select') && !el.disabled;
  const enhanced = () => !window.matchMedia || window.matchMedia('(pointer:fine)').matches;
  const optionAt = target => {
    const row = target.closest('.dropdown-option');
    return row && !row.hasAttribute('aria-disabled') ? Number(row.dataset.index) : -1;
  };

  function ensureList() {
    if (list) return list;
    list = document.createElement('div');
    list.className = 'dropdown';
    list.setAttribute('role', 'listbox');
    list.hidden = true;
    // a press on the list must not take the focus off the select
    list.addEventListener('mousedown', e => e.preventDefault());
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
    return list;
  }

  function highlight(i) {
    if (!owner || i < 0) return;
    const rows = list.children;
    if (rows[owner.selectedIndex]) rows[owner.selectedIndex].classList.remove('active');
    owner.selectedIndex = i;
    if (!rows[i]) return;
    rows[i].classList.add('active');
    rows[i].scrollIntoView({ block: 'nearest' });
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
    l.innerHTML = render(optionsOf(select), origin, origin);
    l.hidden = false;
    select.setAttribute('aria-expanded', 'true');
    place();
    if (l.children[origin]) l.children[origin].scrollIntoView({ block: 'nearest' });
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
    const options = optionsOf(target);
    const at = target.selectedIndex;
    if (key === 'ArrowDown') highlight(nextIndex(options, at, 1));
    else if (key === 'ArrowUp') highlight(nextIndex(options, at, -1));
    else if (key === 'Home') highlight(nextIndex(options, -1, 1));
    else if (key === 'End') highlight(nextIndex(options, options.length, -1));
    else if (key === 'Enter' || key === ' ') commit();
    else if (key === 'Escape') cancel();
    else if (key.length === 1 && !e.metaKey && !e.ctrlKey && !e.altKey) highlight(matchIndex(options, at, key));
    else return;
    e.preventDefault();
  });

  document.addEventListener('focusout', e => {
    if (owner && e.target === owner) commit();
  });
  // the page scrolling under an open list, or the window going away, closes it as a menu would
  document.addEventListener('scroll', e => {
    if (owner && e.target !== list) cancel();
  }, true);
  window.addEventListener('blur', () => cancel());
  window.addEventListener('resize', () => {
    if (owner) place();
  });

  return { optionsOf, nextIndex, matchIndex, render };
})();
