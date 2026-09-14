// Serialized into the page world. Keep this function self-contained.
function applyPlayerVolume(value, unmute = false) {
  const volume = Number(value);
  if (!Number.isFinite(volume)) return false;
  const level = Math.max(0, Math.min(100, Math.round(volume)));
  const player = document.querySelector('#movie_player');
  if (typeof player?.setVolume !== 'function' || typeof player?.getVolume !== 'function') return false;

  // The Music slider uses a nonlinear scale: inputs 1–4 become engine volume
  // zero. Never mix that scale with a direct HTMLMediaElement volume write:
  // the engine would remain at zero and restore silence on later transitions.
  if (unmute && level > 0 && player.isMuted?.()) player.unMute();
  // unMute can restore YouTube's minimum audible level (5). Set the requested
  // level last so unmuting cannot silently turn a numeric 1 into 5.
  player.setVolume(level);
  return player.getVolume() === level;
}

module.exports = { applyPlayerVolume };
