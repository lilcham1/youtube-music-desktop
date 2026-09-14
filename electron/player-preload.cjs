const { ipcRenderer } = require('electron');

let lastPayload = '';
let reportTimer;

function trackDetails() {
  const metadata = navigator.mediaSession?.metadata;
  const video = findInTree('video');
  const playerTitle = findInTree('#song-title') || findInTree('.title.ytmusic-player-bar');
  const playerByline = findInTree('#byline') || findInTree('.byline.ytmusic-player-bar');
  const pageTitle = document.title.replace(/\s*[-–|]\s*YouTube Music.*$/i, '').trim();
  const title = metadata?.title || playerTitle?.textContent?.trim() || pageTitle;
  const artist = metadata?.artist || playerByline?.textContent?.trim() || '';
  const album = metadata?.album || '';
  const artwork = metadata?.artwork?.[metadata.artwork.length - 1]?.src || '';
  return {
    playing: Boolean(video && !video.paused && !video.ended && video.readyState > 2),
    title,
    artist,
    album,
    artwork,
    positionSeconds: video?.currentTime || 0,
    durationSeconds: Number.isFinite(video?.duration) ? video.duration : 0,
  };
}

function reportPlayback() {
  reportTimer = undefined;
  const payload = trackDetails();
  const fingerprint = JSON.stringify(payload);
  if (fingerprint === lastPayload) return;
  lastPayload = fingerprint;
  ipcRenderer.send('playback', payload);
}

function schedulePlaybackReport() {
  if (reportTimer) clearTimeout(reportTimer);
  reportTimer = setTimeout(reportPlayback, 100);
}

function attachVideoEvents() {
  document.querySelectorAll('video').forEach((video) => {
    if (video.dataset.ytmDesktopPlayback === 'true') return;
    video.dataset.ytmDesktopPlayback = 'true';
    // A new media element or track can arrive after did-finish-load. Restore
    // through the player engine, never through an independent volume watchdog.
    ipcRenderer.send('player:media-attached');
    const syncVolume = () => ipcRenderer.send('player:volume-ready');
    // loadedmetadata happens before audible playback. The main process keeps
    // the Chromium stream muted until this exact volume sync completes.
    ['loadedmetadata', 'playing'].forEach((event) => video.addEventListener(event, syncVolume));
    ['play', 'playing', 'pause', 'ended', 'loadedmetadata', 'emptied', 'seeking', 'seeked', 'ratechange'].forEach((event) => {
      video.addEventListener(event, schedulePlaybackReport);
    });
  });
}

function findInTree(selector, root = document) {
  const direct = root.querySelector?.(selector);
  if (direct) return direct;
  for (const element of root.querySelectorAll?.('*') || []) {
    if (element.shadowRoot) {
      const nested = findInTree(selector, element.shadowRoot);
      if (nested) return nested;
    }
  }
  return null;
}

function install() {
  attachVideoEvents();
  schedulePlaybackReport();
}

let installQueued = false;
function scheduleInstall() {
  if (installQueued) return;
  installQueued = true;
  setTimeout(() => {
    installQueued = false;
    install();
  }, 150);
}

function start() {
  // Electron preloads run before the page DOM exists. Observing a null root
  // throws and prevents every playback/volume listener from being installed.
  new MutationObserver(scheduleInstall).observe(document.documentElement, { childList: true, subtree: true });
  install();
}
if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', start, { once: true });
else start();
