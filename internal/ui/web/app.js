// graphmaster ui: search, ego graphs with expand-on-click, file and
// package views, impact highlighting. Plain JavaScript, no build step.
// All text from the API is inserted with textContent, never as HTML.
(function () {
  'use strict';

  const token = new URLSearchParams(location.search).get('token') || '';
  const $ = (id) => document.getElementById(id);
  const MAX_CLIENT_ELEMENTS = 6000; // beyond this, merging views replaces them

  // Current view: {type: 'packages'|'file'|'ego'|'impact', arg}.
  let view = null;
  const expanded = new Set();
  let impactOn = false;

  // ---------- API ----------

  async function api(path, params) {
    const u = new URL(path, location.origin);
    for (const [k, v] of Object.entries(params || {})) {
      if (v !== undefined && v !== null && v !== '') u.searchParams.set(k, String(v));
    }
    u.searchParams.set('token', token);
    let r;
    try {
      r = await fetch(u, { headers: { Accept: 'application/json' }, cache: 'no-store' });
    } catch (e) {
      throw new Error('server unreachable (is `graphmaster ui` still running?)');
    }
    const body = await r.json().catch(() => ({ error: r.status + ' ' + r.statusText }));
    if (!r.ok) throw new Error(body.error || r.statusText);
    if (body.banner) setStatus(body.banner, body.banner.startsWith('⚠') ? 'warn' : '');
    return body;
  }

  function setStatus(text, cls) {
    const s = $('status');
    s.textContent = text;
    s.className = 'status' + (cls ? ' ' + cls : '');
    s.title = text;
  }

  function fail(err) {
    setStatus(String(err && err.message ? err.message : err), 'error');
  }

  // ---------- cytoscape ----------

  function cssVar(name) {
    return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  }

  function buildStyle() {
    const v = {
      fg: cssVar('--fg'), muted: cssVar('--muted'), bg: cssVar('--bg'), border: cssVar('--border'),
      accent: cssVar('--accent'), node: cssVar('--node'), func: cssVar('--func'), type: cssVar('--type'),
      test: cssVar('--test'), file: cssVar('--file'), dir: cssVar('--dir'), edge: cssVar('--edge'),
      seed: cssVar('--seed'), impact: cssVar('--impact'),
    };
    return [
      { selector: 'node', style: {
        label: 'data(label)', color: v.fg, 'font-size': 10, 'text-valign': 'bottom', 'text-margin-y': 3,
        'text-wrap': 'ellipsis', 'text-max-width': 160, 'min-zoomed-font-size': 7,
        'background-color': v.node, width: 16, height: 16,
        'text-background-color': v.bg, 'text-background-opacity': 0.7, 'text-background-padding': 1,
      } },
      { selector: 'node.k-function, node.k-method, node.k-constructor, node.k-macro', style: { 'background-color': v.func } },
      { selector: 'node.k-class, node.k-struct, node.k-interface, node.k-trait, node.k-enum, node.k-type',
        style: { 'background-color': v.type, shape: 'round-rectangle', width: 20, height: 16 } },
      { selector: 'node.k-interface, node.k-trait', style: { shape: 'round-diamond', width: 20, height: 20 } },
      { selector: 'node.test', style: { 'background-color': v.test, shape: 'round-triangle' } },
      { selector: 'node.file', style: {
        shape: 'round-rectangle', 'background-color': v.file, width: 24, height: 18, 'font-size': 10,
      } },
      { selector: 'node.ext', style: { 'background-opacity': 0.5, 'border-width': 1, 'border-style': 'dashed', 'border-color': v.muted } },
      { selector: 'node.group', style: {
        shape: 'round-rectangle', width: 'data(size)', height: 'data(size)', 'font-size': 11,
        'background-color': v.file, label: 'data(caption)', 'text-wrap': 'wrap',
      } },
      { selector: 'node.dir', style: { 'background-color': v.dir } },
      { selector: ':parent', style: {
        'background-color': v.fg, 'background-opacity': 0.04, 'border-width': 1, 'border-color': v.border,
        shape: 'round-rectangle', 'text-valign': 'top', 'text-halign': 'center', 'text-margin-y': -2,
        padding: 10, color: v.muted,
      } },
      { selector: 'node.center', style: { 'border-width': 3, 'border-color': v.accent, width: 24, height: 24, 'font-weight': 'bold' } },
      { selector: 'edge', style: {
        width: 1.2, 'line-color': v.edge, 'target-arrow-color': v.edge, 'target-arrow-shape': 'triangle',
        'arrow-scale': 0.8, 'curve-style': 'bezier', opacity: 0.85,
      } },
      { selector: 'edge.weak', style: { 'line-style': 'dashed' } },
      { selector: 'edge.k-references', style: { 'line-style': 'dotted' } },
      { selector: 'edge.k-extends, edge.k-implements, edge.k-embeds', style: { 'target-arrow-shape': 'triangle-backcurve', width: 1.8 } },
      { selector: 'edge.fileedge, edge.groupedge', style: {
        width: 'mapData(count, 1, 200, 1, 8)', label: 'data(count)', 'font-size': 9, color: v.muted,
        'text-background-color': v.bg, 'text-background-opacity': 0.85, 'curve-style': 'bezier',
      } },
      { selector: 'node.seed', style: { 'background-color': v.seed, 'border-width': 3, 'border-color': v.seed } },
      { selector: 'node.hit', style: { 'background-color': v.impact } },
      { selector: 'node.hit.d1', style: { 'border-width': 3, 'border-color': v.impact } },
      { selector: 'edge.hit', style: { 'line-color': v.impact, 'target-arrow-color': v.impact, opacity: 1, width: 1.8 } },
      { selector: '.faded', style: { opacity: 0.15 } },
      { selector: ':parent.faded', style: { opacity: 0.4 } },
      { selector: 'node:selected', style: { 'border-width': 3, 'border-color': v.accent } },
      { selector: 'edge:selected', style: { 'line-color': v.accent, 'target-arrow-color': v.accent, width: 2.5 } },
    ];
  }

  const cy = cytoscape({
    container: $('graph'),
    style: buildStyle(),
    minZoom: 0.05,
    maxZoom: 4,
    boxSelectionEnabled: false,
  });

  if (window.matchMedia) {
    const mq = window.matchMedia('(prefers-color-scheme: dark)');
    const restyle = () => cy.style(buildStyle());
    if (mq.addEventListener) mq.addEventListener('change', restyle);
  }

  // Adds elements that are not in the graph yet; parents and edge ends
  // that are missing are dropped so the batch stays valid.
  function merge(elements) {
    const present = (id) => cy.getElementById(id).nonempty();
    const batch = new Set();
    const nodes = [];
    const edges = [];
    for (const el of elements) {
      if (present(el.data.id)) continue;
      if (el.group === 'nodes') {
        if (el.data.parent && !present(el.data.parent) && !batch.has(el.data.parent)) delete el.data.parent;
        batch.add(el.data.id);
        nodes.push(el);
      } else {
        edges.push(el);
      }
    }
    const ok = (id) => batch.has(id) || present(id);
    const added = cy.add(nodes.concat(edges.filter((e) => ok(e.data.source) && ok(e.data.target))));
    return added;
  }

  function replace(elements) {
    cy.elements().remove();
    expanded.clear();
    clearImpact();
    const added = merge(elements);
    $('empty').hidden = cy.nodes().length > 0;
    return added;
  }

  function runLayout(kind, opts) {
    const n = cy.nodes().length;
    let layout;
    if (kind === 'concentric') {
      layout = { name: 'concentric', concentric: (node) => 10 - (node.data('depth') || 0), levelWidth: () => 1, minNodeSpacing: 12 };
    } else if (n > 700) {
      layout = { name: 'grid', avoidOverlap: true };
    } else {
      layout = {
        name: 'cose', animate: false, nodeDimensionsIncludeLabels: true, randomize: !(opts && opts.keep),
        idealEdgeLength: () => 70, nodeRepulsion: () => 9000, numIter: n > 300 ? 600 : 1200, padding: 30,
      };
    }
    cy.layout(Object.assign(layout, { fit: true, padding: 30 })).run();
  }

  function truncNote(res) {
    if (res.truncated) setStatus(($('status').textContent || '') + ' · showing first ' + res.elements.length + ' elements', 'warn');
  }

  // ---------- views ----------

  async function showPackages(prefix) {
    const res = await api('/api/packages', { prefix: prefix || '' });
    view = { type: 'packages', arg: res.prefix };
    for (const el of res.elements) {
      if (el.group === 'nodes') {
        const d = el.data;
        d.size = Math.round(22 + 9 * Math.log2(1 + (d.symbols || 0)));
        d.caption = d.label + '\n' + (d.kind === 'dir' ? d.files + ' files · ' : '') + (d.symbols || 0) + ' sym';
      }
    }
    replace(res.elements);
    if (!res.elements.length) showEmpty('Nothing indexed under ' + (res.prefix || 'the repository root') + '.');
    runLayout('cose');
    truncNote(res);
    crumbs(res.prefix, null);
    setHash('packages', res.prefix);
  }

  async function showFile(path) {
    const res = await api('/api/file', { path: path });
    view = { type: 'file', arg: res.file.path };
    replace(res.elements);
    runLayout('cose');
    truncNote(res);
    const dir = res.file.path.includes('/') ? res.file.path.slice(0, res.file.path.lastIndexOf('/') + 1) : '';
    crumbs(dir, res.file.path.slice(dir.length));
    setHash('file', res.file.path);
  }

  async function showEgo(id) {
    const res = await api('/api/ego', egoParams(id));
    view = { type: 'ego', arg: id };
    replace(res.elements);
    expanded.add(String(id));
    runLayout('cose');
    truncNote(res);
    const center = cy.getElementById(res.center);
    if (center.nonempty()) {
      center.select();
      crumbs(dirOf(center.data('path')), center.data('path').slice(dirOf(center.data('path')).length));
    }
    setHash('ego', id);
    showDetails(id);
  }

  // expand merges the neighbourhood of a symbol into the current graph.
  async function expand(id) {
    if (expanded.has(String(id))) return;
    if (cy.elements().length > MAX_CLIENT_ELEMENTS) return showEgo(id);
    const res = await api('/api/ego', Object.assign(egoParams(id), { depth: 1 }));
    expanded.add(String(id));
    const added = merge(res.elements);
    if (impactOn) added.addClass('faded');
    if (added.nonempty()) runLayout('cose', { keep: true });
    truncNote(res);
  }

  function egoParams(id) {
    return { id: id, depth: $('depth').value, group: $('group').checked ? 'file' : '' };
  }

  async function showImpact(params) {
    const res = await api('/api/impact', params);
    const fresh = !view || view.type === 'packages' || cy.elements().length + res.elements.length > MAX_CLIENT_ELEMENTS;
    if (fresh) {
      replace(res.elements);
      view = { type: 'impact', arg: params };
    } else {
      merge(res.elements);
    }
    clearImpact();
    const hit = new Set();
    cy.batch(() => {
      for (const el of res.elements) {
        const e = cy.getElementById(el.data.id);
        if (e.empty()) continue;
        hit.add(el.data.id);
        if (el.group === 'nodes') {
          if (el.classes.includes('seed')) e.addClass('seed');
          else e.addClass('hit d' + Math.min(el.data.depth || 1, 6));
          e.data('depth', el.data.depth || 0);
        } else {
          e.addClass('hit');
        }
      }
      cy.elements().forEach((e) => {
        if (!hit.has(e.id()) && !(e.isNode() && e.isParent())) e.addClass('faded');
      });
    });
    impactOn = true;
    $('btn-clear').hidden = false;
    runLayout(fresh ? 'concentric' : 'cose', { keep: !fresh });
    let msg = 'impact: ' + res.affected + ' symbols in ' + res.files + ' files · ' + res.tests + ' tests · depth ' + res.depth;
    if (res.walkCapped) msg += ' (walk limit reached)';
    if (res.truncated) msg += ' · showing ' + res.elements.length + ' elements';
    setStatus(msg, res.truncated ? 'warn' : '');
  }

  function clearImpact() {
    cy.elements().removeClass('faded hit seed d1 d2 d3 d4 d5 d6');
    impactOn = false;
    $('btn-clear').hidden = true;
  }

  function showEmpty(text) {
    const e = $('empty');
    e.textContent = text;
    e.hidden = false;
  }

  function dirOf(path) {
    return path && path.includes('/') ? path.slice(0, path.lastIndexOf('/') + 1) : '';
  }

  // crumbs renders "root / a/ / b/ / file" links into the package view.
  function crumbs(prefix, leaf) {
    const nav = $('crumbs');
    nav.replaceChildren();
    const link = (text, target) => {
      const a = document.createElement('a');
      a.textContent = text;
      a.addEventListener('click', (ev) => { ev.preventDefault(); showPackages(target).catch(fail); });
      nav.append(a);
    };
    const sep = () => { const s = document.createElement('span'); s.className = 'sep'; s.textContent = '/'; nav.append(s); };
    link('root', '');
    let acc = '';
    for (const part of (prefix || '').split('/').filter(Boolean)) {
      acc += part + '/';
      sep();
      link(part, acc);
    }
    if (leaf) {
      sep();
      const span = document.createElement('span');
      span.textContent = leaf;
      nav.append(span);
    }
  }

  // ---------- details panel ----------

  function el(tag, cls, text) {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined && text !== null) e.textContent = text;
    return e;
  }

  function button(text, onClick) {
    const b = el('button', '', text);
    b.type = 'button';
    b.addEventListener('click', () => Promise.resolve(onClick()).catch(fail));
    return b;
  }

  function symLink(sym) {
    const a = el('a', '', sym.name);
    a.title = sym.qname;
    a.addEventListener('click', () => showDetails(sym.id).catch(fail));
    return a;
  }

  async function showDetails(id) {
    const res = await api('/api/node', { id: id });
    const s = res.symbol;
    $('panel').hidden = false;
    $('p-kind').textContent = s.kind;
    $('p-name').textContent = s.name;
    const body = $('p-body');
    body.replaceChildren();

    body.append(el('div', 'qname', s.qname));
    const loc = el('div', 'loc');
    const fileLink = el('a', '', s.path + ':' + s.start + '-' + s.end);
    fileLink.title = 'Open file view';
    fileLink.addEventListener('click', () => showFile(s.path).catch(fail));
    loc.append(fileLink, document.createTextNode('  ' + s.lang));
    body.append(loc);

    const flags = el('div', 'flags');
    if (s.exported) flags.append(el('span', 'kind', 'exported'));
    if (s.test) flags.append(el('span', 'kind', 'test'));
    if (s.declaration) flags.append(el('span', 'kind', 'declaration'));
    if (flags.childNodes.length) body.append(flags);
    if (s.sig) body.append(el('div', 'sig', s.sig));

    const actions = el('div', 'actions');
    actions.append(
      button('Ego graph', () => showEgo(s.id)),
      button('Expand', () => (cy.getElementById('n' + s.id).nonempty() ? expand(s.id) : showEgo(s.id))),
      button('Impact', () => showImpact({ id: s.id })),
      button('File view', () => showFile(s.path)),
    );
    body.append(actions);

    if (res.parent) {
      body.append(el('h3', '', 'Defined in'));
      const ul = el('ul', 'list');
      const li = el('li');
      li.append(el('span', 'kind', res.parent.kind), document.createTextNode(' '), symLink(res.parent));
      ul.append(li);
      body.append(ul);
    }

    body.append(el('h3', '', 'Edges'));
    const dl = el('dl', 'counts');
    const fmt = (m) => Object.keys(m).sort().map((k) => k + ' ' + m[k]).join(', ') || '—';
    dl.append(el('dt', '', 'incoming'), el('dd', '', fmt(res.in)));
    dl.append(el('dt', '', 'outgoing'), el('dd', '', fmt(res.out)));
    dl.append(el('dt', '', 'unresolved'), el('dd', '', String(res.unresolved)));
    body.append(dl);

    if (res.children.length) {
      body.append(el('h3', '', 'Members (' + (res.children.length + (res.moreChildren || 0)) + ')'));
      const ul = el('ul', 'list');
      for (const c of res.children) {
        const li = el('li');
        li.append(el('span', 'kind', c.kind), document.createTextNode(' '), symLink(c), el('span', 'muted', '  :' + c.start));
        ul.append(li);
      }
      if (res.moreChildren) ul.append(el('li', 'muted', '… +' + res.moreChildren + ' more'));
      body.append(ul);
    }

    body.append(el('h3', '', 'Source'));
    const src = res.source;
    if (!src || src.error) {
      body.append(el('div', 'error', src ? src.error : 'no source'));
    } else {
      const pre = el('pre', 'code');
      src.lines.forEach((line, i) => {
        const row = el('span', 'line');
        row.append(el('span', 'ln', String(src.start + i)), document.createTextNode(line));
        pre.append(row);
      });
      body.append(pre);
      if (src.truncated) body.append(el('div', 'muted', '… source truncated'));
    }

    cy.$(':selected').unselect();
    cy.getElementById('n' + s.id).select();
  }

  // ---------- graph interaction ----------

  cy.on('tap', 'node', (ev) => {
    const d = ev.target.data();
    if (ev.target.hasClass('group')) {
      (d.kind === 'dir' ? showPackages(d.path) : showFile(d.path)).catch(fail);
    } else if (ev.target.hasClass('file')) {
      if (ev.target.hasClass('ext')) showFile(d.path).catch(fail);
    } else if (ev.target.hasClass('sym')) {
      showDetails(d.ref).catch(fail);
      if (view && view.type !== 'file') expand(d.ref).catch(fail);
    }
  });

  cy.on('dbltap', 'node.sym', (ev) => showEgo(ev.target.data('ref')).catch(fail));

  // ---------- search ----------

  const q = $('q');
  const results = $('results');
  let searchSeq = 0;
  let activeIndex = -1;
  let timer = null;

  function closeResults() {
    // Invalidate debounced and in-flight searches so a late response cannot
    // reopen the list after the user picked a result or dismissed it.
    clearTimeout(timer);
    searchSeq++;
    results.hidden = true;
    q.setAttribute('aria-expanded', 'false');
    activeIndex = -1;
  }

  function pick(item) {
    closeResults();
    q.blur();
    if (item.kind === 'file') showFile(item.path).catch(fail);
    else showEgo(item.id).catch(fail);
  }

  async function search(text) {
    const seq = ++searchSeq;
    const res = await api('/api/search', { q: text, limit: 20 });
    if (seq !== searchSeq) return; // a newer query is running
    results.replaceChildren();
    const items = [];
    const head = (t) => results.append(el('li', 'head', t));
    const row = (item, name, where, kind) => {
      const li = el('li');
      li.setAttribute('role', 'option');
      li.append(el('span', 'kind', kind), el('span', 'name', name), el('span', 'where', where));
      li.addEventListener('mousedown', (ev) => { ev.preventDefault(); pick(item); });
      items.push({ li: li, item: item });
      results.append(li);
    };
    if (res.symbols.length) head('Symbols' + (res.more ? ' (more…)' : ''));
    for (const s of res.symbols) row(s, s.name, s.path + ':' + s.start, s.kind);
    if (res.files.length) head('Files');
    for (const f of res.files) row({ kind: 'file', path: f.path }, f.path.slice(f.path.lastIndexOf('/') + 1), f.path, f.lang);
    if (!items.length) results.append(el('li', 'head', 'No matches'));
    results._items = items;
    results.hidden = false;
    q.setAttribute('aria-expanded', 'true');
    activeIndex = -1;
  }

  q.addEventListener('input', () => {
    clearTimeout(timer);
    const text = q.value.trim();
    if (!text) { closeResults(); return; }
    timer = setTimeout(() => search(text).catch(fail), 180);
  });

  q.addEventListener('keydown', (ev) => {
    const items = results._items || [];
    if (ev.key === 'Escape') { closeResults(); return; }
    if (results.hidden || !items.length) return;
    if (ev.key === 'ArrowDown' || ev.key === 'ArrowUp') {
      ev.preventDefault();
      activeIndex = (activeIndex + (ev.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length;
      items.forEach((it, i) => it.li.classList.toggle('active', i === activeIndex));
      items[activeIndex].li.scrollIntoView({ block: 'nearest' });
    } else if (ev.key === 'Enter') {
      ev.preventDefault();
      pick(items[Math.max(activeIndex, 0)].item);
    }
  });

  q.addEventListener('blur', () => setTimeout(closeResults, 120));
  document.addEventListener('keydown', (ev) => {
    if (ev.key === '/' && document.activeElement !== q) { ev.preventDefault(); q.focus(); q.select(); }
  });

  // ---------- toolbar and navigation ----------

  $('btn-packages').addEventListener('click', () => showPackages(view && view.type === 'packages' ? view.arg : '').catch(fail));
  $('btn-fit').addEventListener('click', () => cy.fit(undefined, 30));
  $('btn-clear').addEventListener('click', () => { clearImpact(); });
  $('p-close').addEventListener('click', () => { $('panel').hidden = true; cy.resize(); });
  $('brand').addEventListener('click', (ev) => { ev.preventDefault(); showPackages('').catch(fail); });
  $('depth').addEventListener('change', () => { if (view && view.type === 'ego') showEgo(view.arg).catch(fail); });
  $('group').addEventListener('change', () => { if (view && view.type === 'ego') showEgo(view.arg).catch(fail); });

  // The hash records the view so reloads and back/forward work; the token
  // stays in the query string.
  let hashBusy = false;
  function setHash(type, arg) {
    const h = '#' + type + '=' + encodeURIComponent(arg);
    if (location.hash !== h) {
      hashBusy = true;
      location.hash = h;
    }
  }

  function route() {
    const m = /^#(packages|file|ego)=(.*)$/.exec(location.hash);
    if (!m) return showPackages('');
    const arg = decodeURIComponent(m[2]);
    if (m[1] === 'file') return showFile(arg);
    if (m[1] === 'ego') return showEgo(arg);
    return showPackages(arg);
  }

  window.addEventListener('hashchange', () => {
    if (hashBusy) { hashBusy = false; return; }
    route().catch(fail);
  });

  if (!token) {
    showEmpty('Missing token: open the URL printed by `graphmaster ui`.');
  } else {
    route().catch(fail);
  }
})();
