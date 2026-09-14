const { test } = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
const path = require('node:path');
const { applyPlayerVolume } = require('../electron/player-volume.cjs');

function fixture() {
  const media = { volume: 0, muted: false };
  let engineVolume = 0;
  const engine = {
    setVolume(value) { engineVolume = value; media.volume = value / 100; },
    getVolume() { return engineVolume; },
    isMuted() { return media.muted; },
    unMute() { media.muted = false; this.setVolume(5); },
  };
  const document = { querySelector(selector) {
    assert.equal(selector, '#movie_player', 'must not write the nonlinear Music slider');
    return engine;
  } };
  const apply = (value, unmute = false) => vm.runInNewContext(
    `(${applyPlayerVolume.toString()})(${JSON.stringify(value)}, ${unmute})`, { document },
  );
  return { media, engine, apply };
}

test('all 101 numeric levels keep media and engine synchronized, including 1–4', () => {
  const { media, engine, apply } = fixture();
  for (let volume = 0; volume <= 100; volume++) {
    assert.equal(apply(volume), true);
    assert.equal(engine.getVolume(), volume);
    assert.equal(media.volume, volume / 100);
    // A subsequent engine reapplication must not restore a stale zero.
    engine.setVolume(engine.getVolume());
    assert.equal(media.volume, volume / 100);
  }
});

test('lifecycle restore honors mute; only a positive user volume change unmutes', () => {
  const { media, apply } = fixture();
  media.muted = true;
  apply(1);
  assert.equal(media.muted, true);
  apply(0, true);
  assert.equal(media.muted, true);
  apply(1, true);
  assert.equal(media.muted, false);
  assert.equal(media.volume, 0.01);
});

test('bounds and invalid volume are safe', () => {
  const { engine, apply } = fixture();
  apply(-10); assert.equal(engine.getVolume(), 0);
  apply(120); assert.equal(engine.getVolume(), 100);
  apply(2.6); assert.equal(engine.getVolume(), 3);
  assert.equal(apply('invalid'), false);
  assert.equal(engine.getVolume(), 3);
});

test('an unavailable player does not fall back to an unsynchronized media write', () => {
  assert.equal(vm.runInNewContext(`(${applyPlayerVolume.toString()})(1)`, {
    document: { querySelector() { return null; } },
  }), false);
});

test('preload guards new media, then syncs its volume without forcing playback or unmuting', () => {
  const source = fs.readFileSync(path.join(__dirname, '../electron/player-preload.cjs'), 'utf8');
  const sent = [], events = {};
  const video = { dataset: {}, addEventListener(name, callback) {
    (events[name] ||= []).push(callback);
  } };
  const document = {
    readyState: 'complete', documentElement: {},
    querySelectorAll(selector) { return selector === 'video' ? [video] : []; },
  };
  vm.runInNewContext(source, {
    require() { return { ipcRenderer: { send(channel) { sent.push(channel); } } }; },
    document, MutationObserver: class { observe() {} },
    setTimeout() { return 1; }, clearTimeout() {},
  });
  assert.deepEqual(sent, ['player:media-attached']);
  for (const event of ['loadedmetadata', 'playing']) events[event].forEach(callback => callback());
  assert.equal(sent.filter(channel => channel === 'player:volume-ready').length, 2);
  for (const event of ['pause', 'waiting', 'stalled', 'error', 'volumechange']) {
    (events[event] || []).forEach(callback => callback());
  }
  assert.equal(sent.length, 3, 'pause/mute/buffering must not trigger a volume/play watchdog');
});

test('preload waits for DOM readiness before observing the document', () => {
  const source = fs.readFileSync(path.join(__dirname, '../electron/player-preload.cjs'), 'utf8');
  let ready, observations = 0;
  const document = {
    readyState: 'loading', documentElement: null,
    addEventListener(event, callback) { assert.equal(event, 'DOMContentLoaded'); ready = callback; },
    querySelectorAll() { return []; },
  };
  vm.runInNewContext(source, {
    require() { return { ipcRenderer: { send() {} } }; }, document,
    MutationObserver: class { observe(root) { assert.ok(root); observations++; } },
    setTimeout() { return 1; }, clearTimeout() {},
  });
  assert.equal(observations, 0);
  document.documentElement = {};
  ready();
  assert.equal(observations, 1);
});
