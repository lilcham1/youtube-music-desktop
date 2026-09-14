# YouTube Music Desktop

A compact, personal Windows desktop player for the official YouTube Music website.

> Unofficial personal project. It is not affiliated with, endorsed by, or sponsored by YouTube, Google, or Discord.

## Included

- One native Electron window, with no browser tab or second visible helper app
- Persistent official YouTube Music sign-in inside the app
- Numeric `0–100` volume control
- System-tray controls: open, settings, Discord settings, and quit
- Startup and minimize/close-to-tray preferences
- Optional Discord Rich Presence that publishes only while music is playing
  - compact status: artist
  - card: song title, artist, artwork, and position-aware timer

## Requirements

- Windows 10 or 11
- Node.js 20+
- Discord desktop client, only if Rich Presence is enabled

## Run locally

```powershell
npm install
npm run desktop
```

## Build an installer

```powershell
npm run package:desktop
```

The NSIS installer is created in `dist/`.

## Playback regression checks

Run `npm test` for volume mapping, mute behavior, preload readiness, and profile
persistence tests. Numeric volume is applied through the playback engine, not
YouTube Music's nonlinear slider. Per-song loudness normalization remains intact.

For an opt-in live test, close the app, launch it with
`--remote-debugging-address=127.0.0.1 --remote-debugging-port=9231`, load a song,
then run `node scripts/test-player-live.cjs`. This starts playback, tests levels
0–4, mute/pause, seeking, a track transition and two minutes of minimized
playback. It leaves the saved volume at 1. Close and reopen normally afterward
to disable debugging. Debugging is never enabled by the app itself.

## Discord Rich Presence

1. Create a Discord application in the [Discord Developer Portal](https://discord.com/developers/applications).
2. Copy its Application ID.
3. In the app, select the Discord icon in the title bar, enable Rich Presence, paste the ID, and save.

No Discord client secret is needed or stored. The app communicates only with the running local Discord desktop client.
