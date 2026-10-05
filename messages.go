package main

import (
	"context"
	"encoding/json"
	"log"
	"net/url"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"

	"youtube-music/internal/settings"
	"youtube-music/internal/startup"
	"youtube-music/internal/updater"
)

type inbound struct {
	Type     string          `json:"type"`
	Value    *float64        `json:"value"`
	URL      string          `json:"url"`
	Edge     string          `json:"edge"`
	Text     string          `json:"text"`
	Name     string          `json:"name"`
	Settings json.RawMessage `json:"settings"`
	Playback
}

// onMessage handles postMessage calls from the pages. Each page may only
// send the message types that belong to it.
func (d *desktop) onMessage(window application.Window, message string, origin *application.OriginInfo) {
	var msg inbound
	if err := json.Unmarshal([]byte(message), &msg); err != nil {
		return
	}
	switch window.Name() {
	case "player":
		// Only the YouTube Music page itself, not frames (ads) inside it.
		if !strings.HasPrefix(origin.Origin, musicURL) {
			return
		}
		d.onPlayerMessage(msg)
	case "shell":
		d.onShellMessage(msg)
	case "settings":
		d.onSettingsMessage(msg)
	case "mini":
		d.onMiniMessage(msg)
	}
}

func (d *desktop) onPlayerMessage(msg inbound) {
	switch msg.Type {
	case "ready":
		d.applyPlayerVolume(true)
		d.mu.Lock()
		d.playerReady = true
		join := d.pendingJoin
		d.pendingJoin = ""
		d.mu.Unlock()
		if join != "" {
			go d.joinFromLink(join)
		} else {
			go d.listen.ensureAuto()
		}
	case "open-external":
		d.openExternal(msg.URL)
	case "playback":
		d.onPlayback(msg.Playback)
	case "volume-changed":
		if msg.Value == nil {
			return
		}
		d.update(func(s *settings.Settings) { s.SetVolume(*msg.Value) })
		// Re-applying writes the matching PREF value for the next launch; the
		// engine already has this level, so nothing audible changes.
		d.applyPlayerVolume(false)
		d.pushAllState()
	}
}

func (d *desktop) onShellMessage(msg inbound) {
	switch msg.Type {
	case "ready":
		d.pushState(d.shell)
	case "volume":
		if msg.Value != nil {
			d.update(func(s *settings.Settings) { s.SetVolume(*msg.Value) })
			d.applyPlayerVolume(false)
		}
		d.pushAllState()
	case "open-settings":
		d.openSettings("general")
	case "open-discord":
		d.openSettings("discord")
	case "open-listen":
		d.openSettings("listen")
	case "toggle-mini":
		d.toggleMini()
	case "drag":
		// Wails starts the native move (and hops to the UI thread itself).
		d.shell.HandleMessage("wails:drag")
	case "resize":
		if resizeEdges[msg.Edge] && !d.shell.IsMaximised() {
			d.shell.HandleMessage("wails:resize:" + msg.Edge)
		}
	case "minimize":
		d.shell.Minimise()
	case "maximize":
		d.shell.ToggleMaximise()
	case "close":
		d.shell.Close()
	}
}

func (d *desktop) onMiniMessage(msg inbound) {
	switch msg.Type {
	case "ready":
		d.pushNowPlaying()
	case "command":
		if userCommands[msg.Name] {
			d.command(msg.Name, nil)
		}
	case "drag":
		d.mini.HandleMessage("wails:drag")
	case "open-main":
		d.showMain()
	case "close":
		d.hideMini()
	}
}

func (d *desktop) onSettingsMessage(msg inbound) {
	switch msg.Type {
	case "ready":
		d.pushSettingsState()
	case "close-settings":
		d.closeSettings()
	case "quit":
		d.quit()
	case "open-external":
		d.openExternal(msg.URL)
	case "copy":
		if msg.Text != "" {
			d.app.Clipboard.SetText(msg.Text)
		}
	case "check-updates":
		go d.updates.Check(context.Background())
	case "install-update":
		if d.updates.Status().State == updater.Downloaded {
			if err := d.updates.Install(); err == nil {
				d.quit()
			}
		}
	case "save":
		d.saveGeneral(msg.Settings)
	case "listen-host":
		d.listen.Host()
	case "listen-join":
		go d.listen.Join(msg.Text)
	case "listen-stop":
		go d.listen.StopByUser()
	}
}

func (d *desktop) saveGeneral(raw json.RawMessage) {
	var next struct {
		DiscordEnabled     *bool `json:"discordEnabled"`
		DiscordListenAlong *bool `json:"discordListenAlong"`
		MinimizeToTray     *bool `json:"minimizeToTray"`
		CloseToTray        *bool `json:"closeToTray"`
		StartWithWindows   *bool `json:"startWithWindows"`
	}
	if json.Unmarshal(raw, &next) != nil {
		return
	}
	cfg := d.update(func(s *settings.Settings) {
		if next.DiscordEnabled != nil {
			s.DiscordEnabled = *next.DiscordEnabled
		}
		if next.DiscordListenAlong != nil {
			s.DiscordListenAlong = *next.DiscordListenAlong
		}
		if next.MinimizeToTray != nil {
			s.MinimizeToTray = *next.MinimizeToTray
		}
		if next.CloseToTray != nil {
			s.CloseToTray = *next.CloseToTray
		}
		if next.StartWithWindows != nil {
			s.StartWithWindows = *next.StartWithWindows
		}
	})
	if next.StartWithWindows != nil {
		if err := startup.Set(d.exe, *next.StartWithWindows); err != nil {
			log.Printf("start with windows: %v", err)
		}
	}
	d.presence.Configure(cfg.DiscordEnabled, cfg.DiscordAppID)
	if next.DiscordListenAlong != nil {
		d.listen.autoSettingChanged()
	}
	if next.DiscordEnabled != nil || next.DiscordListenAlong != nil {
		go d.listen.ensureAuto()
	}
	d.pushAllState()
}

// openExternal opens a web link in the default browser. Only plain http(s)
// URLs are accepted.
func (d *desktop) openExternal(raw string) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return
	}
	if err := d.app.Browser.OpenURL(u.String()); err != nil {
		log.Printf("open %s: %v", u, err)
	}
}
