# Audio verification — 0.1.32

Investigated on 2026-09-14 against music.youtube.com in an Electron 44 view.

## Reported

- Audio disappeared mid-session while the track position kept advancing.
- Each launch started loud and then dropped to the saved level, so the Windows
  volume mixer had been lowered to compensate.

## Root cause

- 0.1.29–0.1.31 muted the whole webContents (`setAudioMuted(true)`) on load and
  waited for the page to report a `volume-ready` event before un-muting. That
  event depends on `<video>` lifecycle events that YouTube does not always fire
  for a re-used media element, so the un-mute could be missed and the stream
  stayed silent. A time-based fail-safe only papered over the ordering problem.
- The loud start happened because the page's player initialised at YouTube's
  own remembered level (often 100) and the app corrected it only after
  `did-finish-load`, hundreds of milliseconds into playback.
- Writing `#movie_player.setVolume` directly changes the engine but not the
  player bar's state or its persisted value (`PREF` cookie), so the page could
  later snap back to a different level. `ytmusic-player-bar.volume` is read-only
  from outside; `updateVolume(n)` is the one call that updates bar, engine and
  cookie together.

## Measured behaviour (music.youtube.com, 2026-09-14)

- Volume is persisted in the `PREF` cookie on `.youtube.com` as `volume=N`
  (N = slider value), not in localStorage. Seeding `PREF=volume=77` and
  reloading produced bar 77 / engine 50 on the first sample after load.
- Slider→engine is nonlinear: 5→1, 10→2, 20→5, 30→8, 40→13, 50→20, 60→29,
  70→40, 80→55, 90→74, 100→100; 1–4 map to engine 0.
- `paper-slider` dispatches a composed `value-change` event and reflects
  `value` / `aria-valuenow` as attributes, so an isolated-world preload can
  observe in-page volume changes without touching page-defined properties.

## Changes

- Volume is stored on the slider scale. Existing engine-scale settings are
  converted once at startup (`volumeScale: 'slider'`) using the measured
  curve, so the upgrade keeps the same loudness.
- The saved level is written into the `PREF` cookie before the player page is
  loaded and again whenever it changes. Other PREF fields are preserved.
- Live changes go through `ytmusic-player-bar.updateVolume`.
- In-page slider changes are reported back and saved, so the title bar, the
  page and the next launch agree.
- All `setAudioMuted` calls, the navigation-triggered guard, the
  `media-attached` / `volume-ready` signals and the fail-safe timer are gone.
  Nothing in the app can silence the stream any more.

## Verification

- 9 automated tests pass (`npm test`): PREF merging, migration monotonicity,
  bar-API routing, no watchdog/mute listeners in the preload, slider
  round-trip reporting, DOM-readiness, profile persistence.
- Live check in the packaged app: see `scripts/test-player-live.cjs`.

After installing, reset the app's level in the Windows volume mixer to 100 %
and use the title-bar VOL control; the mixer workaround is no longer needed.

---

# Audio dropout verification — 0.1.29

Tested on Windows against the installed application on 2026-09-10.

## Reproduced before the fix

- YouTube Music's player-bar `updateVolume(1..4)` produced engine volume `0`.
  Calling the engine's `setVolume(1)` instead produced engine `1` and media `0.01`.
- The old implementation called the nonlinear bar handler and then assigned the
  media element directly. Its engine stayed at `0` while media briefly read `0.01`.
- During real playback, the media volume fell to `0` at time `74.506`, with
  `paused=false`, `muted=false`, and engine volume `0`. Playback continued
  advancing to `107.925` while still at zero. This was an observed failure, not
  merely a simulated media reset.
- The preload's observer could be started before `document.documentElement`
  existed. The installed media had no playback-listener marker before the fix;
  after deferring startup until DOM readiness, the marker was present.
- Unmuting after setting volume could restore YouTube's minimum level `5`.
  The requested volume is now set after unmuting.

## Changes

- Use the engine's exact 0–100 volume API. No synthetic slider events or direct
  media volume writes in production code.
- Initialize playback observers only after the document exists.
- Reapply saved volume when media attaches, metadata loads, or playback starts;
  lifecycle restores preserve deliberate mute.
- Remove the speculative timers that rewrote element volume or retried play.
- Preserve YouTube's per-song loudness normalization.

## Verification results

- Eight automated regression tests passed, including all 101 numeric levels,
  unmute ordering, DOM-startup readiness, and shared-profile persistence.
- Installed-app live test passed: numeric 0–4, explicit mute, unmute back to 1,
  deliberate pause, seek, natural song transition, and 120 seconds minimized.
- Background samples retained engine volume `1`; media remained positive
  (`0.01`, or approximately `0.009322` for a normalized track), playback advanced,
  and there were no media errors or sustained zero-volume events.
- Built and installed locally; no GitHub release was published by this fix.

This validates the reproduced application-level volume failure. It is not a
guarantee against unrelated network outages or audio-device failures. Physical
speaker output was not independently measured.
