// End-to-end volume check against a running app started with
// YTM_DEBUG_PORT=9233. Restores the original saved level when done.
import fs from 'node:fs';
import os from 'node:os';
import assert from 'node:assert/strict';

const port = process.env.YTM_DEBUG_PORT || '9233';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const settingsFile = `${os.homedir()}/.youtube-music/settings.json`;
const savedVolume = () => JSON.parse(fs.readFileSync(settingsFile, 'utf8')).volume;

async function connect(match) {
  const targets = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
  const target = targets.find((t) => t.type === 'page' && match(t));
  if (!target) throw new Error('debug target not found; start the app with YTM_DEBUG_PORT');
  const ws = new WebSocket(target.webSocketDebuggerUrl);
  await new Promise((r) => (ws.onopen = r));
  let id = 0;
  const pending = new Map();
  ws.onmessage = (m) => { const d = JSON.parse(m.data); pending.get(d.id)?.(d); };
  const send = (method, params = {}) => new Promise((r) => { pending.set(++id, r); ws.send(JSON.stringify({ id, method, params })); });
  const evaluate = async (expression) => {
    const r = await send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
    if (r.result.exceptionDetails) throw new Error(r.result.exceptionDetails.exception?.description);
    return r.result.result.value;
  };
  return { send, evaluate, close: () => ws.close() };
}

const page = await connect((t) => t.url.startsWith('https://music.youtube.com'));
const shell = await connect((t) => t.url === 'about:blank' && t.title === 'YouTube Music');
const state = () => page.evaluate(`(() => {
  const mp = document.querySelector('#movie_player'); const v = document.querySelector('video');
  return { engine: mp?.getVolume?.(), media: v?.volume, muted: !!(v?.muted || mp?.isMuted?.()),
    playing: !!v && !v.paused, title: mp?.getVideoData?.()?.title,
    pref: (/(?:^|; )PREF=([^;]*)/.exec(document.cookie) || [])[1] };
})()`);
const setTitleBar = (n) => shell.evaluate(`(() => { const v = document.querySelector('#volume'); v.value = '${n}'; v.dispatchEvent(new Event('change')); })()`);
const original = savedVolume();

try {
  let s = await state();
  if (!s.playing) {
    await page.evaluate(`document.querySelector('ytmusic-play-button-renderer')?.click()`);
    for (let i = 0; i < 30 && !(s = await state()).playing; i++) await sleep(500);
  }
  assert.equal(s.playing, true, 'something must be playing');
  assert.equal(s.engine, original, 'starts at the saved level');
  assert.ok(!s.muted && s.media > 0, 'audible');
  console.log('start         ', JSON.stringify(s));

  await setTitleBar(30); await sleep(800); s = await state();
  assert.equal(s.engine, 30); assert.equal(savedVolume(), 30); assert.match(s.pref, /volume=61/);
  console.log('title bar 30  ', JSON.stringify(s));

  await page.evaluate(`document.querySelector('#movie_player').setVolume(70)`); await sleep(600); s = await state();
  assert.equal(s.engine, 30, 'a YouTube-initiated change is reverted'); assert.equal(savedVolume(), 30);
  console.log('youtube 70    ', JSON.stringify(s));

  for (const type of ['mousePressed', 'mouseReleased']) {
    await page.send('Input.dispatchMouseEvent', { type, x: 5, y: 300, button: 'left', clickCount: 1 });
  }
  await page.evaluate(`document.querySelector('#movie_player').setVolume(12)`); await sleep(800); s = await state();
  assert.equal(s.engine, 12); assert.equal(savedVolume(), 12);
  assert.equal(await shell.evaluate(`Number(document.querySelector('#volume').value)`), 12);
  console.log('user 12       ', JSON.stringify(s));

  const before = s.title;
  await page.evaluate(`document.querySelector('#movie_player').nextVideo()`);
  for (let i = 0; i < 20; i++) { await sleep(500); s = await state(); if (s.title !== before && s.playing) break; }
  await sleep(1500); s = await state();
  assert.notEqual(s.title, before); assert.equal(s.engine, 12); assert.ok(!s.muted && s.playing);
  console.log('next track    ', JSON.stringify(s));
  console.log('PASS');
} finally {
  await setTitleBar(original);
  page.close(); shell.close();
}
