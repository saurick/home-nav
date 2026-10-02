'use strict';

(() => {
  const $ = (selector, root = document) => root.querySelector(selector);
  const $$ = (selector, root = document) => [...root.querySelectorAll(selector)];
  const icons = JSON.parse($('#ui-icon-data').textContent);
  let navigation = JSON.parse($('#navigation-data').textContent);
  const icon = name => icons['mdi:' + name] || '';
  const groupsRoot = $('#groups');
  const pinnedGroup = $('#pinned-group');
  const pinnedGroupID = '@pinned';
  const search = $('#service-search');
  const editForm = $('#edit-form');
  const settingsForm = $('#settings-form');
  const groupForm = $('#group-form');
  const editDialog = $('#edit-dialog');
  const settingsDialog = $('#settings-dialog');
  const groupsDialog = $('#groups-dialog');
  const galleryDialog = $('#gallery-dialog');
  const field = name => editForm.elements.namedItem(name);
  const setting = name => settingsForm.elements.namedItem(name);
  const groupField = name => groupForm.elements.namedItem(name);
  const modes = ['external', 'internal', 'internal_domain'];
  const modeLabels = { external: '外网', internal: '内网 IP', internal_domain: '内网域名' };
  const urlFields = { external: 'external_url', internal: 'internal_url', internal_domain: 'internal_domain_url' };
  const cards = new Map($$('.app-icon', groupsRoot).map(node => [node.dataset.serviceId, node]));
  const groupNodes = new Map($$('.group[data-group-id]', groupsRoot).map(node => [node.dataset.groupId, node]));
  const reducedMotion = matchMedia('(prefers-reduced-motion: reduce)');
  let editMode = false;
  let previousSearch = '';
  let selection = 0;
  let statusServiceID = '';
  let statuses = {};
  let statusUnavailable = false;
  let statusLoading = false;
  let statusRevision = 0;
  let statusTimer;
  let galleryAssets = [];
  let galleryMode = 'browse';
  let galleryFilter = 'all';
  let galleryLoading = false;
  let galleryLoadID = 0;
  let uploadTarget = '';
  let uploadType = 'icon';
  let pendingDelete = null;
  let pendingDiscard = null;
  let toastTimer;
  let sortDirty = false;
  let sortSaving = false;
  let sortPromise = null;
  let dragState = null;
  let groupSortDirty = false;
  let mutationTail = Promise.resolve();
  let suppressClickUntil = 0;
  const baselines = new Map();
  const busyDialogs = new Set();
  const returnFocus = new Map();
  const addressFields = new Map($$('[data-address]', editForm).map(node => [node.dataset.address, node]));

  function readStorage(key, fallback) {
    try { return localStorage.getItem(key) ?? fallback; } catch (_) { return fallback; }
  }
  function writeStorage(key, value) {
    try { localStorage.setItem(key, value); } catch (_) {}
  }
  let accessMode = readStorage('home-nav.access-mode', 'external');
  if (!modes.includes(accessMode)) accessMode = 'external';
  let preferences;
  try { preferences = JSON.parse(readStorage('home-nav.view-preferences', '{}')); } catch (_) { preferences = {}; }
  if (!preferences || typeof preferences !== 'object' || Array.isArray(preferences)) preferences = {};
  preferences.density = preferences.density === 'compact' ? 'compact' : 'comfortable';
  preferences.collapsed = Array.isArray(preferences.collapsed) ? preferences.collapsed.filter(id => typeof id === 'string') : [];
  function savePreferences() { writeStorage('home-nav.view-preferences', JSON.stringify(preferences)); }
  const allServices = () => navigation.groups.flatMap(group => group.services);
  const service = id => allServices().find(item => item.id === id);
  const group = id => navigation.groups.find(item => item.id === id);
  const serviceID = target => target.closest('[data-service-id]')?.dataset.serviceId;

  function preferredEntry(item, mode = accessMode) {
    const order = mode === 'internal' ? ['internal', 'internal_domain', 'external'] : mode === 'internal_domain' ? ['internal_domain', 'internal', 'external'] : ['external', 'internal', 'internal_domain'];
    const type = order.find(candidate => item[urlFields[candidate]]);
    return { type: type || '', url: type ? item[urlFields[type]] : '', fallback: Boolean(type && type !== mode) };
  }
  function entryHint(entry) {
    return entry.fallback ? '未配置' + modeLabels[accessMode] + '地址，使用' + modeLabels[entry.type] + '地址' : modeLabels[entry.type] + '地址';
  }
  function openRedirectHref(url) { return '/open?url=' + encodeURIComponent(url); }
  function openEntryURL(url) {
    if (!url) return showToast('没有可用的访问地址。', true);
    window.open(openRedirectHref(url), '_blank', 'noopener,noreferrer');
  }
  function showToast(message, error = false) {
    clearTimeout(toastTimer);
    $('#toast-message').textContent = message;
    $('#toast').classList.toggle('is-error', error);
    ($$('dialog[open]').at(-1) || document.body).append($('#toast'));
    $('#toast').hidden = false;
    if (!error) toastTimer = setTimeout(() => { $('#toast').hidden = true; }, 3000);
  }
  $('#toast-close').addEventListener('click', () => { clearTimeout(toastTimer); $('#toast').hidden = true; });

  class RequestError extends Error {
    constructor(message, field = '', status = 0) { super(message); this.field = field; this.status = status; }
  }
  function errorText(error, fallback = '操作未完成，请稍后重试。') {
    if (error instanceof RequestError) return error.message;
    console.error(error);
    return fallback;
  }
  async function request(url, options = {}) {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), options.body instanceof FormData ? 60000 : 15000);
    try {
      const init = { method: options.method || 'GET', cache: 'no-store', signal: controller.signal };
      if (options.body instanceof FormData) init.body = options.body;
      else if (options.body !== undefined) { init.headers = { 'Content-Type': 'application/json' }; init.body = JSON.stringify(options.body); }
      const response = await fetch(url, init);
      const payload = await response.json().catch(() => null);
      if (response.status === 401) throw new RequestError('请重新登录后再操作，当前填写内容仍保留在页面中。', '', 401);
      if (!response.ok) throw new RequestError(payload?.error || '操作未完成，请稍后重试。', payload?.field || '', response.status);
      if (!payload) throw new RequestError('暂未收到完整结果，请重新读取列表确认保存状态。');
      return payload;
    } catch (error) {
      if (error instanceof RequestError) throw error;
      throw new RequestError(controller.signal.aborted ? '暂未收到操作结果，请重新读取列表确认后再操作。' : '连接中断，请检查网络并重新读取列表确认保存状态。');
    } finally { clearTimeout(timer); }
  }
  function mutate(url, options) {
    const result = mutationTail.then(() => request(url, options));
    mutationTail = result.catch(() => {});
    return result;
  }
  function applyNavigation(data, render = true) {
    if (!data || !Array.isArray(data.groups)) throw new RequestError('保存结果未能完整读取，请重新读取列表确认。');
    const previous = new Map(allServices().map(item => [item.id, item]));
    for (const item of data.groups.flatMap(item => item.services)) {
      if (JSON.stringify(previous.get(item.id)?.health) !== JSON.stringify(item.health)) { delete statuses[item.id]; statusRevision++; }
    }
    navigation = data;
    const ids = new Set(data.groups.map(item => item.id).concat(pinnedGroupID));
    preferences.collapsed = preferences.collapsed.filter(id => ids.has(id));
    savePreferences();
    if (render) renderNavigation();
    applyAppearance(navigation.appearance);
    refreshStatus();
  }
  async function reloadNavigation() {
    if ((sortDirty || sortSaving) && !(await saveSort())) return;
    try {
      await mutationTail;
      applyNavigation(await mutate('/api/navigation'));
      showToast('列表已更新。');
    } catch (error) { showToast(errorText(error, '列表暂时无法更新，请重试。'), true); }
  }
  function fingerprint(form) {
    return JSON.stringify([...new FormData(form).entries()].filter(([, value]) => typeof value === 'string'));
  }
  function rememberForm(form) { baselines.set(form, fingerprint(form)); }
  function isDirty(dialog) {
    if (dialog === editDialog) return fingerprint(editForm) !== baselines.get(editForm);
    if (dialog === settingsDialog) return fingerprint(settingsForm) !== baselines.get(settingsForm);
    if (dialog === groupsDialog) return groupSortDirty || (!groupForm.hidden && fingerprint(groupForm) !== baselines.get(groupForm));
    return false;
  }
  function updateModalState() {
    const open = $$('dialog[open]');
    document.body.classList.toggle('modal-open', open.length > 0);
    (open.at(-1) || document.body).append($('#toast'));
  }
  function openDialog(dialog, focus) {
    closePopovers();
    if (dialog.open) return;
    returnFocus.set(dialog, document.activeElement);
    dialog.showModal();
    updateModalState();
    (focus || $('[autofocus], input:not([type=hidden]), button', dialog))?.focus({ preventScroll: true });
  }
  function closeDialog(dialog) {
    dialog.close();
    updateModalState();
    if (dialog === settingsDialog) applyAppearance(navigation.appearance);
    if (dialog === editDialog) $('#upload-file').value = '';
    if (dialog === groupsDialog) { groupSortDirty = false; groupForm.hidden = true; renderGroupManager(); }
    const previous = returnFocus.get(dialog);
    if (previous?.isConnected && !previous.disabled && previous.getClientRects().length) previous.focus({ preventScroll: true });
    else {
      const activeDialog = $$('dialog[open]').at(-1);
      const target = activeDialog ? $('button:not(:disabled), input:not(:disabled):not([type=hidden])', activeDialog)
        : previous?.closest('#tools-menu') ? $('#tools-menu-button')
        : !$('#empty-state').hidden ? $('#empty-add-button')
        : search.disabled ? $('#edit-mode-button') : search;
      target?.focus({ preventScroll: true });
    }
  }
  function requestClose(dialog) {
    if (busyDialogs.has(dialog)) return;
    if (!isDirty(dialog)) return closeDialog(dialog);
    pendingDiscard = dialog;
    openDialog($('#discard-dialog'), $('#continue-editing'));
  }
  for (const dialog of $$('dialog')) {
    dialog.addEventListener('cancel', event => { event.preventDefault(); requestClose(dialog); });
    dialog.addEventListener('click', event => {
      if (event.target.closest('[data-close]')) return requestClose(dialog);
      if (event.target !== dialog) return;
      const rect = dialog.getBoundingClientRect();
      if (event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom) requestClose(dialog);
    });
    dialog.addEventListener('close', updateModalState);
  }
  document.addEventListener('keydown', event => {
    if (event.key !== 'Tab') return;
    const dialog = $$('dialog[open]').at(-1);
    if (!dialog) return;
    const focusable = $$('button, a[href], input:not([type=hidden]), select, textarea, summary, [tabindex]', dialog).filter(node => !node.disabled && node.tabIndex >= 0 && node.getClientRects().length);
    const first = focusable[0]; const last = focusable.at(-1);
    if (!first) { event.preventDefault(); dialog.focus({ preventScroll: true }); return; }
    if (event.shiftKey && (document.activeElement === first || !dialog.contains(document.activeElement))) { event.preventDefault(); last?.focus(); }
    else if (!event.shiftKey && (document.activeElement === last || !dialog.contains(document.activeElement))) { event.preventDefault(); first?.focus(); }
  });
  $('#continue-editing').addEventListener('click', () => { closeDialog($('#discard-dialog')); pendingDiscard = null; });
  $('#discard-changes').addEventListener('click', () => {
    const dialog = pendingDiscard;
    closeDialog($('#discard-dialog'));
    pendingDiscard = null;
    if (dialog) closeDialog(dialog);
  });
  window.addEventListener('beforeunload', event => {
    if (sortDirty || busyDialogs.size || $$('dialog[open]').some(isDirty)) { event.preventDefault(); event.returnValue = ''; }
  });

  function clearErrors(form) {
    for (const input of $$('[aria-invalid]', form)) { input.removeAttribute('aria-invalid'); input.removeAttribute('aria-errormessage'); }
    for (const message of $$('.field-error', form)) message.remove();
    $('.form-feedback', form)?.setAttribute('hidden', '');
  }
  function setFieldError(form, name, message, focus = true) {
    const input = form.elements.namedItem(name);
    if (!input) return;
    const container = input.closest('.field');
    if (!container) return;
    let label = $('.field-error', container);
    if (!label) { label = document.createElement('p'); label.className = 'field-error'; label.id = input.id + '-error'; label.setAttribute('role', 'alert'); container.append(label); }
    label.textContent = message;
    input.setAttribute('aria-invalid', 'true');
    input.setAttribute('aria-errormessage', label.id);
    if (focus) {
      for (const details of $$('details', form)) if (details.contains(input)) details.open = true;
      input.focus();
    }
  }
  function formError(form, error) {
    const message = errorText(error, '保存未完成，填写内容已保留，请重试。');
    if (error.field && form.elements.namedItem(error.field)) setFieldError(form, error.field, message);
    const output = $('.form-feedback', form);
    if (output) { output.textContent = message; output.hidden = false; }
    else showToast(message, true);
  }
  async function submit(form, button, workingLabel, action) {
    if (form.dataset.busy === 'true' || busyDialogs.has(form.closest('dialog'))) return;
    const dialog = form.closest('dialog');
    form.dataset.busy = 'true';
    form.setAttribute('aria-busy', 'true');
    try { await busyAction(dialog, button, workingLabel, action, error => formError(form, error)); }
    finally { form.dataset.busy = 'false'; form.removeAttribute('aria-busy'); }
  }
  for (const form of [editForm, settingsForm, groupForm]) form.addEventListener('input', event => {
    event.target.removeAttribute('aria-invalid');
    event.target.removeAttribute('aria-errormessage');
    $('.field-error', event.target.closest('.field') || form)?.remove();
  });

  function imageSource(value) {
    if (/^(https?:\/\/|\/)/.test(value)) return value;
    const match = /^([a-z0-9-]+):([a-z0-9-]+)$/.exec(value);
    return match ? '/.iconify/' + match[1] + '/' + match[2] + '.svg' : '';
  }
  function fallback(item) {
    const span = document.createElement('span');
    span.className = 'icon-fallback';
    span.textContent = item.icon_text || Array.from(item.name || '?')[0].toLocaleUpperCase();
    return span;
  }
  const renderedIcons = new WeakMap();
  function renderIcon(container, item) {
    const previous = renderedIcons.get(container);
    renderedIcons.set(container, item);
    if (previous && previous.icon === item.icon && previous.icon_text === item.icon_text && container.firstChild && (!$('.icon-fallback', container) || previous.name === item.name)) return;
    const src = imageSource(item.icon || '');
    if (!src) return container.replaceChildren(fallback(item));
    const image = new Image();
    image.alt = ''; image.loading = 'lazy'; image.decoding = 'async'; image.width = 42; image.height = 42; image.src = src;
    image.addEventListener('error', () => { if (image.parentNode === container) container.replaceChildren(fallback(renderedIcons.get(container))); });
    container.replaceChildren(image);
  }
  document.addEventListener('error', event => {
    const image = event.target;
    if (!(image instanceof HTMLImageElement)) return;
    const node = image.closest('.app-icon');
    const item = service(node?.dataset.serviceId);
    if (item && image.closest('.icon-button')) image.parentElement.replaceChildren(fallback(item));
  }, true);
  function highlight(node, text, terms) {
    node.replaceChildren();
    if (!terms.length) { node.textContent = text; return; }
    const escaped = terms.map(term => term.replace(/[.*+?^{}\x24()|[\]\\]/g, '\\$&'));
    const regex = new RegExp('(' + escaped.join('|') + ')', 'gi');
    let start = 0;
    for (const match of text.matchAll(regex)) {
      node.append(document.createTextNode(text.slice(start, match.index)));
      const marked = document.createElement('mark'); marked.textContent = match[0]; node.append(marked);
      start = match.index + match[0].length;
    }
    node.append(document.createTextNode(text.slice(start)));
  }
  function renderCard(item, terms) {
    let node = cards.get(item.id);
    if (!node) { node = $('#service-template').content.firstElementChild.cloneNode(true); cards.set(item.id, node); }
    const entry = preferredEntry(item);
    const hint = entryHint(entry);
    node.dataset.serviceId = item.id; node.dataset.groupId = item.group_id;
    node.dataset.internalUrl = item.internal_url; node.dataset.internalDomainUrl = item.internal_domain_url; node.dataset.externalUrl = item.external_url;
    const launcher = $('.icon-button', node);
    const name = $('.app-name', node);
    if (node.dataset.iconValue !== (item.icon || '') || node.dataset.iconText !== (item.icon_text || '') || (!item.icon || $('.icon-fallback', launcher)) && node.dataset.iconName !== item.name || !launcher.firstChild) renderIcon(launcher, item);
    const existingImage = $('img', launcher);
    if (existingImage?.complete && !existingImage.naturalWidth) launcher.replaceChildren(fallback(item));
    node.dataset.iconValue = item.icon || '';
    node.dataset.iconText = item.icon_text || ''; node.dataset.iconName = item.name;
    for (const link of [launcher, name]) { link.href = entry.url || '#'; link.title = item.name + '；' + hint; link.setAttribute('aria-label', item.name + '，' + hint); }
    launcher.id = 'entry-' + item.id;
    highlight(name, item.name, terms);
    const matchingTags = (item.tags || []).filter(tag => terms.some(term => tag.toLocaleLowerCase().includes(term)));
    const tags = $('.match-tags', node);
    tags.hidden = matchingTags.length === 0;
    highlight(tags, matchingTags.join(' · '), terms);
    const pin = $('.pin-button', node);
    pin.setAttribute('aria-pressed', String(Boolean(item.pinned))); pin.setAttribute('aria-label', (item.pinned ? '取消置顶' : '置顶') + item.name);
    $('.drag-handle', node).hidden = !editMode;
    $('.drag-handle', node).setAttribute('aria-label', '拖拽' + item.name + '，也可用方向键调整位置');
    $('.card-more', node).setAttribute('aria-label', item.name + '更多操作');
    const route = $('.entry-route', node);
    route.textContent = (modeLabels[entry.type] || '未设置') + (entry.fallback ? '回退' : '');
    route.classList.toggle('is-fallback', entry.fallback);
    route.setAttribute('aria-label', item.name + '，' + hint + '，查看详情');
    route.title = hint + '；查看入口与状态';
    renderCardStatus(item, node);
    return node;
  }
  function renderNavigation() {
    if (dragState?.active) return;
    const pendingOrder = editMode && (sortDirty || sortSaving) ? sortPayload() : null;
    closePopovers();
    document.body.dataset.density = preferences.density;
    $('#density-button > span:last-child').textContent = '显示密度：' + (preferences.density === 'compact' ? '紧凑' : '舒适');
    const terms = search.value.trim().toLocaleLowerCase().split(/\s+/).filter(Boolean);
    const all = allServices();
    const liveIDs = new Set(all.map(item => item.id));
    for (const [id, node] of cards) if (!liveIDs.has(id)) { node.remove(); cards.delete(id); }
    const liveGroups = new Set(navigation.groups.map(item => item.id));
    for (const [id, node] of groupNodes) if (!liveGroups.has(id)) { node.remove(); groupNodes.delete(id); }
    const matched = new Set(all.filter(item => terms.every(term => (item.name + ' ' + (item.tags || []).join(' ')).toLocaleLowerCase().includes(term))).map(item => item.id));
    const pinned = [];
    for (const item of all) renderCard(item, terms).hidden = !matched.has(item.id);
    for (const item of navigation.groups) {
      let node = groupNodes.get(item.id);
      if (!node) { node = $('#group-template').content.firstElementChild.cloneNode(true); groupNodes.set(item.id, node); }
      node.dataset.groupId = item.id;
      node.id = 'group-' + item.id;
      $('.group-heading h2', node).textContent = item.name;
      const grid = $('.icon-grid', node);
      const shown = item.services.filter(entry => editMode || !entry.pinned);
      for (const entry of item.services) if (entry.pinned && !editMode) pinned.push(entry);
      const shownIDs = new Set(shown.map(entry => entry.id));
      for (const child of [...grid.children]) if (!shownIDs.has(child.dataset.serviceId)) child.remove();
      for (const entry of shown) grid.append(cards.get(entry.id));
      const count = shown.filter(entry => matched.has(entry.id)).length;
      $('.group-count', node).textContent = terms.length ? count + ' / ' + shown.length : shown.length + ' 个入口';
      node.hidden = terms.length ? count === 0 : !editMode && shown.length === 0 && item.services.length > 0;
      for (const button of $$('[data-group-id], [data-action=add-service], [data-action=collapse-group]', node)) button.dataset.groupId = item.id;
      const collapse = $('.collapse-button', node);
      const collapsed = !editMode && !terms.length && preferences.collapsed.includes(item.id);
      collapse.setAttribute('aria-expanded', String(!collapsed)); collapse.setAttribute('aria-label', (collapsed ? '展开' : '折叠') + item.name);
      collapse.hidden = editMode;
      grid.hidden = collapsed;
      $('.group-actions [data-action=add-service]', node).setAttribute('aria-label', '在' + item.name + '新增入口');
      $('.empty-group', node).hidden = collapsed || item.services.length > 0;
      groupsRoot.append(node);
    }
    const pinnedGrid = $('.icon-grid', pinnedGroup);
    pinnedGrid.replaceChildren(...pinned.map(item => cards.get(item.id)));
    const pinnedCount = pinned.filter(item => matched.has(item.id)).length;
    pinnedGroup.hidden = editMode || (terms.length ? pinnedCount === 0 : pinned.length === 0);
    $('.group-count', pinnedGroup).textContent = terms.length ? pinnedCount + ' / ' + pinned.length : pinned.length + ' 个入口';
    const collapsedPinned = !terms.length && preferences.collapsed.includes(pinnedGroupID);
    pinnedGrid.hidden = collapsedPinned;
    $('.collapse-button', pinnedGroup).setAttribute('aria-expanded', String(!collapsedPinned));
    $('.collapse-button', pinnedGroup).setAttribute('aria-label', (collapsedPinned ? '展开' : '折叠') + '常用入口');
    $('#clear-search').hidden = !search.value;
    $('#empty-state').hidden = matched.size > 0;
    $('#empty-title').textContent = terms.length ? '没有匹配的入口' : '暂无服务入口';
    $('#empty-message').textContent = terms.length ? '换个名称或标签试试，或清空搜索查看全部入口。' : '添加一个入口，让常用服务触手可及。';
    $('#empty-add-button').hidden = terms.length > 0;
    $('#empty-clear-button').hidden = terms.length === 0;
    const jumpGroups = navigation.groups.filter(item => editMode || !item.services.length || item.services.some(entry => !entry.pinned));
    const jumpOptions = jumpGroups.map(item => new Option(item.name, item.id));
    if (pinned.length && !editMode) jumpOptions.unshift(new Option('常用入口', pinnedGroupID));
    $('#group-jump-control').hidden = jumpOptions.length < 3;
    const jump = $('#group-jump');
    jump.replaceChildren(new Option('选择分组', ''), ...jumpOptions);
    updateAccessMode();
    updateSelection();
    if (groupsDialog.open && groupForm.hidden && !groupSortDirty) renderGroupManager();
    if ($('#status-dialog').open) renderStatusDetails();
    if (pendingOrder) restoreOrder(pendingOrder);
  }
  function visibleCards() { return $$('.app-icon', groupsRoot).filter(node => !node.hidden && !node.closest('.group').hidden && !node.parentElement.hidden); }
  function updateSelection() {
    const visible = visibleCards();
    selection = visible.length ? Math.min(Math.max(selection, 0), visible.length - 1) : 0;
    for (const node of cards.values()) { node.classList.remove('is-search-selected'); $('.icon-button', node).removeAttribute('aria-current'); }
    const selecting = Boolean(search.value.trim()) && visible.length > 0;
    if (selecting) {
      visible[selection].classList.add('is-search-selected');
      $('.icon-button', visible[selection]).setAttribute('aria-current', 'true');
      search.setAttribute('aria-activedescendant', $('.icon-button', visible[selection]).id);
    } else search.removeAttribute('aria-activedescendant');
    $('#search-summary').textContent = search.value.trim() ? '找到 ' + visible.length + ' 个入口' + (visible.length ? ' · ↑↓选择，Enter 打开' : '') : '';
  }
  search.addEventListener('input', () => { selection = 0; renderNavigation(); });
  function clearSearch() { search.value = ''; selection = 0; renderNavigation(); search.focus({ preventScroll: true }); }
  $('#clear-search').addEventListener('click', clearSearch);
  $('#empty-clear-button').addEventListener('click', clearSearch);
  search.addEventListener('keydown', event => {
    if (event.isComposing) return;
    if (event.key === 'Escape') { event.preventDefault(); return clearSearch(); }
    if (event.key === 'Enter') {
      const selected = visibleCards()[selection];
      if (selected && search.value.trim()) { event.preventDefault(); openEntryURL(preferredEntry(service(selected.dataset.serviceId)).url); }
    }
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      const visible = visibleCards();
      if (!visible.length || !search.value.trim()) return;
      event.preventDefault(); selection = (selection + (event.key === 'ArrowDown' ? 1 : -1) + visible.length) % visible.length;
      updateSelection(); visible[selection].scrollIntoView({ block: 'nearest', behavior: 'instant' });
    }
  });
  document.addEventListener('keydown', event => {
    if (event.isComposing || $$('dialog[open]').length || editMode) return;
    const editable = event.target.closest('input, textarea, select, [contenteditable]');
    if (((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') || (event.key === '/' && !editable && !event.metaKey && !event.ctrlKey && !event.altKey)) { event.preventDefault(); search.focus(); }
  });

  function updateAccessMode() {
    document.body.dataset.accessMode = accessMode;
    $('#access-mode-label').textContent = modeLabels[accessMode] + '优先';
    $('#access-mode-button').setAttribute('aria-label', '优先访问入口，当前' + modeLabels[accessMode] + '优先');
    const services = allServices();
    for (const button of $$('button[data-access-mode]')) {
      button.setAttribute('aria-checked', String(button.dataset.accessMode === accessMode));
      $('.access-mode-count', button).textContent = '已配置 ' + services.filter(item => item[urlFields[button.dataset.accessMode]]).length + '/' + services.length;
    }
  }
  const popovers = [[$('#access-mode-menu'), $('#access-mode-button')], [$('#tools-menu'), $('#tools-menu-button')]];
  function closePopover(menu, trigger, restore = false) {
    menu.hidden = true; trigger?.setAttribute('aria-expanded', 'false');
    if (restore) trigger?.focus({ preventScroll: true });
  }
  function closePopovers() {
    for (const [menu, trigger] of popovers) closePopover(menu, trigger);
    $('#item-menu').hidden = true;
  }
  for (const [menu, trigger] of popovers) {
    const open = last => {
      const wasOpen = !menu.hidden; closePopovers();
      if (wasOpen) return;
      menu.hidden = false; trigger.setAttribute('aria-expanded', 'true');
      positionPopover(menu, trigger);
      const buttons = $$('button', menu);
      (last ? buttons.at(-1) : $('[aria-checked=true]', menu) || buttons[0])?.focus();
    };
    trigger.addEventListener('click', () => open(false));
    trigger.addEventListener('keydown', event => { if (event.key === 'ArrowDown' || event.key === 'ArrowUp') { event.preventDefault(); open(event.key === 'ArrowUp'); } });
    menu.addEventListener('keydown', event => menuKeys(event, menu, () => closePopover(menu, trigger, true)));
    menu.addEventListener('focusout', event => { if (!menu.contains(event.relatedTarget) && event.relatedTarget !== trigger) closePopover(menu, trigger); });
  }
  function positionPopover(menu, trigger) {
    const rect = trigger.getBoundingClientRect();
    menu.style.position = 'fixed';
    menu.style.right = 'auto';
    menu.style.left = Math.max(12, Math.min(rect.right - menu.offsetWidth, innerWidth - menu.offsetWidth - 12)) + 'px';
    menu.style.top = Math.max(12, Math.min(rect.bottom + 8, innerHeight - menu.offsetHeight - 12)) + 'px';
    menu.style.maxHeight = Math.max(120, innerHeight - 24) + 'px';
    menu.style.overflowY = 'auto';
  }
  window.addEventListener('resize', () => { closePopovers(); if (dragState) finishDrag(true); });
  window.addEventListener('scroll', () => {
    if (dragState) return;
    for (const [menu, trigger] of popovers) if (!menu.hidden) positionPopover(menu, trigger);
    if (!$('#item-menu').hidden && menuReturnFocus?.isConnected) positionItemMenu(menuReturnFocus);
  }, { passive: true });
  document.addEventListener('wheel', event => { if (!event.target.closest('.popover')) closePopovers(); }, { passive: true });
  function menuKeys(event, menu, close) {
    const buttons = $$('button:not(:disabled)', menu).filter(node => !node.hidden);
    const index = buttons.indexOf(document.activeElement);
    let next;
    if (event.key === 'Escape') { event.preventDefault(); return close(); }
    if (event.key === 'ArrowDown') next = (index + 1) % buttons.length;
    if (event.key === 'ArrowUp') next = (index - 1 + buttons.length) % buttons.length;
    if (event.key === 'Home') next = 0;
    if (event.key === 'End') next = buttons.length - 1;
    if (next !== undefined) { event.preventDefault(); buttons[next]?.focus(); }
  }
  document.addEventListener('pointerdown', event => {
    if (!event.target.closest('.popover, .access-mode-control, .tools-control, .card-more')) closePopovers();
  });
  $('#access-mode-menu').addEventListener('click', event => {
    const button = event.target.closest('button[data-access-mode]');
    if (!button) return;
    accessMode = button.dataset.accessMode;
    writeStorage('home-nav.access-mode', accessMode);
    renderNavigation();
    $('#access-mode-button').focus({ preventScroll: true });
    showToast(modeLabels[accessMode] + '优先，缺少时使用其他已配置地址。');
  });
  $('#density-button').addEventListener('click', () => {
    preferences.density = preferences.density === 'compact' ? 'comfortable' : 'compact';
    savePreferences(); renderNavigation(); $('#tools-menu-button').focus({ preventScroll: true });
    showToast('已切换为' + (preferences.density === 'compact' ? '紧凑' : '舒适') + '显示。');
  });
  $('#refresh-navigation-button').addEventListener('click', () => { closePopovers(); reloadNavigation(); });
  $('#group-jump').addEventListener('change', event => {
    const id = event.target.value;
    if (!id) return;
    search.value = '';
    preferences.collapsed = preferences.collapsed.filter(value => value !== id);
    savePreferences(); renderNavigation();
    (id === pinnedGroupID ? pinnedGroup : groupNodes.get(id))?.scrollIntoView({ behavior: 'instant', block: 'start' });
  });

  function updateHealthFields(clear = false) {
    const type = field('health_type').value;
    for (const container of $$('[data-health-for]', editForm)) {
      const active = container.dataset.healthFor.split(' ').includes(type);
      container.hidden = !active;
      for (const input of $$('input', container)) {
        input.disabled = !active;
        if (clear && !active && input.name !== 'health_timeout') input.value = '';
      }
    }
  }
  function refreshPreview() {
    const container = $('#service-preview');
    let image = $('.icon-button', container);
    let name = $('.app-name', container);
    if (!image) { image = document.createElement('div'); image.className = 'icon-button'; container.append(image); }
    if (!name) { name = document.createElement('span'); name.className = 'app-name'; container.append(name); }
    renderIcon(image, { name: field('name').value, icon: field('icon').value, icon_text: field('icon_text').value });
    name.textContent = field('name').value || '入口名称';
  }
  async function openService(id = '', groupID = '') {
    if ((sortDirty || sortSaving) && !(await saveSort())) return showToast('请先保存或放弃未完成的排序。', true);
    const item = service(id);
    if (id && !item) return showToast('该入口已被移除，请重新读取列表。', true);
    editForm.reset(); clearErrors(editForm);
    const values = { id, name: item?.name || '', group_id: item?.group_id || groupID || navigation.groups[0]?.id || '', description: item?.description || '', icon: item?.icon || '', icon_text: item?.icon_text || '', external_url: item?.external_url || '', internal_url: item?.internal_url || '', internal_domain_url: item?.internal_domain_url || '', tags: (item?.tags || []).join(', '), notes: item?.notes || '', health_type: item?.health.type || 'disabled', health_url: item?.health.url || '', health_address: item?.health.address || '', health_expect_status: item?.health.expect_status || '', health_timeout: item?.health.timeout || '2s' };
    field('group_id').replaceChildren(...navigation.groups.map(item => new Option(item.name, item.id)));
    for (const [key, value] of Object.entries(values)) field(key).value = value;
    field('pinned').checked = Boolean(item?.pinned);
    const primary = item ? preferredEntry(item).type || accessMode : accessMode;
    $('#primary-address').replaceChildren(addressFields.get(primary));
    $('#secondary-addresses').replaceChildren(...modes.filter(mode => mode !== primary).map(mode => addressFields.get(mode)));
    for (const details of $$('details', editForm)) details.open = false;
    updateHealthFields(); refreshPreview();
    $('#edit-title').textContent = item ? '编辑入口' : '新增入口';
    $('#save-service-button').textContent = item ? '保存入口' : '新增入口';
    $('#delete-service-button').hidden = !item;
    rememberForm(editForm);
    openDialog(editDialog, field('name'));
  }
  function webURL(value) {
    try { return ['http:', 'https:'].includes(new URL(value).protocol); } catch (_) { return false; }
  }
  function positiveDuration(value) {
    return /^(\d+(\.\d+)?(ns|us|µs|μs|ms|s|m|h))+$/.test(value) && /[1-9]/.test(value);
  }
  function validateService() {
    if (!field('name').value.trim()) { setFieldError(editForm, 'name', '请填写入口名称。'); return false; }
    const names = modes.map(mode => urlFields[mode]);
    if (!names.some(name => field(name).value.trim())) { setFieldError(editForm, urlFields[$('#primary-address [data-address]').dataset.address], '请至少填写一个访问地址。'); return false; }
    for (const name of names) if (field(name).value.trim() && !webURL(field(name).value.trim())) { setFieldError(editForm, name, '请输入以 http:// 或 https:// 开头的有效地址。'); return false; }
    if (field('icon').value.trim() && !imageSource(field('icon').value.trim())) { setFieldError(editForm, 'icon', '请输入在线图标名（例如 mdi:nas），或有效的图片地址。'); return false; }
    const type = field('health_type').value;
    if (type === 'http' && !webURL(field('health_url').value.trim())) { setFieldError(editForm, 'health_url', '请填写有效的网页探测地址。'); return false; }
    if (type === 'http' && field('health_expect_status').value && (!Number.isInteger(Number(field('health_expect_status').value)) || Number(field('health_expect_status').value) < 100 || Number(field('health_expect_status').value) > 599)) { setFieldError(editForm, 'health_expect_status', '请输入 100–599 之间的状态码。'); return false; }
    if (type === 'tcp' && !/^\S+:\d+$/.test(field('health_address').value.trim())) { setFieldError(editForm, 'health_address', '请填写地址与端口，例如 192.168.1.10:443。'); return false; }
    if (type !== 'disabled' && !positiveDuration(field('health_timeout').value.trim() || '2s')) { setFieldError(editForm, 'health_timeout', '请输入大于零的超时时间，例如 2s 或 500ms。'); return false; }
    return true;
  }
  function servicePayload() {
    const type = field('health_type').value;
    const result = {};
    for (const name of ['name', 'description', 'icon_text', 'icon', 'external_url', 'internal_url', 'internal_domain_url', 'group_id', 'notes']) result[name] = field(name).value.trim();
    result.tags = field('tags').value.split(/[,，]/).map(value => value.trim()).filter(Boolean);
    result.pinned = field('pinned').checked;
    result.health = { type, url: type === 'http' ? field('health_url').value.trim() : '', address: type === 'tcp' ? field('health_address').value.trim() : '', expect_status: type === 'http' ? Number(field('health_expect_status').value || 200) : 0, timeout: type === 'disabled' ? '2s' : field('health_timeout').value.trim() || '2s' };
    return result;
  }
  let previewFrame;
  editForm.addEventListener('input', event => {
    if (!['name', 'icon', 'icon_text'].includes(event.target.name)) return;
    cancelAnimationFrame(previewFrame);
    previewFrame = requestAnimationFrame(refreshPreview);
  });
  field('health_type').addEventListener('change', () => updateHealthFields(true));
  $('#use-entry-for-health').addEventListener('click', () => {
    const entry = preferredEntry(servicePayload());
    if (!entry.url) return setFieldError(editForm, urlFields[accessMode], '请先填写一个访问地址。');
    field('health_url').value = entry.url;
  });
  editForm.addEventListener('submit', event => {
    event.preventDefault(); clearErrors(editForm);
    if (!validateService()) return;
    const id = field('id').value; const payload = servicePayload();
    submit(editForm, $('#save-service-button'), '正在保存…', async () => {
      const result = await mutate(id ? '/api/services/' + encodeURIComponent(id) : '/api/services', { method: id ? 'PUT' : 'POST', body: payload });
      applyNavigation(result.navigation); rememberForm(editForm); closeDialog(editDialog);
      showToast(id ? '入口已保存。' : '入口已新增。');
    });
  });
  $('#delete-service-button').addEventListener('click', () => openDelete('service', field('id').value, service(field('id').value)?.name || field('name').value));

  function appearanceStyle(target, appearance, fixed = false) {
    target.style.backgroundColor = appearance.background_color || '#000000';
    const alpha = { low: .18, medium: .30, high: .42 }[appearance.background_overlay] || .30;
    target.style.backgroundImage = appearance.background_image ? 'linear-gradient(rgba(0,0,0,' + alpha + '),rgba(0,0,0,' + alpha + ')),url(' + JSON.stringify(appearance.background_image) + ')' : '';
    target.style.backgroundSize = appearance.background_image ? 'cover' : '';
    target.style.backgroundPosition = appearance.background_image ? 'center' : '';
    target.style.backgroundAttachment = fixed && !matchMedia('(max-width: 760px)').matches ? 'fixed' : 'scroll';
  }
  function applyAppearance(appearance, committed = true) {
    appearanceStyle(document.body, appearance, true);
    if (committed) {
      document.body.dataset.backgroundColor = appearance.background_color;
      document.body.dataset.backgroundImage = appearance.background_image;
      document.body.dataset.backgroundOverlay = appearance.background_overlay;
    }
  }
  function draftAppearance() {
    return { background_color: setting('background_color').value.trim(), background_image: setting('background_image').value.trim(), background_overlay: setting('background_overlay').value };
  }
  function previewSettings() {
    const draft = draftAppearance();
    appearanceStyle($('#settings-preview'), draft);
    applyAppearance(draft, false);
    if (/^#(?:[0-9a-f]{3}|[0-9a-f]{6})$/i.test(draft.background_color)) setting('background_color_picker').value = draft.background_color.length === 4 ? '#' + [...draft.background_color.slice(1)].map(value => value + value).join('') : draft.background_color;
  }
  function openSettings() {
    clearErrors(settingsForm);
    for (const [name, value] of Object.entries(navigation.appearance)) setting(name).value = value;
    setting('background_color_picker').value = navigation.appearance.background_color;
    previewSettings(); rememberForm(settingsForm);
    openDialog(settingsDialog, setting('background_color'));
  }
  $('#open-settings-button').addEventListener('click', openSettings);
  settingsForm.addEventListener('input', event => {
    if (event.target.name === 'background_color_picker') setting('background_color').value = event.target.value;
    previewSettings();
  });
  setting('background_overlay').addEventListener('change', previewSettings);
  $('#reset-background-button').addEventListener('click', () => {
    setting('background_color').value = '#000000'; setting('background_image').value = ''; setting('background_overlay').value = 'medium'; previewSettings();
  });
  settingsForm.addEventListener('submit', event => {
    event.preventDefault(); clearErrors(settingsForm);
    const payload = draftAppearance();
    if (!/^#(?:[0-9a-f]{3}|[0-9a-f]{6})$/i.test(payload.background_color)) return setFieldError(settingsForm, 'background_color', '请输入有效颜色值，例如 #18212b。');
    if (payload.background_image && !payload.background_image.startsWith('/uploads/') && !webURL(payload.background_image)) return setFieldError(settingsForm, 'background_image', '请选择图库图片，或填写有效的图片地址。');
    submit(settingsForm, $('button[type=submit]', settingsForm), '正在保存…', async () => {
      const result = await mutate('/api/settings', { method: 'PUT', body: payload });
      applyNavigation(result.navigation); rememberForm(settingsForm); closeDialog(settingsDialog); showToast('页面设置已保存。');
    });
  });

  function renderGroupManager() {
    const list = $('#group-list');
    const order = groupSortDirty ? $$('.group-row', list).map(node => node.dataset.groupId) : navigation.groups.map(item => item.id);
    const ordered = order.concat(navigation.groups.map(item => item.id).filter(id => !order.includes(id)));
    list.replaceChildren();
    for (const id of ordered) {
      const item = group(id);
      if (!item) continue;
      const row = document.createElement('article'); row.className = 'group-row'; row.dataset.groupId = id;
      const info = document.createElement('div'); const title = document.createElement('h3'); title.className = 'group-row-title'; title.textContent = item.name;
      const meta = document.createElement('div'); meta.className = 'group-row-meta'; meta.textContent = item.services.length + ' 个入口';
      info.append(title, meta); row.append(info);
      const actions = document.createElement('div'); actions.className = 'group-row-actions';
      const commands = [['move-group-up', 'arrow-up', '上移'], ['move-group-down', 'arrow-down', '下移'], ['add-to-group', 'plus', '添加入口'], ['edit-group', 'pencil-box-outline', '重命名'], ['delete-group', 'trash-can-outline', '删除空分组']];
      for (const [action, image, text] of commands) {
        const button = document.createElement('button'); button.type = 'button'; button.className = 'icon-action'; button.dataset.action = action; button.innerHTML = icon(image); button.setAttribute('aria-label', text + item.name); button.title = text;
        if (action === 'delete-group' && (item.services.length || navigation.groups.length === 1)) {
          button.disabled = true; button.title = item.services.length ? '请先移动或移除该分组中的入口' : '至少需要保留一个分组';
        }
        actions.append(button);
      }
      row.append(actions); list.append(row);
    }
    updateGroupMoveButtons();
    $('#save-group-sort-button').disabled = !groupSortDirty;
  }
  function updateGroupMoveButtons() {
    const rows = $$('.group-row', $('#group-list'));
    rows.forEach((row, index) => { $('[data-action=move-group-up]', row).disabled = index === 0; $('[data-action=move-group-down]', row).disabled = index === rows.length - 1; });
  }
  function openGroupForm(id = '') {
    if (!groupForm.hidden && fingerprint(groupForm) !== baselines.get(groupForm)) return showToast('请先保存或取消当前分组名称的修改。', true);
    groupForm.reset(); clearErrors(groupForm); groupField('id').value = id; groupField('name').value = group(id)?.name || '';
    groupForm.hidden = false; rememberForm(groupForm); groupField('name').focus();
  }
  $('#open-groups-button').addEventListener('click', async () => {
    if ((sortDirty || sortSaving) && !(await saveSort())) return;
    groupSortDirty = false; groupForm.hidden = true; renderGroupManager(); openDialog(groupsDialog, $('#add-group-button'));
  });
  $('#add-group-button').addEventListener('click', () => openGroupForm());
  $('#cancel-group-button').addEventListener('click', () => { groupForm.reset(); groupForm.hidden = true; clearErrors(groupForm); $('#add-group-button').focus(); });
  groupForm.addEventListener('submit', event => {
    event.preventDefault(); clearErrors(groupForm);
    const name = groupField('name').value.trim(); const id = groupField('id').value;
    if (!name) return setFieldError(groupForm, 'name', '请填写分组名称。');
    submit(groupForm, $('button[type=submit]', groupForm), '正在保存…', async () => {
      const result = await mutate(id ? '/api/groups/' + encodeURIComponent(id) : '/api/groups', { method: id ? 'PUT' : 'POST', body: { name } });
      applyNavigation(result.navigation); groupForm.hidden = true; rememberForm(groupForm); renderGroupManager(); $('#add-group-button').focus();
      showToast(id ? '分组名称已保存。' : '分组已新增，可以直接添加第一个入口。');
    });
  });
  $('#group-list').addEventListener('click', event => {
    const button = event.target.closest('button[data-action]');
    if (!button || busyDialogs.has(groupsDialog)) return;
    const row = button.closest('.group-row'); const id = row.dataset.groupId;
    if (button.dataset.action === 'edit-group') return openGroupForm(id);
    if (button.dataset.action === 'delete-group') return openDelete('group', id, group(id).name);
    if (button.dataset.action === 'add-to-group') {
      if (isDirty(groupsDialog)) return showToast('请先保存或取消分组名称及顺序的修改。', true);
      closeDialog(groupsDialog); return openService('', id);
    }
    if (button.dataset.action === 'move-group-up' && row.previousElementSibling) row.previousElementSibling.before(row);
    if (button.dataset.action === 'move-group-down' && row.nextElementSibling) row.nextElementSibling.after(row);
    groupSortDirty = JSON.stringify($$('.group-row', $('#group-list')).map(node => node.dataset.groupId)) !== JSON.stringify(navigation.groups.map(item => item.id));
    $('#save-group-sort-button').disabled = !groupSortDirty;
    updateGroupMoveButtons(); button.focus();
  });
  $('#save-group-sort-button').addEventListener('click', () => {
    if (!groupSortDirty) return;
    const ids = $$('.group-row', $('#group-list')).map(node => node.dataset.groupId);
    busyAction(groupsDialog, $('#save-group-sort-button'), '正在保存…', async () => {
      const result = await mutate('/api/groups/sort', { method: 'PUT', body: { group_ids: ids } });
      groupSortDirty = false; applyNavigation(result.navigation); renderGroupManager(); showToast('分组顺序已保存。');
    });
  });

  async function busyAction(dialog, button, label, action, onError = error => showToast(errorText(error), true)) {
    if (busyDialogs.has(dialog)) return;
    const previousLabel = button.innerHTML;
    const controls = $$('button, input, select, textarea', dialog);
    const disabled = new Map(controls.map(control => [control, control.disabled]));
    const previousFocus = document.activeElement;
    busyDialogs.add(dialog); dialog.setAttribute('aria-busy', 'true');
    for (const control of controls) control.disabled = true;
    dialog.tabIndex = -1; dialog.focus({ preventScroll: true });
    button.textContent = label;
    let failure;
    try { await action(); } catch (error) { failure = error; }
    finally {
      for (const control of controls) control.disabled = disabled.get(control);
      button.innerHTML = previousLabel; busyDialogs.delete(dialog); dialog.removeAttribute('aria-busy');
      if (dialog === groupsDialog) { updateGroupMoveButtons(); $('#save-group-sort-button').disabled = !groupSortDirty; }
      if (dialog.open && (document.activeElement === dialog || !dialog.contains(document.activeElement))) {
        const focus = previousFocus?.isConnected && !previousFocus.disabled && previousFocus.getClientRects().length ? previousFocus : $('button:not(:disabled)', dialog);
        focus?.focus({ preventScroll: true });
      }
    }
    if (failure) onError(failure);
  }

  async function openDelete(type, id, name) {
    if (!id) return;
    if ((sortDirty || sortSaving) && !(await saveSort())) return;
    pendingDelete = { type, id, name };
    const labels = { service: '导航入口', group: '空分组', asset: '图片资源' };
    $('#delete-title').textContent = '删除' + labels[type];
    $('#delete-description').textContent = '确定删除“' + name + '”吗？';
    $('#delete-note').textContent = type === 'asset' ? '这张图片会从图库中删除。正在被入口或背景使用的图片不能删除。' : type === 'group' ? '只删除这个空分组。含有入口的分组需要先移动或移除入口。' : '只从导航配置移除入口，不会删除、停止或重启真实服务。';
    $('#delete-feedback').hidden = true;
    openDialog($('#delete-dialog'), $('[data-close]', $('#delete-dialog')));
  }
  $('#confirm-delete-button').addEventListener('click', () => {
    if (!pendingDelete) return;
    const { type, id } = pendingDelete;
    busyAction($('#delete-dialog'), $('#confirm-delete-button'), '正在删除…', async () => {
      const url = type === 'asset' ? '/api/assets?url=' + encodeURIComponent(id) : (type === 'group' ? '/api/groups/' : '/api/services/') + encodeURIComponent(id);
      const result = await mutate(url, { method: 'DELETE' });
      if (type !== 'asset') applyNavigation(result.navigation);
      closeDialog($('#delete-dialog')); pendingDelete = null;
      if (type === 'service' && field('id').value === id && editDialog.open) closeDialog(editDialog);
      if (type === 'asset') await loadGallery();
      if (type === 'group') renderGroupManager();
      showToast('已删除。');
    }, error => { $('#delete-feedback').textContent = errorText(error, '删除未完成，请重试。'); $('#delete-feedback').hidden = false; });
  });

  function payloadFor(item) {
    const payload = { ...item, health: { ...item.health }, tags: [...(item.tags || [])] };
    delete payload.id;
    return payload;
  }
  async function togglePin(id, button) {
    if (button?.disabled) return;
    if ((sortDirty || sortSaving) && !(await saveSort())) return showToast('请先保存或放弃未完成的排序。', true);
    const item = service(id);
    if (!item) return;
    if (button) button.disabled = true;
    try {
      const payload = payloadFor(item); payload.pinned = !item.pinned;
      const result = await mutate('/api/services/' + encodeURIComponent(id), { method: 'PUT', body: payload });
      applyNavigation(result.navigation);
      if (button) button.disabled = false;
      const collapse = button?.closest('.group')?.querySelector('.collapse-button');
      (button?.getClientRects().length ? button : collapse?.getClientRects().length ? collapse : $('.drag-handle', cards.get(id)))?.focus({ preventScroll: true });
      showToast(payload.pinned ? '已固定到常用入口。' : '已取消置顶。');
    } catch (error) { showToast(errorText(error, '置顶未保存，请重试。'), true); }
    finally { if (button) button.disabled = false; }
  }
  let menuServiceID = '';
  let menuReturnFocus;
  function menuButton(action, text, image, url = '') {
    const button = document.createElement('button'); button.type = 'button'; button.setAttribute('role', 'menuitem'); button.tabIndex = -1; button.dataset.action = action; button.dataset.url = url; button.innerHTML = icon(image);
    const label = document.createElement('span'); label.textContent = text; button.append(label); button.setAttribute('aria-label', text);
    return button;
  }
  function openItemMenu(id, anchor, point) {
    const item = service(id); if (!item) return;
    closePopovers(); menuServiceID = id; menuReturnFocus = anchor;
    const menu = $('#item-menu'); menu.replaceChildren();
    const title = document.createElement('p'); title.className = 'menu-heading'; title.textContent = item.name; menu.append(title);
    const missing = [];
    for (const type of modes) {
      const url = item[urlFields[type]];
      if (!url) { missing.push(modeLabels[type]); continue; }
      const row = document.createElement('div'); row.className = 'menu-entry';
      const label = document.createElement('span'); label.className = 'menu-entry-label'; label.textContent = modeLabels[type];
      const address = document.createElement('small'); address.textContent = new URL(url).host + new URL(url).pathname; address.title = url; label.append(address);
      const open = menuButton('open', '打开' + modeLabels[type] + '地址', 'open-in-new', url); const copy = menuButton('copy', '复制' + modeLabels[type] + '地址', 'content-copy', url);
      $('span:last-child', open).className = 'sr-only'; $('span:last-child', copy).className = 'sr-only';
      row.append(label, open, copy); menu.append(row);
    }
    if (missing.length) { const note = document.createElement('p'); note.className = 'popover-note'; note.textContent = '未设置：' + missing.join('、'); menu.append(note); }
    const divider = document.createElement('div'); divider.className = 'menu-separator'; menu.append(divider);
    menu.append(menuButton('pin', item.pinned ? '取消置顶' : '固定到常用入口', 'star-outline'), menuButton('edit', '编辑入口', 'pencil-box-outline'), menuButton('delete', '删除导航入口', 'trash-can-outline'));
    $('[data-action=delete]', menu).classList.add('menu-danger');
    menu.hidden = false; menu.removeAttribute('style');
    positionItemMenu(anchor, point);
    $('button', menu)?.focus({ preventScroll: true });
  }
  function positionItemMenu(anchor, point) {
    const menu = $('#item-menu');
    if (!matchMedia('(max-width: 760px)').matches) {
      const rect = anchor.getBoundingClientRect(); const left = point?.x ?? rect.right; const top = point?.y ?? rect.bottom + 8;
      menu.style.left = Math.max(12, Math.min(left, innerWidth - menu.offsetWidth - 12)) + 'px';
      menu.style.top = Math.max(12, Math.min(top, innerHeight - menu.offsetHeight - 12)) + 'px';
    }
  }
  function closeItemMenu() { $('#item-menu').hidden = true; menuReturnFocus?.focus({ preventScroll: true }); }
  $('#item-menu').addEventListener('keydown', event => menuKeys(event, $('#item-menu'), closeItemMenu));
  $('#item-menu').addEventListener('focusout', event => { if (!$('#item-menu').contains(event.relatedTarget)) $('#item-menu').hidden = true; });
  $('#item-menu').addEventListener('click', event => {
    const button = event.target.closest('button[data-action]'); if (!button) return;
    const item = service(menuServiceID); if (!item) return closeItemMenu();
    const action = button.dataset.action; closeItemMenu();
    if (action === 'open') openEntryURL(button.dataset.url);
    if (action === 'copy') copyText(button.dataset.url);
    if (action === 'pin') togglePin(item.id, $('.pin-button', cards.get(item.id)));
    if (action === 'edit') openService(item.id);
    if (action === 'delete') openDelete('service', item.id, item.name);
  });

  async function copyText(value) {
    if (!value) return;
    if (navigator.clipboard?.writeText && window.isSecureContext) {
      try { await navigator.clipboard.writeText(value); return showToast('地址已复制。'); } catch (_) {}
    }
    const previous = document.activeElement;
    const textarea = document.createElement('textarea'); textarea.value = value; textarea.readOnly = true; textarea.style.position = 'fixed'; textarea.style.opacity = '0';
    ($$('dialog[open]').at(-1) || document.body).append(textarea); textarea.focus(); textarea.select();
    let copied = false;
    try { copied = document.execCommand('copy'); } catch (_) {}
    textarea.remove(); previous?.focus({ preventScroll: true });
    if (copied) return showToast('地址已复制。');
    $('#manual-copy').value = value; openDialog($('#copy-dialog'), $('#manual-copy')); $('#manual-copy').select();
  }

  const statusLabels = { healthy: '探测正常', unhealthy: '探测异常', unknown: '等待探测', disabled: '未监测' };
  function renderCardStatus(item, node) {
    const dot = $('.health-dot', node); const result = statuses[item.id]?.status || 'unknown';
    dot.hidden = item.health.type === 'disabled';
    dot.dataset.status = statusUnavailable ? 'unknown' : result;
    const hint = statusUnavailable ? '状态暂时无法更新，可查看最近记录' : (statusLabels[result] || '等待探测');
    dot.title = hint; dot.setAttribute('aria-label', item.name + '，' + hint + '，查看详情');
  }
  async function refreshStatus() {
    if (statusLoading || document.hidden) return;
    statusLoading = true;
    const revision = statusRevision;
    try {
      const payload = await request('/api/status');
      if (!payload.services || typeof payload.services !== 'object') throw new RequestError('状态暂时无法读取。');
      if (revision === statusRevision) { statuses = payload.services; statusUnavailable = false; }
    } catch (_) { statusUnavailable = true; }
    finally {
      statusLoading = false;
      for (const item of allServices()) if (cards.has(item.id)) renderCardStatus(item, cards.get(item.id));
      if ($('#status-dialog').open) renderStatusDetails();
      if (revision !== statusRevision) refreshStatus();
    }
  }
  function renderStatusDetails() {
    const item = service(statusServiceID); if (!item) return closeDialog($('#status-dialog'));
    const entry = preferredEntry(item); const status = statuses[item.id]; const disabled = item.health.type === 'disabled';
    $('#status-title').textContent = item.name;
    $('#status-entry').textContent = entryHint(entry);
    $('#status-entry-url').textContent = entry.url || '未设置';
    $('#status-result').textContent = disabled ? '未监测' : statusUnavailable ? '暂时无法更新，保留最近记录' : statusLabels[status?.status || 'unknown'] || '等待探测';
    $('#status-target').textContent = disabled ? '未设置探测' : item.health.type === 'tcp' ? '端口连接：' + item.health.address : '网页响应：' + item.health.url;
    const time = status?.checked_at ? new Date(status.checked_at) : null;
    $('#status-time').textContent = time && Number.isFinite(time.getTime()) ? time.toLocaleString() + ' · ' + (statusLabels[status.status] || '等待探测') : '暂无检查记录';
    $('#status-open').href = entry.url || '#'; $('#status-open').dataset.url = entry.url;
    $('#status-copy').disabled = !entry.url;
  }
  $('#status-open').addEventListener('click', event => { if (!event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey) { event.preventDefault(); openEntryURL(event.currentTarget.dataset.url); } });
  $('#status-copy').addEventListener('click', () => copyText(preferredEntry(service(statusServiceID)).url));

  function formatBytes(size) {
    if (size < 1024) return size + ' B';
    if (size < 1024 * 1024) return Math.round(size / 1024) + ' KB';
    return (size / 1024 / 1024).toFixed(1) + ' MB';
  }
  function selectedAssetURL() {
    return galleryMode === 'icon' ? field('icon').value : galleryMode === 'background' ? setting('background_image').value : '';
  }
  function renderGallery() {
    const query = $('#gallery-search').value.trim().toLocaleLowerCase();
    const visible = galleryAssets.filter(asset => (galleryFilter === 'all' || asset.type === galleryFilter) && (asset.name || asset.url).toLocaleLowerCase().includes(query));
    const grid = $('#gallery-grid'); grid.replaceChildren();
    $('#gallery-empty').hidden = visible.length > 0;
    $('#gallery-message').textContent = galleryAssets.length ? '没有匹配的图片，试试其他名称或分类。' : '图库还没有图片，可以先上传一张。';
    $('#retry-gallery').hidden = true;
    $('#gallery-summary').textContent = visible.length + ' 张图片' + (selectedAssetURL() ? ' · 点击图片选用，选入后仍需保存' : '');
    for (const asset of visible) {
      const card = document.createElement('article'); card.className = 'gallery-card'; card.dataset.url = asset.url;
      const selected = selectedAssetURL() === asset.url; card.classList.toggle('is-selected', selected);
      const thumb = document.createElement('button'); thumb.type = 'button'; thumb.className = 'gallery-thumb'; thumb.dataset.action = 'select-asset';
      thumb.setAttribute('aria-label', (galleryMode === 'browse' ? '预览' : '选择') + '图片 ' + (asset.name || asset.url)); thumb.setAttribute('aria-pressed', String(selected));
      const image = new Image(); image.alt = ''; image.loading = 'lazy'; image.decoding = 'async'; image.src = asset.url;
      image.addEventListener('error', () => { image.remove(); const text = document.createElement('span'); text.className = 'image-error'; text.textContent = '图片无法预览'; thumb.prepend(text); });
      thumb.append(image);
      if (selected) { const mark = document.createElement('span'); mark.className = 'selection-mark'; mark.innerHTML = icon('check'); thumb.append(mark); }
      card.append(thumb);
      const body = document.createElement('div'); body.className = 'gallery-body';
      const name = document.createElement('p'); name.className = 'gallery-name'; name.textContent = asset.name || asset.url; name.title = name.textContent;
      const meta = document.createElement('div'); meta.className = 'gallery-meta';
      meta.textContent = (asset.type === 'wallpaper' ? '壁纸' : '图标') + ' · ' + formatBytes(Number(asset.size || 0)) + (asset.width && asset.height ? ' · ' + asset.width + '×' + asset.height : '');
      const used = Array.isArray(asset.used_by) && asset.used_by.length > 0;
      if (used) { const label = document.createElement('span'); label.className = 'gallery-used'; label.textContent = '使用中'; label.title = asset.used_by.join('、'); meta.append(label); }
      const actions = document.createElement('div'); actions.className = 'gallery-actions';
      for (const [action, image, label] of [['copy-asset', 'content-copy', '复制图片地址'], ['delete-asset', 'trash-can-outline', used ? '图片使用中，无法删除' : '删除图片']]) {
        const button = document.createElement('button'); button.type = 'button'; button.className = 'icon-action'; button.dataset.action = action; button.innerHTML = icon(image); button.setAttribute('aria-label', label); button.title = label;
        if (action === 'delete-asset') button.disabled = used;
        actions.append(button);
      }
      body.append(name, meta, actions); card.append(body); grid.append(card);
    }
  }
  async function loadGallery() {
    const id = ++galleryLoadID; galleryLoading = true;
    const restoreFocus = $('#gallery-grid').contains(document.activeElement);
    $('#gallery-grid').replaceChildren(); $('#gallery-empty').hidden = false; $('#gallery-message').textContent = '正在读取图库…'; $('#retry-gallery').hidden = true; $('#gallery-summary').textContent = '';
    try {
      const result = await request('/api/assets');
      if (!Array.isArray(result.assets)) throw new RequestError('图库暂时无法读取，请重试。');
      if (id !== galleryLoadID) return;
      galleryAssets = result.assets; renderGallery();
      if (restoreFocus && galleryDialog.open) $('#gallery-search').focus({ preventScroll: true });
    } catch (error) {
      if (id !== galleryLoadID) return;
      $('#gallery-message').textContent = errorText(error, '图库暂时无法读取，请重试。'); $('#gallery-empty').hidden = false; $('#retry-gallery').hidden = false;
    } finally { if (id === galleryLoadID) galleryLoading = false; }
  }
  function setGalleryFilter(value) {
    galleryFilter = value;
    for (const tab of $$('[data-gallery-filter]')) { const active = tab.dataset.galleryFilter === value; tab.setAttribute('aria-selected', String(active)); tab.tabIndex = active ? 0 : -1; }
    if (!galleryLoading) renderGallery();
  }
  function openGallery(mode = 'browse') {
    galleryMode = mode; $('#gallery-search').value = '';
    $('#gallery-title').textContent = mode === 'icon' ? '选择入口图标' : mode === 'background' ? '选择背景图片' : '图库';
    $('#gallery-help').textContent = mode === 'browse' ? '点击图片预览，可复制地址或删除未使用的图片。' : '点击图片选入，完成后保存' + (mode === 'icon' ? '入口。' : '页面设置。');
    for (const button of $$('[data-upload-type]')) button.hidden = (mode === 'icon' && button.dataset.uploadType !== 'icon') || (mode === 'background' && button.dataset.uploadType !== 'wallpaper');
    setGalleryFilter(mode === 'icon' ? 'icon' : mode === 'background' ? 'wallpaper' : 'all');
    openDialog(galleryDialog, $('[aria-selected=true]', galleryDialog));
    loadGallery();
  }
  $('#open-gallery-button').addEventListener('click', () => openGallery());
  $('#open-icon-gallery-button').addEventListener('click', () => openGallery('icon'));
  $('#open-background-gallery-button').addEventListener('click', () => openGallery('background'));
  $('#retry-gallery').addEventListener('click', loadGallery);
  $('#gallery-search').addEventListener('input', () => { if (!galleryLoading) renderGallery(); });
  $('.gallery-tabs').addEventListener('click', event => { const tab = event.target.closest('[data-gallery-filter]'); if (tab) setGalleryFilter(tab.dataset.galleryFilter); });
  $('.gallery-tabs').addEventListener('keydown', event => {
    const tabs = $$('[data-gallery-filter]'); const index = tabs.indexOf(document.activeElement);
    let next;
    if (event.key === 'ArrowRight') next = (index + 1) % tabs.length;
    if (event.key === 'ArrowLeft') next = (index - 1 + tabs.length) % tabs.length;
    if (event.key === 'Home') next = 0;
    if (event.key === 'End') next = tabs.length - 1;
    if (next !== undefined) { event.preventDefault(); setGalleryFilter(tabs[next].dataset.galleryFilter); tabs[next].focus(); }
  });
  function selectAsset(asset) {
    if (galleryMode === 'icon') {
      field('icon').value = asset.url; refreshPreview(); closeDialog(galleryDialog); showToast('图标已选入，保存入口后生效。');
    } else if (galleryMode === 'background') {
      setting('background_image').value = asset.url; previewSettings(); closeDialog(galleryDialog); showToast('背景已选入，保存页面设置后生效。');
    } else {
      $('#image-title').textContent = asset.name || '图片预览'; $('#image-preview').src = asset.url; $('#image-preview').alt = asset.name || '图片预览'; openDialog($('#image-dialog'));
    }
  }
  $('#gallery-grid').addEventListener('click', event => {
    const button = event.target.closest('button[data-action]'); const card = button?.closest('.gallery-card'); const asset = galleryAssets.find(item => item.url === card?.dataset.url);
    if (!asset) return;
    if (button.dataset.action === 'select-asset') selectAsset(asset);
    if (button.dataset.action === 'copy-asset') copyText(asset.url);
    if (button.dataset.action === 'delete-asset') openDelete('asset', asset.url, asset.name || asset.url);
  });

  function chooseUpload(target, type) {
    const dialog = target === 'icon' ? editDialog : target === 'background' ? settingsDialog : galleryDialog;
    if (busyDialogs.has(dialog)) return;
    uploadTarget = target; uploadType = type;
    const input = $('#upload-file'); input.value = ''; dialog.append(input); input.click();
  }
  $('#upload-icon-button').addEventListener('click', () => chooseUpload('icon', 'icon'));
  $('#upload-background-button').addEventListener('click', () => chooseUpload('background', 'wallpaper'));
  for (const button of $$('[data-upload-type]')) button.addEventListener('click', () => chooseUpload('gallery', button.dataset.uploadType));
  $('#upload-file').addEventListener('change', event => {
    const file = event.target.files[0]; if (!file) return;
    if (file.size > 8 * 1024 * 1024) { showToast('图片不能超过 8 MB，请压缩后再上传。', true); event.target.value = ''; return; }
    const target = uploadTarget; const type = uploadType;
    const dialog = target === 'icon' ? editDialog : target === 'background' ? settingsDialog : galleryDialog;
    const button = target === 'icon' ? $('#upload-icon-button') : target === 'background' ? $('#upload-background-button') : $('[data-upload-type=' + type + ']');
    busyAction(dialog, button, '正在上传…', async () => {
      const data = new FormData(); data.append('file', file); data.append('asset_type', type);
      const result = await mutate('/api/uploads', { method: 'POST', body: data });
      if (!result.url) throw new RequestError('图片结果未能完整读取，请重新读取图库确认。');
      if (target === 'icon') { field('icon').value = result.url; refreshPreview(); showToast('图标已上传并选入，保存入口后生效。'); }
      else if (target === 'background') { setting('background_image').value = result.url; previewSettings(); showToast('背景已上传并选入，保存页面设置后生效。'); }
      else if (galleryMode !== 'browse') selectAsset({ url: result.url, type });
      else { await loadGallery(); showToast('图片已上传。'); }
    }).finally(() => { event.target.value = ''; });
  });

  document.addEventListener('click', event => {
    const target = event.target;
    if (performance.now() < suppressClickUntil && target.closest('.app-icon')) { event.preventDefault(); return; }
    const link = target.closest('.app-icon .icon-button, .app-icon .app-name');
    if (link) {
      const id = serviceID(link);
      if (editMode) { event.preventDefault(); return openService(id); }
      if (!event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey) { event.preventDefault(); return openEntryURL(preferredEntry(service(id)).url); }
    }
    const button = target.closest('button[data-action]');
    if (!button || !button.closest('#main')) return;
    const action = button.dataset.action; const id = serviceID(button);
    if (action === 'add-service') openService('', button.dataset.groupId || '');
    if (action === 'pin') togglePin(id, button);
    if (action === 'service-menu') openItemMenu(id, button);
    if (action === 'status') { statusServiceID = id; renderStatusDetails(); openDialog($('#status-dialog')); }
    if (action === 'collapse-group') {
      const groupID = button.dataset.groupId;
      preferences.collapsed = preferences.collapsed.includes(groupID) ? preferences.collapsed.filter(value => value !== groupID) : preferences.collapsed.concat(groupID);
      savePreferences(); renderNavigation(); button.focus({ preventScroll: true });
    }
  });
  groupsRoot.addEventListener('contextmenu', event => {
    const node = event.target.closest('.app-icon'); if (!node) return;
    event.preventDefault(); openItemMenu(node.dataset.serviceId, $('.card-more', node), { x: event.clientX, y: event.clientY });
  });
  groupsRoot.addEventListener('keydown', event => {
    const node = event.target.closest('.app-icon'); if (!node) return;
    if (event.key === 'ContextMenu' || (event.shiftKey && event.key === 'F10')) { event.preventDefault(); openItemMenu(node.dataset.serviceId, $('.card-more', node)); }
  });

  function canonicalOrder() {
    return { groups: navigation.groups.map(item => ({ group_id: item.id, service_ids: item.services.map(entry => entry.id) })) };
  }
  function sortPayload() {
    return { groups: navigation.groups.map(item => ({
      group_id: item.id,
      service_ids: [...$('.icon-grid', groupNodes.get(item.id)).children].map(node => node.dataset.serviceId).filter(Boolean)
    })) };
  }
  function restoreOrder(order) {
    for (const item of order.groups) {
      const grid = $('.icon-grid', groupNodes.get(item.group_id));
      if (!grid) continue;
      for (const id of item.service_ids) {
        const node = cards.get(id);
        if (node) { grid.append(node); node.dataset.groupId = item.group_id; }
      }
    }
    updateSortCounts();
  }
  function updateSortCounts() {
    for (const node of groupNodes.values()) {
      const count = $('.icon-grid', node).children.length;
      $('.group-count', node).textContent = count + ' 个入口';
      $('.empty-group', node).hidden = count > 0;
    }
  }
  function sortFeedback(message = '', error = false) {
    $('#sort-status').textContent = message || (sortSaving ? '正在保存排序…' : sortDirty ? '排序待保存' : '排序已保存');
    $('#sort-status').classList.toggle('is-error', error);
    $('#save-sort-button').hidden = !sortDirty || sortSaving;
    $('#discard-sort-button').hidden = !sortDirty || sortSaving;
    $('#finish-edit-button').disabled = sortSaving;
  }
  function saveSort() {
    if (sortPromise) return sortPromise;
    if (dragState?.active) return Promise.resolve(false);
    if (!sortDirty) return Promise.resolve(true);
    sortSaving = true;
    sortFeedback();
    sortPromise = (async () => {
      try {
        while (sortDirty && !dragState?.active) {
          const payload = sortPayload();
          const result = await mutate('/api/services/sort', { method: 'PUT', body: payload });
          // A second move can finish while the first request is in flight.
          const current = sortPayload();
          sortDirty = JSON.stringify(current) !== JSON.stringify(payload);
          applyNavigation(result.navigation, !dragState?.active);
        }
        return !sortDirty;
      } catch (error) {
        sortDirty = true;
        sortFeedback('排序未保存，请重试或放弃', true);
        showToast(errorText(error, '排序未保存，当前位置已保留，请重试。'), true);
        return false;
      } finally {
        sortSaving = false; sortPromise = null;
        sortFeedback(sortDirty ? '排序未保存，请重试或放弃' : '', sortDirty);
      }
    })();
    return sortPromise;
  }
  async function setEditMode(value) {
    if (dragState) finishDrag(true);
    if (!value && (sortDirty || sortSaving) && !(await saveSort())) return;
    closePopovers();
    editMode = value;
    if (value) { previousSearch = search.value; search.value = ''; }
    else { search.value = previousSearch; selection = 0; }
    search.disabled = value;
    search.placeholder = value ? '完成编辑后可搜索' : '搜索名称或标签';
    document.body.classList.toggle('is-edit-mode', value);
    $('#edit-bar').hidden = !value;
    $('#edit-mode-button').setAttribute('aria-pressed', String(value));
    $('#edit-mode-button > span:last-child').textContent = value ? '完成' : '编辑';
    $('#edit-mode-button').setAttribute('aria-label', value ? '完成编辑' : '开启编辑');
    renderNavigation(); sortFeedback();
    $('#edit-mode-button').focus({ preventScroll: true });
  }
  $('#edit-mode-button').addEventListener('click', () => setEditMode(!editMode));
  $('#finish-edit-button').addEventListener('click', () => setEditMode(false));
  $('#save-sort-button').addEventListener('click', saveSort);
  $('#discard-sort-button').addEventListener('click', async () => {
    if (sortSaving || dragState) return;
    const button = $('#discard-sort-button'); button.disabled = true;
    try {
      // A lost response can mean the server already saved the order.
      const data = await mutate('/api/navigation');
      sortDirty = false; applyNavigation(data); sortFeedback();
      showToast('已恢复服务器保存的顺序。');
    } catch (error) { showToast(errorText(error, '暂时无法读取已保存的顺序，请恢复连接后重试。'), true); }
    finally { button.disabled = false; }
  });

  const sortAnimations = new Map();
  function animatedNodes() { return $$('.app-icon:not(.is-dragging), .drag-placeholder', groupsRoot); }
  function animateGridMove(mutator) {
    if (reducedMotion.matches) return mutator();
    const before = new Map(animatedNodes().map(node => [node, node.getBoundingClientRect()]));
    mutator();
    for (const node of animatedNodes()) {
      const first = before.get(node); if (!first) continue;
      sortAnimations.get(node)?.cancel();
      const last = node.getBoundingClientRect();
      const dx = first.left - last.left; const dy = first.top - last.top;
      if (Math.abs(dx) < 1 && Math.abs(dy) < 1) continue;
      const animation = node.animate([{ transform: 'translate(' + dx + 'px,' + dy + 'px)' }, { transform: 'translate(0,0)' }], { duration: 200, easing: 'cubic-bezier(.16,1,.3,1)' });
      sortAnimations.set(node, animation);
      const clear = () => { if (sortAnimations.get(node) === animation) sortAnimations.delete(node); };
      animation.onfinish = clear; animation.oncancel = clear;
    }
  }
  function layoutSortRect(node) {
    const parent = node.offsetParent;
    if (!parent) return node.getBoundingClientRect();
    const rect = parent.getBoundingClientRect();
    return { left: rect.left + parent.clientLeft + node.offsetLeft, top: rect.top + parent.clientTop + node.offsetTop, width: node.offsetWidth, height: node.offsetHeight };
  }
  function placeDraggedItem(x, y) {
    const target = document.elementFromPoint(x, y)?.closest('.group[data-group-id]');
    if (!target) return;
    const grid = $('.icon-grid', target);
    if (!grid || grid.hidden) return;
    for (const node of groupNodes.values()) node.classList.toggle('is-drop-target', node === target);
    const items = $$('.app-icon', grid).map(node => ({ node, rect: layoutSortRect(node) }));
    const rows = [];
    for (const item of items) {
      let row = rows.find(value => Math.abs(value.top - item.rect.top) < 8);
      if (!row) { row = { top: item.rect.top, bottom: item.rect.top + item.rect.height, items: [] }; rows.push(row); }
      row.items.push(item); row.bottom = Math.max(row.bottom, item.rect.top + item.rect.height);
    }
    rows.sort((a, b) => a.top - b.top);
    const row = rows.find(value => y <= value.bottom) || rows.at(-1);
    row?.items.sort((a, b) => a.rect.left - b.rect.left);
    const next = row?.items.find(item => x < item.rect.left + item.rect.width / 2)?.node;
    const previous = next ? null : row?.items.at(-1)?.node;
    const placeholder = dragState.placeholder;
    if (next && placeholder.nextElementSibling === next) return;
    if (previous && placeholder.previousElementSibling === previous) return;
    if (!row && placeholder.parentElement === grid) return;
    animateGridMove(() => { if (next) next.before(placeholder); else if (previous) previous.after(placeholder); else grid.append(placeholder); });
    updateSortCounts();
  }
  function dragFrame() {
    if (!dragState?.active) return;
    const state = dragState;
    state.item.style.transform = 'translate3d(' + (state.x - state.offsetX) + 'px,' + (state.y - state.offsetY) + 'px,0)';
    placeDraggedItem(state.x, state.y);
    const edge = 70;
    const topEdge = $('.top-tools').getBoundingClientRect().bottom + edge;
    const step = state.y < topEdge ? -Math.min(18, Math.ceil((topEdge - state.y) / edge * 18)) : state.y > innerHeight - edge ? Math.min(18, Math.ceil((state.y - innerHeight + edge) / edge * 18)) : 0;
    if (step) window.scrollBy({ top: step, behavior: 'instant' });
    state.frame = requestAnimationFrame(dragFrame);
  }
  function beginDrag() {
    const state = dragState;
    const rect = state.item.getBoundingClientRect();
    state.offsetX = state.startX - rect.left; state.offsetY = state.startY - rect.top;
    state.placeholder = document.createElement('div'); state.placeholder.className = 'drag-placeholder'; state.placeholder.dataset.serviceId = state.item.dataset.serviceId;
    state.placeholder.style.height = rect.height + 'px'; state.placeholder.setAttribute('aria-hidden', 'true');
    state.item.before(state.placeholder);
    document.body.append(state.item);
    state.handle.setPointerCapture(state.pointerID);
    state.item.classList.add('is-dragging');
    state.item.style.width = rect.width + 'px'; state.item.style.height = rect.height + 'px';
    state.active = true; document.body.classList.add('is-dragging'); closePopovers();
    $('#sort-status').textContent = '松开把手放置，Esc 取消';
    dragFrame();
  }
  function finishDrag(cancel = false) {
    const state = dragState; if (!state) return;
    if (state.active && !cancel) placeDraggedItem(state.x, state.y);
    dragState = null;
    cancelAnimationFrame(state.frame);
    if (state.placeholder) state.placeholder.replaceWith(state.item);
    state.item.classList.remove('is-dragging');
    state.item.style.width = ''; state.item.style.height = ''; state.item.style.transform = '';
    document.body.classList.remove('is-dragging');
    for (const node of groupNodes.values()) node.classList.remove('is-drop-target');
    try { state.handle.releasePointerCapture(state.pointerID); } catch (_) {}
    if (!state.active) return;
    suppressClickUntil = performance.now() + 300;
    if (cancel) { restoreOrder(state.order); showToast('本次拖动已取消。'); }
    else { state.item.dataset.groupId = state.item.closest('.group').dataset.groupId; updateSortCounts(); }
    sortDirty = JSON.stringify(sortPayload()) !== JSON.stringify(canonicalOrder());
    sortFeedback(); state.handle.focus({ preventScroll: true });
    if (sortDirty) saveSort();
  }
  groupsRoot.addEventListener('pointerdown', event => {
    const handle = event.target.closest('.drag-handle');
    if (!handle || !editMode || event.button !== 0 || dragState) return;
    dragState = { item: handle.closest('.app-icon'), handle, pointerID: event.pointerId, startX: event.clientX, startY: event.clientY, x: event.clientX, y: event.clientY, active: false, frame: 0, order: sortPayload() };
    handle.setPointerCapture(event.pointerId);
  });
  document.addEventListener('pointermove', event => {
    if (!dragState || event.pointerId !== dragState.pointerID) return;
    dragState.x = event.clientX; dragState.y = event.clientY;
    if (!dragState.active && Math.hypot(dragState.x - dragState.startX, dragState.y - dragState.startY) > 8) beginDrag();
    if (dragState.active) event.preventDefault();
  }, { passive: false });
  document.addEventListener('pointerup', event => { if (event.pointerId === dragState?.pointerID) finishDrag(); });
  document.addEventListener('pointercancel', event => { if (event.pointerId === dragState?.pointerID) finishDrag(true); });
  document.addEventListener('keydown', event => { if (event.key === 'Escape' && dragState) { event.preventDefault(); finishDrag(true); } });
  window.addEventListener('blur', () => { if (dragState) finishDrag(true); });
  document.addEventListener('visibilitychange', () => { if (document.hidden && dragState) finishDrag(true); });
  groupsRoot.addEventListener('keydown', event => {
    const handle = event.target.closest('.drag-handle');
    if (!handle || !editMode || dragState || !['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown'].includes(event.key)) return;
    event.preventDefault();
    const item = handle.closest('.app-icon'); const grid = item.parentElement;
    const grids = navigation.groups.map(entry => $('.icon-grid', groupNodes.get(entry.id)));
    const children = [...grid.children]; const index = children.indexOf(item); const groupIndex = grids.indexOf(grid);
    const columns = Math.max(1, getComputedStyle(grid).gridTemplateColumns.split(' ').length);
    const shift = { ArrowLeft: -1, ArrowRight: 1, ArrowUp: -columns, ArrowDown: columns }[event.key];
    const next = index + shift;
    animateGridMove(() => {
      if (next < 0 && grids[groupIndex - 1]) grids[groupIndex - 1].append(item);
      else if (next >= children.length && grids[groupIndex + 1]) grids[groupIndex + 1].prepend(item);
      else if (next >= 0 && next < children.length) { if (shift < 0) children[next].before(item); else children[next].after(item); }
    });
    item.dataset.groupId = item.closest('.group').dataset.groupId;
    updateSortCounts(); handle.focus({ preventScroll: true });
    sortDirty = JSON.stringify(sortPayload()) !== JSON.stringify(canonicalOrder());
    sortFeedback();
    if (sortDirty) saveSort();
  });

  function scheduleStatus() {
    clearInterval(statusTimer);
    if (!document.hidden) statusTimer = setInterval(refreshStatus, 30000);
  }
  document.addEventListener('visibilitychange', () => { scheduleStatus(); if (!document.hidden) refreshStatus(); });
  const toolbarObserver = new ResizeObserver(() => { document.documentElement.style.setProperty('--toolbar-height', ($('.top-tools').getBoundingClientRect().height + 12) + 'px'); });
  toolbarObserver.observe($('.top-tools'));
  renderNavigation(); applyAppearance(navigation.appearance); refreshStatus(); scheduleStatus();
})();
