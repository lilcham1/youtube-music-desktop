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

## Discord Rich Presence

1. Create a Discord application in the [Discord Developer Portal](https://discord.com/developers/applications).
2. Copy its Application ID.
3. In the app, select the Discord icon in the title bar, enable Rich Presence, paste the ID, and save.

No Discord client secret is needed or stored. The app communicates only with the running local Discord desktop client.
