const { ipcRenderer } = require('electron');

const text = (selector) => document.querySelector(selector)?.textContent?.trim() || '';
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

function addNumericVolume() {
  const slider = findInTree('#volume-slider') || findInTree('paper-slider#volume-slider') || findInTree('input[type="range"]');
  let widget = document.getElementById('ytm-desktop-volume');
  if (!widget) {
    widget = document.createElement('label');
    widget.id = 'ytm-desktop-volume';
    widget.title = 'Volume (0 to 100)';
    widget.innerHTML = '<span>VOL</span><input aria-label="Volume, 0 to 100" type="number" min="0" max="100" step="1">';
    document.documentElement.append(widget);
  }
  const input = widget.querySelector('input');
  const clamp = (value) => Math.max(0, Math.min(100, Math.round(Number(value) || 0)));
  const read = () => slider ? clamp(slider.value ?? slider.getAttribute('value')) : 0;
  const sync = () => { if (document.activeElement !== input) input.value = String(read()); };
  if (slider) {
    input.disabled = false;
    input.value = String(read());
    if (!slider.dataset.ytmDesktopVolumeBound) {
      slider.dataset.ytmDesktopVolumeBound = 'true';
      slider.addEventListener('input', sync);
      slider.addEventListener('value-change', sync);
      slider.style.display = 'none';
    }
  } else {
    input.disabled = true;
    input.value = '0';
  }
  input.onchange = () => {
    const value = clamp(input.value);
    input.value = String(value);
    const player = findInTree('ytmusic-player-bar');
    if (typeof player?.updateVolume === 'function') {
      player.updateVolume(value);
    } else if (slider) {
      slider.value = value;
      slider.dispatchEvent(new Event('input', { bubbles: true }));
      slider.dispatchEvent(new Event('change', { bubbles: true }));
    }
  };
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

const style = document.createElement('style');
style.textContent = `
  #ytm-desktop-volume { position:fixed; right:138px; bottom:15px; z-index:2147483647; height:30px; margin:0; padding:0 8px; gap:4px; display:inline-flex; align-items:center; border:1px solid rgba(255,255,255,.16); border-radius:8px; background:rgba(25,25,25,.96); color:#fff; font:700 9px/1 Roboto,Arial,sans-serif; letter-spacing:.7px; box-shadow:0 2px 12px rgba(0,0,0,.35); }
  #ytm-desktop-volume span { color:rgba(255,255,255,.62); }
  #ytm-desktop-volume input { appearance:textfield; width:29px; border:0; outline:0; background:transparent; color:inherit; text-align:center; font:600 12px/1 Roboto,Arial,sans-serif; padding:4px 0; }
  #ytm-desktop-volume input:focus { background:rgba(255,255,255,.12); border-radius:3px; }
  #ytm-desktop-volume input::-webkit-inner-spin-button { appearance:none; display:none; }
  #ytm-desktop-volume input:disabled { opacity:.65; }
`;
document.documentElement.append(style);

new MutationObserver(scheduleInstall).observe(document.documentElement, { childList: true, subtree: true });
if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', install, { once: true });
else install();
