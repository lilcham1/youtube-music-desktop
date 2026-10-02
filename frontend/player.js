// Injected into music.youtube.com after every page load. It never mutes,
// pauses or plays media and never writes the <video> element's volume.
//
// Volume model: the app stores the player *engine* level (what you hear) and
// applies it with #movie_player.setVolume, which exists in both of YouTube
// Music's player UIs. Before playback starts, the PREF cookie (volume=N, old
// slider curve) is seeded so the player initialises at that loudness.
(() => {
  // The player window also shows Google's sign-in pages; only YouTube Music
  // gets this script.
  if (window.__ytmDesktop || location.origin !== 'https://music.youtube.com') return;
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

  // The media session normally has the track; the page search (which walks
  // shadow roots) only runs when it does not.
  const pageText = (...selectors) => {
    for (const selector of selectors) {
      const text = findInTree(selector)?.textContent?.trim();
      if (text) return text;
    }
    return '';
  };
  const trackDetails = () => {
    const metadata = navigator.mediaSession?.metadata;
    const video = document.querySelector('video') || findInTree('video');
    return {
      playing: Boolean(video && !video.paused && !video.ended && video.readyState > 2),
      title: metadata?.title || pageText('#song-title', '.title.ytmusic-player-bar')
        || document.title.replace(/\s*[-–|]\s*YouTube Music.*$/i, '').trim(),
      artist: metadata?.artist || pageText('#byline', '.byline.ytmusic-player-bar'),
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

  // DOM changes only matter for new <video> elements and the player
  // appearing; playback reports come from the media events above.
  let installQueued = false;
  const scheduleInstall = () => {
    if (installQueued) return;
    installQueued = true;
    setTimeout(() => { installQueued = false; attachVideoEvents(); applyWantedVolume(); }, 150);
  };

  // Links that leave YouTube Music open in the default browser instead of
  // replacing the player or opening a bare popup window. Sign-in and cookie
  // consent stay in the app, because they set this window's session.
  const inAppHosts = new Set(['music.youtube.com', 'accounts.google.com', 'accounts.youtube.com',
    'consent.youtube.com', 'consent.google.com']);
  const isExternal = (href) => {
    try {
      const url = new URL(href, location.href);
      return /^https?:$/.test(url.protocol) && !inAppHosts.has(url.hostname);
    } catch { return false; }
  };
  document.addEventListener('click', (event) => {
    if (!event.isTrusted || event.defaultPrevented || event.button !== 0) return;
    const link = event.target.closest?.('a[href]');
    if (!link || !isExternal(link.href)) return;
    event.preventDefault();
    event.stopImmediatePropagation();
    send('open-external', { url: new URL(link.href, location.href).href });
  }, true);
  const pageOpen = window.open;
  window.open = function (url, ...rest) {
    if (url && isExternal(String(url))) {
      send('open-external', { url: new URL(String(url), location.href).href });
      return null;
    }
    return pageOpen.call(window, url, ...rest);
  };

  const setLevel = (level, prefLevel) => {
    wantedVolume = level;
    lastReportedVolume = level;
    if (prefVolume() !== prefLevel) writePrefVolume(prefLevel);
  };

  window.__ytmDesktop = {
    // level: engine level 0–100. prefLevel: the same loudness on the PREF
    // cookie's slider curve (never 1–4, which YouTube treats as silence).
    //
    // init runs once per page load. If the cookie had a different level it
    // reloads once, before anything has played, so the player initialises
    // at the saved loudness from its first frame.
    init(level, prefLevel) {
      const seeded = prefVolume() === prefLevel;
      setLevel(level, prefLevel);
      const reloadKey = 'ytmDesktopVolumeSeed';
      if (!seeded && prefVolume() === prefLevel && lastUserInput === 0 && !mediaPlaying()
          && sessionStorage.getItem(reloadKey) !== String(prefLevel)) {
        sessionStorage.setItem(reloadKey, String(prefLevel));
        location.reload();
        return;
      }
      applyWantedVolume();
    },
    // setVolume applies a new level immediately and never reloads.
    setVolume(level, prefLevel) {
      setLevel(level, prefLevel);
      applyWantedVolume();
    },
  };

  new MutationObserver(scheduleInstall).observe(document.documentElement, { childList: true, subtree: true });
  attachVideoEvents();
  schedulePlaybackReport();
  send('ready');
})();
