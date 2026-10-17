const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const test = require('node:test');
const theme = require('./theme.js');

test('existing density and collapsed groups survive adding appearance preferences', () => {
  const prefs = theme.read({ getItem: () => JSON.stringify({ density: 'comfortable', collapsed: ['apps', 12, '@pinned'] }) });
  assert.deepEqual(prefs, { accent: 'green', theme_mode: 'dark', density: 'comfortable', collapsed: ['apps', '@pinned'] });
  for (const raw of ['invalid', 'null', '[]', '{"accent":"constructor","theme_mode":"invalid","density":"bad"}']) {
    assert.deepEqual(theme.read({ getItem: () => raw }), theme.normalize(null));
  }
});

test('system mode resolves both ways while explicit modes remain explicit', () => {
  assert.equal(theme.resolvedMode('system', true), 'dark');
  assert.equal(theme.resolvedMode('system', false), 'light');
  assert.equal(theme.resolvedMode('light', true), 'light');
  assert.equal(theme.resolvedMode('dark', false), 'dark');
  assert.equal(theme.backgroundColor({ background_color: '#000000' }, 'light'), '#f2f5f3');
  assert.equal(theme.backgroundColor({ background_color: '#18212b' }, 'light'), '#18212b');
  assert.equal(theme.backgroundColor({ background_color: '#000000' }, 'dark'), '#000000');
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
    assert.ok(contrast(palette.dark, '#171f2b') >= 4.5, name + ' dark selected text');
    assert.ok(contrast(palette.dark, '#050607') >= 4.5, name + ' dark button');
  }
});

test('browser bootstrap tolerates denied storage and responds to system changes', () => {
  const root = { dataset: {}, style: { setProperty() {} } };
  const system = { matches: false, addEventListener(_, fn) { this.changed = fn; } };
  const context = { window: {}, document: { documentElement: root, dispatchEvent() {} }, matchMedia: () => system, CustomEvent: class {} };
  Object.defineProperty(context, 'localStorage', { get() { throw new Error('Storage denied'); } });
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, 'theme.js'), 'utf8'), context);
  assert.equal(root.dataset.theme, 'dark');
  assert.equal(context.window.HomeNavTheme.read().theme_mode, 'dark');
  context.window.HomeNavTheme.apply({ theme_mode: 'system', accent: 'pink' });
  assert.equal(root.dataset.theme, 'light');
  system.matches = true; system.changed();
  assert.equal(root.dataset.theme, 'dark');
  assert.doesNotThrow(() => context.window.HomeNavTheme.save({ accent: 'pink' }));
});
