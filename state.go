package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/wailsapp/wails/v3/pkg/application"

	"youtube-music/internal/startup"
	"youtube-music/internal/volume"
)

// applyPlayerVolume sends the saved level to the player page. init is for a
// fresh page load, where the page may reload once to seed YouTube's PREF
// cookie; later changes apply immediately and never reload.
func (d *desktop) applyPlayerVolume(init bool) {
	level := d.config().Volume
	pref, _ := volume.EngineToSlider(float64(level))
	method := "setVolume"
	if init {
		method = "init"
	}
	d.player.ExecJS(fmt.Sprintf("window.__ytmDesktop && window.__ytmDesktop.%s(%d, %d)", method, level, pref))
}

func (d *desktop) state() map[string]any {
	cfg := d.config()
	return map[string]any{
		"volume":           cfg.Volume,
		"discordEnabled":   cfg.DiscordEnabled,
		"minimizeToTray":   cfg.MinimizeToTray,
		"closeToTray":      cfg.CloseToTray,
		"startWithWindows": startup.Enabled(),
		"maximized":        d.shell.IsMaximised(),
		"miniOpen":         cfg.MiniPlayer != nil && cfg.MiniPlayer.Open,
		"version":          strings.TrimSuffix(version, "-dev"),
		"discordStatus":    d.presence.Status(),
		"update":           d.updates.Status(),
		"lastfm":           d.scrobbler.state(),
		"spotify":          d.queue.state(),
		"listen":           d.listen.state(),
		"follow":           d.follow.state(),
	}
}

func (d *desktop) pushState(w *application.WebviewWindow) {
	if w == nil {
		return
	}
	data, _ := json.Marshal(d.state())
	w.ExecJS("window.__app && window.__app.state(" + string(data) + ")")
}

func (d *desktop) pushSettingsState() { d.pushState(d.prefs) }

func (d *desktop) pushAllState() {
	d.pushState(d.shell)
	d.pushSettingsState()
}

// sentence formats an error for the UI: capitalised, ending with a period.
func sentence(err error) string {
	text := strings.TrimSpace(err.Error())
	if text == "" {
		return ""
	}
	r, size := utf8.DecodeRuneInString(text)
	text = string(unicode.ToUpper(r)) + text[size:]
	if !strings.HasSuffix(text, ".") && !strings.HasSuffix(text, "!") && !strings.HasSuffix(text, "?") {
		text += "."
	}
	return text
}
