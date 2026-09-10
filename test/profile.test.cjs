const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { profileDirectory, migrateLegacyProfile, writeSettings } = require('../electron/profile.cjs');

test('a different launcher cannot replace the shared account and settings', (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'ytm-profile-test-'));
  t.after(() => {
    assert.equal(path.dirname(root), path.resolve(os.tmpdir()));
    fs.rmSync(root, { recursive: true });
  });
  const packaged = path.join(root, 'packaged-appdata');
  const normal = path.join(root, 'normal-appdata');
  const target = profileDirectory(root);
  fs.mkdirSync(path.join(packaged, 'Network'), { recursive: true });
  fs.mkdirSync(normal);
  fs.mkdirSync(target);
  fs.writeFileSync(path.join(packaged, 'settings.json'), JSON.stringify({ volume: 1, discordEnabled: true }));
  fs.writeFileSync(path.join(packaged, 'Network', 'Cookies'), 'test account fixture');
  fs.writeFileSync(path.join(normal, 'settings.json'), JSON.stringify({ volume: 50, discordEnabled: false }));
  migrateLegacyProfile(target, packaged);
  migrateLegacyProfile(target, normal);
  assert.deepEqual(JSON.parse(fs.readFileSync(path.join(target, 'settings.json'))), { volume: 1, discordEnabled: true });
  assert.equal(fs.readFileSync(path.join(target, 'Network', 'Cookies'), 'utf8'), 'test account fixture');
  writeSettings(path.join(target, 'settings.json'), { volume: 3, discordEnabled: true });
  migrateLegacyProfile(target, packaged);
  assert.equal(JSON.parse(fs.readFileSync(path.join(target, 'settings.json'))).volume, 3);
  assert.equal(fs.existsSync(path.join(target, 'settings.json.tmp')), false);
});

test('an existing shared profile is retained even without a migration marker', (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'ytm-profile-test-'));
  t.after(() => {
    assert.equal(path.dirname(root), path.resolve(os.tmpdir()));
    fs.rmSync(root, { recursive: true });
  });
  const target = profileDirectory(root);
  fs.mkdirSync(target);
  writeSettings(path.join(target, 'settings.json'), { volume: 0 });
  migrateLegacyProfile(target, path.join(root, 'missing-legacy'));
  assert.equal(JSON.parse(fs.readFileSync(path.join(target, 'settings.json'))).volume, 0);
});
