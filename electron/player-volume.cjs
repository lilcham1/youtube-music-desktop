// Volume is stored on YouTube Music's own scale: the 0-100 value shown on its
// player-bar slider. Music persists that value server-side in the PREF cookie
// and reads it while rendering the page, so seeding the cookie before a load
// makes the player start at the saved level from its very first frame. That
// replaces any need to mute a fresh audio stream and un-mute it later.

const YOUTUBE_COOKIE_DOMAIN = '.youtube.com';
const YOUTUBE_COOKIE_URL = 'https://music.youtube.com/';

function clampVolume(value) {
  const volume = Number(value);
  if (!Number.isFinite(volume)) return undefined;
  return Math.max(0, Math.min(100, Math.round(volume)));
}

// PREF is an '&'-separated list of key=value pairs shared by every YouTube
// property (e.g. "f6=40000000&tz=Europe.Paris&volume=42"). Only the volume
// field is ours to change.
function withPrefVolume(existing, volume) {
  const level = clampVolume(volume);
  const fields = String(existing || '')
    .split('&')
    .filter((field) => field && !field.startsWith('volume='));
  if (level !== undefined) fields.push(`volume=${level}`);
  return fields.join('&');
}

// Releases before 0.1.32 stored the player *engine* level (linear gain) and
// wrote it with #movie_player.setVolume. Music's slider maps to that engine
// level through a steep curve (slider 50 ≈ engine 20), so a saved engine value
// must be converted once or every upgrade would sound much quieter. The pairs
// were measured on music.youtube.com; interpolate between them.
const ENGINE_TO_SLIDER = [
  [0, 0], [1, 5], [2, 10], [3, 15], [5, 20], [6, 25], [8, 30], [13, 40],
  [20, 50], [29, 60], [40, 70], [55, 80], [74, 90], [100, 100],
];
function engineToSliderVolume(engine) {
  const level = clampVolume(engine);
  if (level === undefined) return undefined;
  for (let i = 1; i < ENGINE_TO_SLIDER.length; i++) {
    const [e0, s0] = ENGINE_TO_SLIDER[i - 1];
    const [e1, s1] = ENGINE_TO_SLIDER[i];
    if (level <= e1) return Math.round(s0 + ((level - e0) / (e1 - e0)) * (s1 - s0));
  }
  return 100;
}

function prefVolume(pref) {
  const match = /(?:^|&)volume=(\d+)/.exec(String(pref || ''));
  return match ? clampVolume(match[1]) : undefined;
}

// Serialized into the page world. Keep this function self-contained.
// ytmusic-player-bar.updateVolume is the same call the slider makes: it
// updates the bar state, the player engine and the PREF cookie together, so
// nothing in the page can later snap the level back to a stale value.
function applyPlayerVolume(value) {
  const volume = Number(value);
  if (!Number.isFinite(volume)) return false;
  const level = Math.max(0, Math.min(100, Math.round(volume)));
  const bar = document.querySelector('ytmusic-player-bar');
  if (typeof bar?.updateVolume !== 'function') return false;
  bar.updateVolume(level);
  return true;
}

module.exports = {
  YOUTUBE_COOKIE_DOMAIN,
  YOUTUBE_COOKIE_URL,
  clampVolume,
  withPrefVolume,
  prefVolume,
  engineToSliderVolume,
  applyPlayerVolume,
};
