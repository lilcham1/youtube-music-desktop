# YouTube Music Desktop

A compact, personal Windows desktop player for the official YouTube Music website.

> Unofficial personal project. It is not affiliated with, endorsed by, or sponsored by YouTube, Google, or Discord.

Written in Go with [Wails v3](https://v3.wails.io/) on Windows' built-in WebView2 (Edge) runtime. The installer is about 4 MB and the installed app about 13 MB.

## Included

- One window with a custom dark title bar, no browser tab or helper app
- Persistent official YouTube Music sign-in inside the app
- Numeric `0–100` volume control that remembers its level across launches
- System-tray controls: open, settings, Discord settings, and quit
- Startup and minimize/close-to-tray preferences
- Optional Discord Rich Presence that publishes only while music is playing
  - compact status: artist
  - card: song title, artist, artwork, and position-aware timer
- Links that leave YouTube Music (YouTube videos, help pages) open in your default browser; sign-in stays in the app
- In-app updates from GitHub releases

## Requirements

- Windows 10 or 11 with the WebView2 runtime (built into Windows 11)
- Go 1.25+ to build
- Discord desktop client, only if Rich Presence is enabled

## Run locally

```powershell
go run .
```

## Build an installer

Needs [go-winres](https://github.com/tc-hib/go-winres) (`go install github.com/tc-hib/go-winres@latest`) and [NSIS](https://nsis.sourceforge.io/) (`winget install NSIS.NSIS`).

```powershell
.\build.ps1 -Version 0.2.0
```

This runs the tests, then writes `dist\YouTube Music.exe`, `dist\YouTube-Music-Setup-0.2.0.exe` and `dist\latest.yml`. The installer is per-user, installs to `%LOCALAPPDATA%\Programs\YouTube Music` and replaces an Electron 0.1.x install in place. Pushing a `v*` tag builds and publishes a release from GitHub Actions.

## Where things live

| Path | Contents |
|---|---|
| `%USERPROFILE%\.youtube-music\settings.json` | App settings (shared with the Electron releases) |
| `%USERPROFILE%\.youtube-music\webview2\` | YouTube sign-in and site data |
| `main.go` | Windows, tray, settings and messaging |
| `presence.go`, `internal/discord` | Discord Rich Presence (local IPC, no secret) |
| `frontend/player.js` | Script injected into music.youtube.com |
| `frontend/shell.html`, `frontend/settings.html` | Title bar and settings pages |

The title bar is its own window. The YouTube Music and settings webviews are attached below it as Win32 child windows, so the site's own layout is never modified. Window moving and edge resizing are handled by the title-bar page itself (Wails' own drag/resize script only loads into pages served by its asset server, and these pages are built from strings): it asks the app to start the native move or resize. While the window is not maximised a 6 px frame of that page stays visible around the YouTube view, which is what makes the window resizable from its sides and bottom.

## Volume

The app stores the player *engine* level (`#movie_player.getVolume()`, what you actually hear) and applies it with `#movie_player.setVolume`, which exists in both of YouTube Music's current player UIs. Before playback starts it seeds YouTube's own `PREF` cookie (`volume=N`, on the old slider curve) and reloads once, so the player starts at the saved level from its first frame. If YouTube changes the level on its own it is put back; a change you make in the page (slider, keyboard) is followed and saved. The app never mutes, pauses or plays media and never writes the `<video>` element's volume.

## Tests

```powershell
go test ./...
```

Covers settings migration from every Electron release, the volume curve, the Discord IPC protocol (against a fake Discord) and update throttling, the updater, and invariants of the injected script (never mutes or forces playback; reloads only once at startup).

For a live check, start the app with a loopback debugging port, play something, and run the end-to-end script (requires Node.js):

```powershell
$env:YTM_DEBUG_PORT = 9233; go run .
node scripts\live-test.mjs
```

It verifies the starting level, title bar → page, a YouTube-initiated change being reverted, an in-page change being saved, and a track change, then restores the original level.

## Discord Rich Presence

Select the Discord icon in the title bar and enable Rich Presence. No Discord application ID or client secret needs to be entered; the app talks only to the Discord desktop client running on the same PC.
