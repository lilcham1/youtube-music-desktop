package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/wailsapp/wails/v3/pkg/application"

	"youtube-music/internal/taskbar"
)

// onPlayback fans a playback report out to everything that follows it.
func (d *desktop) onPlayback(pb Playback) {
	d.mu.Lock()
	d.now, d.nowAt = pb, time.Now()
	d.mu.Unlock()
	d.presence.Update(pb)
	d.listen.hostObserve(pb)
	d.tray.update(pb)
	application.InvokeAsync(func() { d.thumbs.SetPlaying(pb.Playing) })
	d.pushNowPlaying()
}

func (d *desktop) nowPlaying() (Playback, time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.now, d.nowAt
}

// command asks the player page to act on the user's behalf (play/pause,
// skip, seek, play a song or playlist, follow a listen-along host).
func (d *desktop) command(name string, arg any) {
	data, err := json.Marshal(arg)
	if err != nil {
		return
	}
	d.player.ExecJS(fmt.Sprintf("window.__ytmDesktop && window.__ytmDesktop.command(%q, %s)", name, data))
}

// userCommands are the playback controls the tray, taskbar and mini player
// may send.
var userCommands = map[string]bool{"playPause": true, "next": true, "previous": true}

// pushNowPlaying sends the current track to the mini player, which ticks the
// position locally between reports.
func (d *desktop) pushNowPlaying() {
	if d.mini == nil || !d.mini.IsVisible() {
		return
	}
	pb, at := d.nowPlaying()
	data, _ := json.Marshal(map[string]any{
		"title": pb.Title, "artist": pb.Artist, "artwork": pb.Artwork, "playing": pb.Playing,
		"position": pb.PositionSeconds, "duration": pb.DurationSeconds, "ageMs": time.Since(at).Milliseconds(),
	})
	d.mini.ExecJS("window.__app && window.__app.nowPlaying(" + string(data) + ")")
}

// wndProc watches the main window's messages for the taskbar thumbnail
// buttons. It runs on the UI thread for every message, so it stays cheap.
func (d *desktop) wndProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) (uintptr, bool) {
	if d.shell == nil || hwnd != uintptr(d.shell.NativeWindow()) {
		return 0, false
	}
	if msg == taskbar.ButtonCreatedMessage() {
		if err := d.thumbs.Add(hwnd); err != nil {
			log.Printf("taskbar buttons: %v", err)
		}
		pb, _ := d.nowPlaying()
		d.thumbs.SetPlaying(pb.Playing)
		return 0, false
	}
	if id, ok := taskbar.Clicked(msg, wParam); ok {
		switch id {
		case taskbar.ButtonPrevious:
			go d.command("previous", nil)
		case taskbar.ButtonPlayPause:
			go d.command("playPause", nil)
		case taskbar.ButtonNext:
			go d.command("next", nil)
		}
		return 0, true
	}
	return 0, false
}

// trayControls owns the notification-area icon and its menu.
type trayControls struct {
	d                                               *desktop
	tray                                            *application.SystemTray
	menu                                            *application.Menu
	nowItem, playItem, nextItem, prevItem, miniItem *application.MenuItem

	mu      sync.Mutex
	tooltip string
	label   string
	playing bool
}

func newTrayControls(d *desktop) *trayControls {
	t := &trayControls{d: d, menu: application.NewMenu()}
	t.nowItem = t.menu.Add("Nothing playing").SetEnabled(false)
	t.playItem = t.menu.Add("Play").OnClick(func(*application.Context) { go d.command("playPause", nil) })
	t.nextItem = t.menu.Add("Next").OnClick(func(*application.Context) { go d.command("next", nil) })
	t.prevItem = t.menu.Add("Previous").OnClick(func(*application.Context) { go d.command("previous", nil) })
	t.menu.AddSeparator()
	t.menu.Add("Open Encore").OnClick(func(*application.Context) { d.showMain() })
	t.miniItem = t.menu.AddCheckbox("Mini player", false).OnClick(func(*application.Context) { d.toggleMini() })
	t.menu.Add("Listen along…").OnClick(func(*application.Context) { d.openSettings("listen") })
	t.menu.Add("Settings…").OnClick(func(*application.Context) { d.openSettings("general") })
	t.menu.AddSeparator()
	t.menu.Add("Quit Encore").OnClick(func(*application.Context) { d.quit() })

	t.tray = d.app.SystemTray.New()
	t.tray.SetIcon([]byte(mustRead("assets/icon.ico")))
	t.tray.SetTooltip("Encore")
	t.tray.SetMenu(t.menu)
	t.tray.OnClick(d.showMain)
	t.tray.OnDoubleClick(d.showMain)
	return t
}

// trayText shortens s to n characters for menu labels and tooltips.
func trayText(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

// nowPlayingText is "Title — Artist", or "" when nothing is loaded.
func nowPlayingText(pb Playback) string {
	switch {
	case pb.Title == "":
		return ""
	case pb.Artist == "":
		return pb.Title
	default:
		return pb.Title + " — " + pb.Artist
	}
}

func (t *trayControls) update(pb Playback) {
	text := nowPlayingText(pb)
	// Windows limits tray tooltips to 127 characters.
	tooltip := "Encore"
	if text != "" {
		tooltip = trayText("Encore\n"+text, 127)
	}
	label := "Nothing playing"
	if text != "" {
		label = trayText(text, 60)
	}
	t.mu.Lock()
	changed := tooltip != t.tooltip || label != t.label || pb.Playing != t.playing
	t.tooltip, t.label, t.playing = tooltip, label, pb.Playing
	t.mu.Unlock()
	if !changed {
		return
	}
	t.tray.SetTooltip(tooltip)
	t.refresh()
}

// refresh re-applies the dynamic labels.
func (t *trayControls) refresh() {
	if t == nil {
		return
	}
	t.mu.Lock()
	label, playing := t.label, t.playing
	t.mu.Unlock()
	if label == "" {
		label = "Nothing playing"
	}
	t.nowItem.SetLabel(label)
	if playing {
		t.playItem.SetLabel("Pause")
	} else {
		t.playItem.SetLabel("Play")
	}
	t.miniItem.SetChecked(t.d.mini != nil && t.d.config().MiniPlayer != nil && t.d.config().MiniPlayer.Open)
	t.menu.Update()
}
