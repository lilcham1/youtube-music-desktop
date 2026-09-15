const { test } = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
const path = require('node:path');
const {
  withPrefVolume, prefVolume, engineToSliderVolume, audibleVolume, applyPlayerVolume,
} = require('../electron/player-volume.cjs');

test('a non-zero level is never inside the slider dead zone (1–4 → silent)', () => {
  assert.equal(audibleVolume(0), 0);
  for (const level of [1, 2, 3, 4]) assert.equal(audibleVolume(level), 5, `level ${level}`);
  assert.equal(audibleVolume(5), 5);
  assert.equal(audibleVolume(42), 42);
  assert.equal(audibleVolume('x'), undefined);
  // The cookie seed applies the same floor: an old engine-scale "1" left by a
  // downgraded build must still be audible on first launch.
  assert.equal(withPrefVolume('tz=UTC', 1), 'tz=UTC&volume=5');
  assert.equal(withPrefVolume('tz=UTC', 0), 'tz=UTC&volume=0');
  for (let engine = 1; engine <= 100; engine++) {
    assert.ok(engineToSliderVolume(engine) >= 5, `engine ${engine} must migrate to an audible slider level`);
  }
});

test('PREF cookie: volume is added, replaced, and other fields are preserved', () => {
  assert.equal(withPrefVolume(undefined, 42), 'volume=42');
  assert.equal(withPrefVolume('', 42), 'volume=42');
  assert.equal(withPrefVolume('volume=7', 42), 'volume=42');
  assert.equal(withPrefVolume('f6=40000000&tz=Europe.Paris&volume=7&f7=100', 42),
    'f6=40000000&tz=Europe.Paris&f7=100&volume=42');
  assert.equal(withPrefVolume('tz=UTC', 250), 'tz=UTC&volume=100');
  assert.equal(withPrefVolume('tz=UTC', -3), 'tz=UTC&volume=0');
  assert.equal(withPrefVolume('tz=UTC&volume=9', 'nope'), 'tz=UTC', 'an invalid level drops the field');
  assert.equal(prefVolume('f6=1&volume=42&f7=2'), 42);
  assert.equal(prefVolume('volume=42'), 42);
  assert.equal(prefVolume('f6=1'), undefined);
});

test('engine gain migrates to the slider scale monotonically and within bounds', () => {
  assert.equal(engineToSliderVolume(0), 0);
  assert.equal(engineToSliderVolume(20), 50);
  assert.equal(engineToSliderVolume(100), 100);
  assert.equal(engineToSliderVolume(-5), 0);
  assert.equal(engineToSliderVolume(500), 100);
  assert.equal(engineToSliderVolume('x'), undefined);
  let previous = -1;
  for (let engine = 0; engine <= 100; engine++) {
    const slider = engineToSliderVolume(engine);
    assert.ok(slider >= previous && slider >= 0 && slider <= 100, `engine ${engine} → ${slider}`);
    previous = slider;
  }
});

function pageFixture(hasBar = true) {
  const calls = [];
  const bar = { updateVolume(value) { calls.push(value); } };
  const document = { querySelector(selector) {
    assert.equal(selector, 'ytmusic-player-bar', 'volume must go through the player bar, never the raw engine');
    return hasBar ? bar : null;
  } };
  const apply = (value) => vm.runInNewContext(
    `(${applyPlayerVolume.toString()})(${JSON.stringify(value)})`, { document },
  );
  return { calls, apply };
}

test('applyPlayerVolume uses the player bar API with a clamped integer level', () => {
  const { calls, apply } = pageFixture();
  assert.equal(apply(42), true);
  assert.equal(apply(2.6), true);
  assert.equal(apply(-10), true);
  assert.equal(apply(120), true);
  assert.deepEqual(calls, [42, 5, 0, 100], '2.6 rounds to 3, which is silent, so it is lifted to 5');
  assert.equal(apply('invalid'), false);
  assert.equal(calls.length, 4);
});

test('an unavailable player bar is reported, not worked around', () => {
  const { calls, apply } = pageFixture(false);
  assert.equal(apply(42), false);
  assert.deepEqual(calls, []);
});

function loadPreload({ readyState = 'complete', documentElement = {}, video } = {}) {
  const source = fs.readFileSync(path.join(__dirname, '../electron/player-preload.cjs'), 'utf8');
  const sent = [];
  const docListeners = {};
  const videoEvents = {};
  const slider = { attrs: {}, getAttribute(name) { return this.attrs[name] ?? null; } };
  const document = {
    readyState,
    documentElement,
    addEventListener(name, callback) { (docListeners[name] ||= []).push(callback); },
    querySelector(selector) { return selector === '#volume-slider' ? slider : null; },
    querySelectorAll(selector) { return selector === 'video' && video ? [video] : []; },
    title: '',
  };
  if (video) video.addEventListener = (name, callback) => { (videoEvents[name] ||= []).push(callback); };
  let observations = 0;
  vm.runInNewContext(source, {
    require() { return { ipcRenderer: { send(channel, value) { sent.push([channel, value]); } } }; },
    document, navigator: {},
    MutationObserver: class { observe(root) { assert.ok(root); observations++; } },
    setTimeout() { return 1; }, clearTimeout() {},
  });
  return { sent, docListeners, videoEvents, slider, observations: () => observations };
}

test('preload never mutes, guards, or force-plays media; it only observes playback', () => {
  const video = { dataset: {}, paused: true, ended: false, readyState: 0, currentTime: 0 };
  const { sent, videoEvents } = loadPreload({ video });
  assert.deepEqual(sent, [], 'attaching media must not signal the main process');
  const bound = Object.keys(videoEvents).sort();
  for (const forbidden of ['stalled', 'waiting', 'error', 'volumechange']) {
    assert.ok(!bound.includes(forbidden), `${forbidden} must not drive a watchdog`);
  }
  assert.ok(bound.includes('play') && bound.includes('pause'));
});

test('preload reports the slider level once per change so the title bar follows the page', () => {
  const { sent, docListeners, slider } = loadPreload();
  const [onValueChange] = docListeners['value-change'];
  slider.attrs['aria-valuenow'] = '42';
  onValueChange({ target: { id: 'volume-slider' } });
  onValueChange({ target: { id: 'volume-slider' } });
  slider.attrs['aria-valuenow'] = '43';
  onValueChange({ target: { id: 'progress-bar' } });
  onValueChange({ target: { id: 'volume-slider' } });
  assert.deepEqual(sent, [['player:volume-changed', 42], ['player:volume-changed', 43]]);
});

test('preload waits for DOM readiness before observing the document', () => {
  const state = { readyState: 'loading', documentElement: null };
  const source = fs.readFileSync(path.join(__dirname, '../electron/player-preload.cjs'), 'utf8');
  let ready, observations = 0;
  const document = {
    get readyState() { return state.readyState; },
    get documentElement() { return state.documentElement; },
    addEventListener(event, callback) { if (event === 'DOMContentLoaded') ready = callback; },
    querySelector() { return null; },
    querySelectorAll() { return []; },
  };
  vm.runInNewContext(source, {
    require() { return { ipcRenderer: { send() {} } }; }, document, navigator: {},
    MutationObserver: class { observe(root) { assert.ok(root); observations++; } },
    setTimeout() { return 1; }, clearTimeout() {},
  });
  assert.equal(observations, 0);
  state.documentElement = {};
  ready();
  assert.equal(observations, 1);
});
