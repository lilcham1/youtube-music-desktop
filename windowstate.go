package main

import (
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"

	"youtube-music/internal/settings"
)

var procMonitorFromWindow = windows.NewLazySystemDLL("user32.dll").NewProc("MonitorFromWindow")

// debouncer runs fn once things have been quiet for delay.
type debouncer struct {
	mu    sync.Mutex
	delay time.Duration
	fn    func()
	timer *time.Timer
}

func newDebouncer(delay time.Duration, fn func()) *debouncer {
	return &debouncer{delay: delay, fn: fn}
}

func (b *debouncer) Trigger() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.timer != nil {
		b.timer.Stop()
	}
	b.timer = time.AfterFunc(b.delay, b.fn)
}

// Flush runs a pending call now.
func (b *debouncer) Flush() {
	b.mu.Lock()
	pending := b.timer != nil && b.timer.Stop()
	b.timer = nil
	b.mu.Unlock()
	if pending {
		b.fn()
	}
}

// onScreen reports whether any part of the window is on a connected monitor.
func onScreen(hwnd uintptr) bool {
	const monitorDefaultToNull = 0
	m, _, _ := procMonitorFromWindow.Call(hwnd, monitorDefaultToNull)
	return m != 0
}

// restoreWindowState applies the main window's last normal bounds. A window
// that would be off-screen (monitor unplugged) is centred instead. It
// reports whether the window should then be maximised, which must happen
// after it is first shown: Windows applies the launch's show state to a
// process's first ShowWindow call, so maximising a hidden window first can
// leave it minimised.
func (d *desktop) restoreWindowState() (maximize bool) {
	ws := d.config().Window
	if ws == nil {
		d.shell.Center()
		return false
	}
	d.shell.SetSize(ws.Width, ws.Height)
	d.shell.SetPosition(ws.X, ws.Y)
	if !onScreen(uintptr(d.shell.NativeWindow())) {
		d.shell.Center()
	}
	return ws.Maximized
}

// saveWindowState records the window's normal bounds, and whether it is
// maximised. Bounds are not overwritten while maximised or minimised, so
// restoring from maximised returns to the last normal size.
// devBuild is true for builds without a release version. They share the
// real profile (to test with its sign-in) but don't save window positions,
// so test runs don't move the installed app's windows.
var devBuild = strings.HasSuffix(version, "-dev")

func (d *desktop) saveWindowState() {
	if devBuild || d.shell == nil || d.shell.IsMinimised() || !d.shell.IsVisible() {
		return
	}
	maximized := d.shell.IsMaximised()
	var bounds settings.WindowState
	if !maximized {
		bounds.X, bounds.Y = d.shell.Position()
		bounds.Width, bounds.Height = d.shell.Size()
	}
	d.update(func(s *settings.Settings) {
		if maximized {
			if s.Window == nil {
				return
			}
			s.Window.Maximized = true
			return
		}
		if bounds.Width <= 0 || bounds.Height <= 0 {
			return
		}
		bounds.Maximized = false
		s.Window = &bounds
	})
}

func (d *desktop) showMini() {
	if mp := d.config().MiniPlayer; mp != nil && (mp.X != 0 || mp.Y != 0) {
		d.mini.SetPosition(mp.X, mp.Y)
	}
	d.mini.Show()
	if !onScreen(uintptr(d.mini.NativeWindow())) {
		d.mini.Center()
	}
	d.pushNowPlaying()
	d.setMiniOpen(true)
}

func (d *desktop) hideMini() {
	d.miniSaver.Flush()
	d.mini.Hide()
	d.setMiniOpen(false)
}

func (d *desktop) toggleMini() {
	if d.mini.IsVisible() {
		d.hideMini()
	} else {
		d.showMini()
	}
}

func (d *desktop) setMiniOpen(open bool) {
	d.update(func(s *settings.Settings) {
		if s.MiniPlayer == nil {
			s.MiniPlayer = &settings.MiniPlayer{}
		}
		s.MiniPlayer.Open = open
	})
	d.pushState(d.shell)
	d.tray.refresh()
}

func (d *desktop) saveMiniState() {
	if devBuild || d.mini == nil || !d.mini.IsVisible() {
		return
	}
	x, y := d.mini.Position()
	d.update(func(s *settings.Settings) {
		if s.MiniPlayer == nil {
			s.MiniPlayer = &settings.MiniPlayer{Open: true}
		}
		s.MiniPlayer.X, s.MiniPlayer.Y = x, y
	})
}
