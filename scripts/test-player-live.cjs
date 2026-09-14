// Opt-in test of the installed app using its temporary loopback debugger.
// Starts quiet playback, exercises volume/pause/track changes, and minimizes.
// Volume is on YouTube Music's own slider scale (the PREF cookie value); the
// engine level behind it is nonlinear and is only checked for sign.
const assert = require('node:assert/strict');
const { connect } = require('./player-debug.cjs');
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
const stateExpression = `(() => {
  const video = document.querySelector('video');
  const player = document.querySelector('#movie_player');
  return { title: player?.getVideoData?.()?.title, time: video?.currentTime,
    duration: video?.duration, volume: video?.volume, engine: player?.getVolume?.(),
    slider: Number(document.querySelector('#volume-slider')?.getAttribute('aria-valuenow')),
    pref: /(?:^|; )PREF=([^;]*)/.exec(document.cookie)?.[1] || '',
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
    for (const level of [0, 5, 10, 25, 50, 10]) {
      await shell.evaluate(`window.youtubeMusicShell.setVolume(${level})`);
      await sleep(500);
      const sample = await state();
      assert.equal(sample.slider, level, 'title-bar volume must drive the player-bar slider');
      assert.match(sample.pref, new RegExp(`(^|&)volume=${level}(&|$)`), 'PREF cookie must follow');
      if (level === 0) assert.equal(sample.engine, 0);
      else assert.ok(sample.engine > 0 && sample.volume > 0, `level ${level} must be audible`);
      console.log('numeric-volume', level, 'PASS');
    }
    // The page's own slider must flow back to the title bar.
    await player.evaluate("document.querySelector('ytmusic-player-bar').updateVolume(33)");
    await sleep(500);
    assert.equal(await shell.evaluate("Number(document.getElementById('volume').value)"), 33,
      'in-page slider changes must update the title bar');
    await shell.evaluate('window.youtubeMusicShell.setVolume(10)');
    await sleep(500);
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
        assert.equal(sample.slider, 10, `numeric volume drifted: ${JSON.stringify(sample)}`);
      }
      if (i % 10 === 0) console.log('background-playback', JSON.stringify(sample));
    }
    assert.ok(changedTrack, 'natural track change must occur');
    assert.ok(progressingSamples > 45, 'background playback must keep advancing');
    console.log('PASS: low volumes, zero, slider round-trip, pause, seek, natural track change and 120 seconds minimized playback');
  } finally {
    player.close(); shell.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
