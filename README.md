# YouTube Music Desktop

A compact, personal Windows desktop player for the official YouTube Music website.

> Unofficial personal project. It is not affiliated with, endorsed by, or sponsored by YouTube, Google, or Discord.

Written in Go with [Wails v3](https://v3.wails.io/) on Windows' built-in WebView2 (Edge) runtime. The installer is about 4 MB and the installed app about 13 MB.

## Included

- One window with a custom dark title bar, no browser tab or helper app; it reopens where you left it (size, position, maximised)
- Persistent official YouTube Music sign-in inside the app
- Numeric `0–100` volume control that remembers its level across launches; scroll over it to change it (Shift: steps of 5)
- Playback controls everywhere: the tray menu (with the current song), the taskbar thumbnail (⏮ ⏯ ⏭), keyboard media keys and the Windows media flyout
- Mini player: a small always-on-top window with artwork and controls
- Optional Discord Rich Presence that publishes only while music is playing
  - compact status: artist
  - card: song title, artist, artwork, and position-aware timer
- **Listen along**: friends with this app hear what you play, in sync; join with a code, a link, or the button on your Discord status
- **Follow someone on Spotify**: play along with a friend listening on Spotify (song, position, pauses and skips through your own Discord bot, or song changes through Last.fm)
- **Spotify playlists**: paste a playlist link to play it on YouTube Music as your queue
- **Last.fm scrobbling**
- Startup and minimize/close-to-tray preferences
- Links that leave YouTube Music (YouTube videos, help pages) open in your default browser; sign-in stays in the app
- In-app updates from GitHub releases, verified against the published SHA-512 before installing

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
| `%USERPROFILE%\.youtube-music\settings.json` | App settings (shared with the Electron releases). Last.fm and Spotify secrets and the Discord bot token are stored encrypted with Windows DPAPI |
| `%USERPROFILE%\.youtube-music\webview2\` | YouTube sign-in and site data |
| `%USERPROFILE%\.youtube-music\app.log` | Log for troubleshooting (capped at 1 MB; the previous one is kept as `app.log.1`) |
| `main.go`, `windowstate.go` | Windows, layout, window position |
| `messages.go`, `state.go`, `playback.go` | Page messages, settings state, playback hub, tray and taskbar controls |
| `presence.go`, `internal/discord` | Discord Rich Presence (local IPC, no secret) |
| `scrobble.go`, `internal/lastfm` | Last.fm scrobbling |
| `spotifyqueue.go`, `internal/spotify`, `internal/ytmusic` | Spotify playlists played through YouTube Music |
| `listenalong.go`, `internal/listen` | Listen along (encrypted relay protocol) |
| `follower.go`, `internal/discordbot` | Following someone on Spotify (Discord bot gateway, Last.fm now playing) |
| `internal/taskbar` | Taskbar thumbnail buttons |
| `frontend/player.js` | Script injected into music.youtube.com |
| `frontend/shell.html`, `frontend/settings.html`, `frontend/mini.html` | Title bar, settings and mini player pages |
| `site/` | The listen-along join page, published with GitHub Pages |

The title bar is its own window. The YouTube Music and settings webviews are attached below it as Win32 child windows, so the site's own layout is never modified. Window moving and edge resizing are handled by the title-bar page itself (Wails' own drag/resize script only loads into pages served by its asset server, and these pages are built from strings): it asks the app to start the native move or resize. While the window is not maximised a 6 px frame of that page stays visible around the YouTube view, which is what makes the window resizable from its sides and bottom.

## Volume

The app stores the player *engine* level (`#movie_player.getVolume()`, what you actually hear) and applies it with `#movie_player.setVolume`, which exists in both of YouTube Music's current player UIs. Before playback starts it seeds YouTube's own `PREF` cookie (`volume=N`, on the old slider curve) and reloads once, so the player starts at the saved level from its first frame. If YouTube changes the level on its own it is put back; a change you make in the page (slider, keyboard) is followed and saved. The app never mutes, pauses or plays media and never writes the `<video>` element's volume.

## Tests

```powershell
go test ./...
```

Covers settings migration, the volume curve, encrypted secrets, the Discord IPC protocol (against a fake Discord) and update throttling, update verification, Last.fm signing and scrobbling rules, Spotify playlist reading, YouTube Music search parsing (against a recorded response) and matching, the Spotify queue decisions, listen-along codes, encryption and sync rules, the Discord bot gateway (against a fake gateway) and Spotify presence reading, Last.fm now-playing parsing, taskbar buttons, and invariants of the injected script (never mutes or forces playback on its own; reloads only once at startup).

Tests against the real services are opt-in:

```powershell
go test -tags live -run Live ./internal/listen/ ./internal/ytmusic/ ./internal/discordbot/
```

For a live check, start the app with a loopback debugging port, play something, and run the end-to-end script (requires Node.js):

```powershell
$env:YTM_DEBUG_PORT = 9233; go run .
node scripts\live-test.mjs
```

It verifies the starting level, title bar → page, a YouTube-initiated change being reverted, an in-page change being saved, and a track change, then restores the original level.

## Listen along

Open **Settings → Listen along → Start hosting** and share the code or link. Friends with this app paste it under *Join a friend*, open the link, or (if your Discord Rich Presence is on) click **Listen along** on your Discord status. Their player then follows yours: song, position, pauses, seeks and skips. A guest can leave at any time; when the host stops, guests are told the session ended.

Sync messages go through the public [ntfy.sh](https://ntfy.sh) relay on a random topic. Every message is AES-256-GCM encrypted with a key that only exists in the code, so the relay cannot read what you play, and messages not sealed with the room's key are ignored. Anyone you give the code to can listen until you stop hosting. The host publishes only real changes (about one message per song) to stay well within ntfy.sh's limits for anonymous use; if the relay ever rate-limits, listeners catch up a minute later. Join links use the `ytm-desktop://` link type, which the installer registers.

## Follow someone on Spotify

**Settings → Listen along → Follow someone on Spotify** plays along with a friend who listens on Spotify, even though they don't use this app. Each song they play is looked up on YouTube Music the same way as for playlists; a song that can't be found pauses until their next one. Following stops if you start hosting, join a listen-along room, or play a Spotify playlist. There are two ways:

- **Through Discord (live: song, position, pauses, seeks and skips).** Spotify shares no live listening data with other apps, but Discord shows it on a friend's status when they've connected Spotify to Discord. A regular Discord account can't read that through an API, so the app uses a Discord bot you create once: in the [Discord developer portal](https://discord.com/developers/applications) create an application, turn on **Presence Intent** and **Server Members Intent** under *Bot*, reset and copy the token into Settings, then use **Copy invite link** to add the bot to a server you share with your friends. Everyone in those servers who is playing Spotify then appears in the list with a **Follow** button. The bot only reads statuses and never sends messages; its token stays on your PC.
- **Through Last.fm (song changes only).** For a friend who scrobbles Spotify to Last.fm: enter their username (needs only your Last.fm API key, no sign-in). Their now-playing song is checked every 10 seconds; Last.fm doesn't share position or pauses.

## Spotify playlists

**Settings → Spotify** plays a Spotify playlist on YouTube Music: each song is looked up on YouTube Music (matching title, artist and length) and the matches play in order as your queue, in groups of 50. Songs that can't be found are listed. Nothing is saved to either account.

Any public playlist works without setup: the app reads Spotify's public playlist page (the one Spotify serves for embedding players on websites), which lists up to 100 songs. For longer playlists you can add your own Spotify developer app (client credentials, no Spotify sign-in): create one at the [Spotify developer dashboard](https://developer.spotify.com/dashboard) with any name, `http://127.0.0.1:8888/callback` as redirect URI (unused) and *Web API* ticked, then copy its Client ID and secret into Settings. Spotify only serves its Web API to apps whose owner has an active **Spotify Premium** subscription; without it (or for playlists Spotify blocks for apps) the app falls back to the public page automatically and says so.

## Last.fm

**Settings → Last.fm** scrobbles what you play: a song counts after half its length or four minutes, whichever comes first (songs of 30 seconds or less never count), and Last.fm also shows what you're listening to now. Create a free [API account](https://www.last.fm/api/account/create), paste its API key and shared secret, press **Connect** and approve in the browser. Scrobbles that can't be sent (offline) are queued and sent later.

## Discord Rich Presence

Select the Discord icon in the title bar and enable Rich Presence. No Discord application ID or client secret needs to be entered; the app talks only to the Discord desktop client running on the same PC.
