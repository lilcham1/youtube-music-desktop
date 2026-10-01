// Injected into music.youtube.com after every page load. It never mutes,
// pauses or plays media and never writes the <video> element's volume.
//
// Volume model: the app stores the player *engine* level (what you hear) and
// applies it with #movie_player.setVolume, which exists in both of YouTube
// Music's player UIs. Before playback starts, the PREF cookie (volume=N, old
// slider curve) is seeded so the player initialises at that loudness.
(() => {
  if (window.__ytmDesktop) return;
  const send = (type, extra) => window.chrome.webview.postMessage(JSON.stringify({ type, ...extra }));

  const findInTree = (selector, root = document) => {
    const direct = root.querySelector?.(selector);
    if (direct) return direct;
    for (const element of root.querySelectorAll?.('*') || []) {
      if (element.shadowRoot) {
        const nested = findInTree(selector, element.shadowRoot);
        if (nested) return nested;
      }
    }
    return null;
  };

  // ---- Playback reporting (Discord Rich Presence) ----

  const trackDetails = () => {
    const metadata = navigator.mediaSession?.metadata;
    const video = findInTree('video');
    const playerTitle = findInTree('#song-title') || findInTree('.title.ytmusic-player-bar');
    const playerByline = findInTree('#byline') || findInTree('.byline.ytmusic-player-bar');
    const pageTitle = document.title.replace(/\s*[-–|]\s*YouTube Music.*$/i, '').trim();
    return {
      playing: Boolean(video && !video.paused && !video.ended && video.readyState > 2),
      title: metadata?.title || playerTitle?.textContent?.trim() || pageTitle,
      artist: metadata?.artist || playerByline?.textContent?.trim() || '',
      album: metadata?.album || '',
      artwork: metadata?.artwork?.[metadata.artwork.length - 1]?.src || '',
      positionSeconds: video?.currentTime || 0,
      durationSeconds: Number.isFinite(video?.duration) ? video.duration : 0,
    };
  };

  let lastPayload = '';
  let reportTimer;
  const reportPlayback = () => {
    reportTimer = undefined;
    const payload = trackDetails();
    const fingerprint = JSON.stringify(payload);
    if (fingerprint === lastPayload) return;
    lastPayload = fingerprint;
    send('playback', payload);
  };
  const schedulePlaybackReport = () => {
    clearTimeout(reportTimer);
    reportTimer = setTimeout(reportPlayback, 100);
  };

  // ---- Volume ----

  const player = () => document.querySelector('#movie_player');
  // The newer mini player's slider is linear: its value is the engine level.
  const linearSlider = () => document.querySelector('input[type=range][aria-label="Volume"]');

  const prefCookie = () => (/(?:^|; )PREF=([^;]*)/.exec(document.cookie) || [])[1] || '';
  const prefVolume = () => {
    const match = /(?:^|&)volume=(\d+)/.exec(prefCookie());
    return match ? Number(match[1]) : undefined;
  };
  const writePrefVolume = (level) => {
    const fields = prefCookie().split('&').filter((f) => f && !f.startsWith('volume='));
    fields.push('volume=' + level);
    const expires = new Date(Date.now() + 2 * 365 * 864e5).toUTCString();
    document.cookie = `PREF=${fields.join('&')}; domain=.youtube.com; path=/; secure; expires=${expires}`;
  };
  const mediaPlaying = () => [...document.querySelectorAll('video')].some((v) => !v.paused && !v.ended);

  let wantedVolume;
  let lastReportedVolume;

  // Sets the engine level and keeps the visible linear slider in step. The
  // user's own mute is left alone: a muted player keeps its level.
  const applyWantedVolume = () => {
    const mp = player();
    if (wantedVolume === undefined || typeof mp?.setVolume !== 'function') return;
    if (mp.getVolume() !== wantedVolume) mp.setVolume(wantedVolume);
    const slider = linearSlider();
    if (slider && Number(slider.value) !== wantedVolume) {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(slider, String(wantedVolume));
      slider.style.setProperty('--yt-slider-shape-gradient-percent', wantedVolume + '%');
      slider.setAttribute('aria-valuenow', String(wantedVolume));
      slider.setAttribute('aria-valuetext', String(wantedVolume));
    }
  };

  // Only a change that follows a real click, key press or wheel is the
  // user's. Anything else is YouTube re-applying a stale level of its own.
  let lastUserInput = 0;
  ['pointerdown', 'pointerup', 'keydown', 'wheel'].forEach((type) => {
    document.addEventListener(type, (event) => { if (event.isTrusted) lastUserInput = Date.now(); }, true);
  });

  // Follows a level the user changed inside the page (either slider, or a
  // keyboard shortcut) so the title bar and saved settings agree; restores
  // the saved level if YouTube changed it on its own.
  const onVolumeChange = () => {
    const mp = player();
    if (typeof mp?.getVolume !== 'function' || mp.isMuted?.()) return;
    const level = mp.getVolume();
    if (!Number.isFinite(level) || level === wantedVolume) return;
    if (Date.now() - lastUserInput > 2000) {
      applyWantedVolume();
      return;
    }
    if (level === lastReportedVolume) return;
    lastReportedVolume = level;
    wantedVolume = level;
    send('volume-changed', { value: level });
  };

  const attachVideoEvents = () => {
    document.querySelectorAll('video').forEach((video) => {
      if (video.dataset.ytmDesktopPlayback === 'true') return;
      video.dataset.ytmDesktopPlayback = 'true';
      ['play', 'playing', 'pause', 'ended', 'loadedmetadata', 'emptied', 'seeking', 'seeked', 'ratechange'].forEach((event) => {
        video.addEventListener(event, schedulePlaybackReport);
      });
      // A newly loaded track keeps the saved level.
      video.addEventListener('loadedmetadata', applyWantedVolume);
      video.addEventListener('volumechange', () => setTimeout(onVolumeChange, 0));
    });
  };

  let installQueued = false;
  const scheduleInstall = () => {
    if (installQueued) return;
    installQueued = true;
    setTimeout(() => { installQueued = false; attachVideoEvents(); applyWantedVolume(); schedulePlaybackReport(); }, 150);
  };

  window.__ytmDesktop = {
    // level: engine level 0–100. prefLevel: the same loudness on the PREF
    // cookie's slider curve (never 1–4, which YouTube treats as silence).
    setVolume(level, prefLevel) {
      wantedVolume = level;
      lastReportedVolume = level;
      if (prefVolume() !== prefLevel) {
        writePrefVolume(prefLevel);
        const reloadKey = 'ytmDesktopVolumeSeed';
        // Reload once so the player initialises from the new cookie, but
        // never interrupt something that is already playing.
        if (prefVolume() === prefLevel && !mediaPlaying() && sessionStorage.getItem(reloadKey) !== String(prefLevel)) {
          sessionStorage.setItem(reloadKey, String(prefLevel));
          location.reload();
          return;
        }
      }
      applyWantedVolume();
    },
  };

  new MutationObserver(scheduleInstall).observe(document.documentElement, { childList: true, subtree: true });
  attachVideoEvents();
  schedulePlaybackReport();
  send('ready');
})();
