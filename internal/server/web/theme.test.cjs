const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const test = require('node:test');
const theme = require('./theme.js');

test('existing density and collapsed groups survive adding appearance preferences', () => {
  const prefs = theme.read({ getItem: () => JSON.stringify({ density: 'comfortable', collapsed: ['apps', 12, '@pinned'] }) });
  assert.deepEqual(prefs, { accent: 'blue', theme_mode: 'system', density: 'comfortable', collapsed: ['apps', '@pinned'] });
  for (const raw of ['invalid', 'null', '[]', '{"accent":"constructor","theme_mode":"invalid","density":"bad"}']) {
    assert.deepEqual(theme.read({ getItem: () => raw }), theme.normalize(null));
  }
});

test('missing preferences and restore defaults use blue, system mode, and compact density', () => {
  const defaults = { accent: 'blue', theme_mode: 'system', density: 'compact', collapsed: [] };
  assert.deepEqual(theme.normalize(null), defaults);
  assert.deepEqual(theme.read({ getItem: () => null }), defaults);
  assert.deepEqual(theme.normalize({ accent: 'green', theme_mode: 'dark', density: 'comfortable', collapsed: ['apps'] }),
    { accent: 'green', theme_mode: 'dark', density: 'comfortable', collapsed: ['apps'] });
});

test('system mode resolves both ways while explicit modes remain explicit', () => {
  assert.equal(theme.resolvedMode('system', true), 'dark');
  assert.equal(theme.resolvedMode('system', false), 'light');
  assert.equal(theme.resolvedMode('light', true), 'light');
  assert.equal(theme.resolvedMode('dark', false), 'dark');
});

test('default backgrounds inherit the current palette while custom backgrounds stay explicit', () => {
  for (const background_color of [undefined, '', '#000', '#000000']) {
    assert.equal(theme.backgroundColor({ background_color }), 'var(--page)');
  }
  for (const background_color of ['#18212b', '#fff', '#C74D0D']) {
    assert.equal(theme.backgroundColor({ background_color }), background_color);
  }
});

function luminance(hex) {
  const values = hex.slice(1).match(/../g).map(v => parseInt(v, 16) / 255).map(v => v <= .04045 ? v / 12.92 : ((v + .055) / 1.055) ** 2.4);
  return values[0] * .2126 + values[1] * .7152 + values[2] * .0722;
}
function contrast(a, b) { const x = luminance(a), y = luminance(b); return (Math.max(x, y) + .05) / (Math.min(x, y) + .05); }
test('all six accents keep readable selected text and primary button labels in both modes', () => {
  assert.equal(Object.keys(theme.accents).length, 6);
  for (const [name, palette] of Object.entries(theme.accents)) {
    assert.ok(contrast(palette.strong, '#ffffff') >= 4.5, name + ' light selected text');
    assert.ok(contrast(palette.primary, palette.onPrimary) >= 4.5, name + ' light button');
    assert.ok(contrast(palette.hover, palette.onPrimary) >= 4.5, name + ' light hovered button');
    assert.ok(contrast(palette.dark, '#171f2b') >= 4.5, name + ' dark selected text');
    assert.ok(contrast(palette.dark, '#050607') >= 4.5, name + ' dark button');
  }
});

test('browser bootstrap tolerates denied storage and responds to system changes', () => {
  const styles = {};
  const root = { dataset: {}, style: { setProperty(name, value) { styles[name] = value; } } };
  const system = { matches: false, addEventListener(_, fn) { this.changed = fn; } };
  const context = { window: {}, document: { documentElement: root, dispatchEvent() {} }, matchMedia: () => system, CustomEvent: class {} };
  Object.defineProperty(context, 'localStorage', { get() { throw new Error('Storage denied'); } });
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, 'theme.js'), 'utf8'), context);
  assert.equal(root.dataset.theme, 'light');
  assert.equal(root.dataset.accent, 'blue');
  assert.equal(context.window.HomeNavTheme.read().theme_mode, 'system');
  for (const [accent, palette] of Object.entries(theme.accents)) {
    for (const theme_mode of ['light', 'dark']) {
      context.window.HomeNavTheme.apply({ theme_mode, accent });
      assert.equal(root.dataset.accent, accent);
      assert.equal(styles['--primary'], theme_mode === 'dark' ? palette.dark : palette.primary);
      assert.equal(styles['--accent'], theme_mode === 'dark' ? palette.dark : palette.strong);
      assert.equal(styles['--on-accent'], theme_mode === 'dark' ? '#050607' : palette.onPrimary);
    }
  }
  context.window.HomeNavTheme.apply({ theme_mode: 'system', accent: 'pink' });
  assert.equal(root.dataset.theme, 'light');
  assert.equal(styles['--primary'], theme.accents.pink.primary);
  system.matches = true; system.changed();
  assert.equal(root.dataset.theme, 'dark');
  assert.equal(styles['--primary'], theme.accents.pink.dark);
  assert.doesNotThrow(() => context.window.HomeNavTheme.save({ accent: 'pink' }));
});
