'use strict';

(() => {
  // Keep the six named accents aligned with Plush's appearance palette.
  const accents = Object.freeze({
    blue: { label: '蓝色', primary: '#0a66c2', strong: '#0b4f96', hover: '#0858aa', dark: '#75b9ff', onPrimary: '#ffffff' },
    green: { label: '绿色', primary: '#16834f', strong: '#0b633a', hover: '#0f6e41', dark: '#70dfa1', onPrimary: '#ffffff' },
    yellow: { label: '黄色', primary: '#f2b515', strong: '#755400', hover: '#db9f00', dark: '#ffd866', onPrimary: '#231f13' },
    pink: { label: '粉色', primary: '#c83b82', strong: '#98245e', hover: '#ac2b6b', dark: '#ff91c3', onPrimary: '#ffffff' },
    orange: { label: '橙色', primary: '#c74d0d', strong: '#943a08', hover: '#aa3f08', dark: '#ff9b61', onPrimary: '#ffffff' },
    purple: { label: '紫色', primary: '#7650c7', strong: '#55349c', hover: '#6240ad', dark: '#b79aff', onPrimary: '#ffffff' },
  });
  const storageKey = 'home-nav.view-preferences';
  function normalize(value) {
    return {
      accent: Object.hasOwn(accents, value?.accent) ? value.accent : 'green',
      theme_mode: ['system', 'light', 'dark'].includes(value?.theme_mode) ? value.theme_mode : 'dark',
      density: value?.density === 'comfortable' ? 'comfortable' : 'compact',
      collapsed: Array.isArray(value?.collapsed) ? value.collapsed.filter(id => typeof id === 'string') : [],
    };
  }
  function read(storage) {
    try { return normalize(JSON.parse(storage.getItem(storageKey) || '{}')); } catch (_) { return normalize(null); }
  }
  function resolvedMode(mode, systemDark) { return mode === 'system' ? (systemDark ? 'dark' : 'light') : mode; }
  function backgroundColor(appearance, mode) {
    const color = appearance.background_color || '#000000';
    return /^#(?:000|000000)$/i.test(color) && mode === 'light' ? '#f2f5f3' : color;
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
    root.style.setProperty('--primary-hover', mode === 'dark' ? palette.dark : palette.hover);
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
