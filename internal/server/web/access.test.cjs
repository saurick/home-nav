const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

// Execute the shipped helpers so the browser and editor contracts share a test.
const app = fs.readFileSync(path.join(__dirname, 'app.js'), 'utf8');
const helpers = app.slice(app.indexOf('  function entryURL('), app.indexOf('  function entryHint('));
function context(defaultMode = 'internal') {
  const ctx = {
    navigation: { default_access_mode: defaultMode }, accessMode: 'internal',
    urlFields: { internal: 'internal_url', internal_domain: 'internal_domain_url', external: 'external_url' },
  };
  vm.runInNewContext(helpers, ctx);
  return ctx;
}
const service = {
  internal_url: 'http://home-lan.example.test:5001/',
  internal_domain_url: 'https://app-lan.example.test/',
  external_url: 'https://app.example.test/',
};

test('new request addresses are used without overwriting the configured hostname', () => {
  const ctx = context();
  for (const host of ['10.55.1.2', '100.122.33.44', '192.168.90.12']) {
    const item = { ...service, internal_access_url: `http://${host}:5001/` };
    const selected = ctx.preferredEntry(item);
    assert.equal(selected.url, item.internal_access_url);
    assert.equal(selected.type, 'internal');
    assert.equal(selected.fallback, false);
    assert.equal(item.internal_url, service.internal_url);
    assert.equal(ctx.preferredEntry(item, 'external').url, service.external_url);
  }
});

test('a blocked direct URL stays blocked and chooses the external fallback', () => {
  const ctx = context();
  const item = { ...service, internal_access_url: '' };
  assert.equal(ctx.entryURL(item, 'internal'), '');
  const selected = ctx.preferredEntry(item);
  assert.equal(selected.url, service.external_url);
  assert.equal(selected.fallback, true);
  assert.equal(selected.unavailable, true);
  assert.equal(ctx.preferredEntry({ ...item, external_url: '' }).url, service.internal_domain_url);
  assert.equal(ctx.preferredEntry({ ...item, external_url: '', internal_domain_url: '' }).url, '');
});

test('unsaved editor values use the stable hostname for health target selection', () => {
  const selected = context().preferredEntry(service);
  assert.equal(selected.url, service.internal_url);
  assert.equal(selected.type, 'internal');
  assert.equal(selected.unavailable, false);
});
