'use strict';

(() => {
  // Keep named accents aligned with Plush's appearance palette.
  const accents = Object.freeze({
    blue: { label: '蓝色', primary: '#0a66c2', strong: '#0b4f96', hover: '#0858aa', dark: '#75b9ff', darkHover: '#8ac4ff', onPrimary: '#ffffff' },
    green: { label: '橄榄绿', primary: '#5e793e', strong: '#49632e', hover: '#506a34', dark: '#a5bc87', darkHover: '#b3c699', onPrimary: '#ffffff' },
    teal: { label: '青绿', primary: '#257f7c', strong: '#1c6260', hover: '#1f6c69', dark: '#7ad6cf', darkHover: '#8edcd6', onPrimary: '#ffffff' },
    yellow: { label: '橙金', primary: '#9b6429', strong: '#78501f', hover: '#865522', dark: '#f1c18b', darkHover: '#f3ca9c', onPrimary: '#ffffff' },
    pink: { label: '粉色', primary: '#c83b82', strong: '#98245e', hover: '#ac2b6b', dark: '#ff91c3', darkHover: '#ffa2cc', onPrimary: '#ffffff' },
    mauve: { label: '藕紫', primary: '#a34f91', strong: '#7d3b6f', hover: '#8e437e', dark: '#e4a3d7', darkHover: '#e8b1dd', onPrimary: '#ffffff' },
    orange: { label: '橙色', primary: '#c74d0d', strong: '#943a08', hover: '#aa3f08', dark: '#ff9b61', darkHover: '#ffaa79', onPrimary: '#ffffff' },
    purple: { label: '灰紫', primary: '#666582', strong: '#50506c', hover: '#595872', dark: '#bab8dc', darkHover: '#c4c3e1', onPrimary: '#ffffff' },
  });
  const storageKey = 'home-nav.view-preferences';
  function normalize(value) {
    return {
      accent: Object.hasOwn(accents, value?.accent) ? value.accent : 'blue',
      theme_mode: ['system', 'light', 'dark'].includes(value?.theme_mode) ? value.theme_mode : 'system',
      density: value?.density === 'comfortable' ? 'comfortable' : 'compact',
      collapsed: Array.isArray(value?.collapsed) ? value.collapsed.filter(id => typeof id === 'string') : [],
    };
  }
  function read(storage) {
    try { return normalize(JSON.parse(storage.getItem(storageKey) || '{}')); } catch (_) { return normalize(null); }
  }
  function resolvedMode(mode, systemDark) { return mode === 'system' ? (systemDark ? 'dark' : 'light') : mode; }
  function backgroundColor(appearance) {
    const color = appearance.background_color || '#000000';
    return /^#(?:000|000000)$/i.test(color) ? 'var(--page)' : color;
  }
  const api = { accents, storageKey, normalize, read, resolvedMode, backgroundColor };
  if (typeof window === 'undefined') { module.exports = api; return; }

  const system = matchMedia('(prefers-color-scheme: dark)');
  let current;
  function apply(value) {
    current = normalize(value);
    const root = document.documentElement;
    const mode = resolvedMode(current.theme_mode, system.matches);
    const palette = accents[current.accent];
    root.dataset.theme = mode;
    root.dataset.themeMode = current.theme_mode;
    root.dataset.accent = current.accent;
    root.dataset.density = current.density;
    if (document.body) document.body.dataset.density = current.density;
    root.style.setProperty('--accent', mode === 'dark' ? palette.dark : palette.strong);
    root.style.setProperty('--primary', mode === 'dark' ? palette.dark : palette.primary);
    root.style.setProperty('--primary-hover', mode === 'dark' ? palette.darkHover : palette.hover);
    root.style.setProperty('--on-accent', mode === 'dark' ? '#050607' : palette.onPrimary);
    document.dispatchEvent(new CustomEvent('home-nav-themechange'));
  }
  function save(value) {
    const normalized = normalize(value);
    try { localStorage.setItem(storageKey, JSON.stringify(normalized)); } catch (_) {}
    return normalized;
  }
  function readCurrent() { try { return read(localStorage); } catch (_) { return normalize(null); } }
  window.HomeNavTheme = { ...api, apply, save, read: readCurrent };
  apply(readCurrent());
  system.addEventListener('change', () => { if (current.theme_mode === 'system') apply(current); });
})();
