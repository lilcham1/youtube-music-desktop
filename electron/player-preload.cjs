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

// The player-bar slider is the page's single source of truth for volume.
// Report its value back so the title-bar control follows changes made inside
// YouTube Music itself. paper-slider's value-change event is composed, and
// aria-valuenow is a plain attribute, so both are visible from this isolated
// world without touching page-defined properties.
let lastReportedVolume;
function reportSliderVolume() {
  const slider = document.querySelector('#volume-slider');
  const value = Number(slider?.getAttribute('aria-valuenow') ?? slider?.getAttribute('value'));
  if (!Number.isFinite(value) || value === lastReportedVolume) return;
  lastReportedVolume = value;
  ipcRenderer.send('player:volume-changed', value);
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
  document.addEventListener('value-change', (event) => {
    if (event.target?.id === 'volume-slider') reportSliderVolume();
  }, true);
  install();
}
if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', start, { once: true });
else start();
