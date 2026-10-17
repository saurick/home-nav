const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const app = fs.readFileSync(path.join(__dirname, 'app.js'), 'utf8');
const helpers = app.slice(app.indexOf('  function isMonochromeIcon('), app.indexOf('  function renderIcon('));
const ctx = {};
vm.runInNewContext(helpers, ctx);
const pixels = (...colors) => Uint8ClampedArray.from(colors.flat());
const clear = [0, 0, 0, 0], black = [0, 0, 0, 255], white = [255, 255, 255, 255];

test('transparent black and white marks adapt, including antialiased edges', () => {
  assert.equal(ctx.isMonochromeIcon(pixels(clear, white, white, white, white, [255, 255, 255, 64])), true);
  assert.equal(ctx.isMonochromeIcon(pixels(clear, black, black, black, black, [0, 0, 0, 64])), true);
});

test('colored artwork, mixed black and white, and opaque tiles retain their colors', () => {
  assert.equal(ctx.isMonochromeIcon(pixels(clear, white, white, white, white, [0, 120, 255, 255])), false);
  assert.equal(ctx.isMonochromeIcon(pixels(clear, white, white, white, white, black)), false);
  assert.equal(ctx.isMonochromeIcon(pixels(black, black, black, black)), false);
  assert.equal(ctx.isMonochromeIcon(pixels(white, white, white, white)), false);
  assert.equal(ctx.isMonochromeIcon(pixels(clear, [100, 100, 100, 255], white, white, white)), false);
});

test('empty, nearly transparent, or nearly opaque images do not become monochrome', () => {
  assert.equal(ctx.isMonochromeIcon(pixels()), false);
  assert.equal(ctx.isMonochromeIcon(pixels(clear, clear, clear, clear)), false);
  assert.equal(ctx.isMonochromeIcon(pixels(clear, [255, 255, 255, 20])), false);
  assert.equal(ctx.isMonochromeIcon(pixels(clear, ...Array(25).fill(white))), false);
});

function adapterContext({ origin = 'https://home.example.test', error = false, samples = pixels(clear, white, white, white, white) } = {}) {
  let reads = 0;
  const context = { URL, location: { origin, href: origin + '/' }, document: { createElement() {
    reads++;
    return { getContext: () => ({ drawImage() { if (error) throw new Error('unreadable pixels'); }, getImageData: () => ({ data: samples }) }) };
  } } };
  vm.runInNewContext(helpers, context);
  return { ...context, reads: () => reads };
}
const image = src => ({ complete: true, naturalWidth: 64, dataset: {}, src });

test('same-source icons share pixel analysis while replacements receive fresh classification', () => {
  const context = adapterContext();
  const first = image('https://home.example.test/white.svg');
  const repeated = image(first.src);
  context.adaptIcon(first); context.adaptIcon(repeated);
  assert.equal(first.dataset.iconTone, 'monochrome');
  assert.equal(repeated.dataset.iconTone, 'monochrome');
  assert.equal(context.reads(), 1);
  const replacement = image('https://home.example.test/new.svg');
  context.adaptIcon(replacement);
  assert.equal(context.reads(), 2);
});

test('external, unreadable, and unloaded images keep normal loading and artwork', () => {
  const context = adapterContext({ error: true });
  const external = image('https://icons.example.test/white.svg');
  context.adaptIcon(external);
  assert.equal(external.dataset.iconTone, 'original');
  assert.equal(context.reads(), 0);
  const unreadable = image('https://home.example.test/unreadable.svg');
  assert.doesNotThrow(() => context.adaptIcon(unreadable));
  assert.equal(unreadable.dataset.iconTone, 'original');
  const unloaded = { ...image(unreadable.src), complete: false };
  context.adaptIcon(unloaded);
  assert.equal(unloaded.dataset.iconTone, undefined);
});
