// Opt-in test of the installed app using its temporary loopback debugger.
// Starts quiet playback, exercises volume/mute/pause/track changes, and minimizes.
const assert = require('node:assert/strict');
const { connect } = require('./player-debug.cjs');
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
const stateExpression = `(() => {
  const video = document.querySelector('video');
  const player = document.querySelector('#movie_player');
  return { title: player?.getVideoData?.()?.title, time: video?.currentTime,
    duration: video?.duration, volume: video?.volume, engine: player?.getVolume?.(),
    muted: video?.muted, paused: video?.paused, ready: video?.readyState,
    error: video?.error?.code || null };
})()`;

(async () => {
  const player = await connect();
  const shell = await connect('shell');
  const state = () => player.evaluate(stateExpression);
  const api = expression => player.evaluate(`(() => { const p = document.querySelector('#movie_player'); ${expression} })()`);
  try {
    const initial = await state();
    assert.ok(initial.duration > 0, 'a song must be loaded');
    await api('p.pauseVideo();');
    for (const level of [0, 1, 2, 3, 4, 1]) {
      await shell.evaluate(`window.youtubeMusicShell.setVolume(${level})`);
      await sleep(500);
      const sample = await state();
      assert.equal(sample.engine, level);
      // YouTube may attenuate individual songs for loudness normalization.
      if (level === 0) assert.equal(sample.volume, 0);
      else assert.ok(sample.volume > 0 && sample.volume <= level / 100 + 0.000001);
      console.log('numeric-volume', level, 'PASS');
    }
    await api('p.mute();');
    await sleep(500);
    assert.equal((await state()).muted, true);
    await api('p.playVideo();');
    await sleep(1000);
    assert.equal((await state()).muted, true, 'lifecycle volume sync must preserve deliberate mute');
    await shell.evaluate('window.youtubeMusicShell.setVolume(1)');
    await sleep(500);
    assert.equal((await state()).muted, false);
    await api('p.pauseVideo();');
    await sleep(2000);
    assert.equal((await state()).paused, true, 'a deliberate pause must stay paused');
    await api('p.playVideo();');
    await sleep(2000);
    const beforeTransition = await state();
    await api('p.seekTo(p.getDuration() - 4, true);');
    await shell.evaluate('window.youtubeMusicShell.minimize()');
    let changedTrack = false;
    let lastTime = null;
    let progressingSamples = 0;
    let zeroSince = null;
    for (let i = 0; i < 60; i++) {
      await sleep(2000);
      const sample = await state();
      changedTrack ||= !!sample.title && sample.title !== beforeTransition.title;
      if (lastTime !== null && sample.time > lastTime) progressingSamples++;
      lastTime = sample.time;
      if (!sample.paused && sample.ready >= 3 && (sample.volume === 0 || sample.engine === 0 || sample.muted)) {
        zeroSince ??= Date.now();
        assert.ok(Date.now() - zeroSince < 4000, `sustained silence: ${JSON.stringify(sample)}`);
      } else zeroSince = null;
      assert.equal(sample.error, null);
      if (sample.ready >= 3 && !sample.paused) {
        assert.equal(sample.engine, 1, `numeric volume drifted: ${JSON.stringify(sample)}`);
      }
      if (i % 10 === 0) console.log('background-playback', JSON.stringify(sample));
    }
    assert.ok(changedTrack, 'natural track change must occur');
    assert.ok(progressingSamples > 45, 'background playback must keep advancing');
    console.log('PASS: low volumes, zero, mute, pause, seek, natural track change and 120 seconds minimized playback');
  } finally {
    player.close(); shell.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
