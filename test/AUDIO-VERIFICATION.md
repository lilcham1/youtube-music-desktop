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
