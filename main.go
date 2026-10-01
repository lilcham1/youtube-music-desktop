// YouTube Music Desktop: a Windows wrapper around music.youtube.com built on
// Wails v3 and the system WebView2 runtime.
//
// Window layout mirrors the Electron releases: a frameless window renders
// the 40 px title bar (shell.html); the YouTube Music webview and the
// settings webview are separate Wails windows re-parented as Win32 children
// below the bar, so the site's own layout is never modified.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"golang.org/x/sys/windows"

	"youtube-music/internal/settings"
	"youtube-music/internal/startup"
	"youtube-music/internal/updater"
	"youtube-music/internal/volume"
	"youtube-music/internal/winhost"
)

// Set by release builds: -ldflags "-X main.version=0.2.0 -X main.updatesEnabled=true".
var (
	version        = "0.2.0-dev"
	updatesEnabled = "false"
)

const (
	musicURL     = "https://music.youtube.com/"
	appID        = "com.youtube.music.personal.desktop"
	titleBarDIP  = 40
	updateDelay  = 12 * time.Second
	updatePeriod = 4 * time.Hour
)

//go:embed frontend/shell.html frontend/settings.html frontend/player.js assets/icon.ico
var files embed.FS

func mustRead(name string) string {
	data, err := files.ReadFile(name)
	if err != nil {
		panic(err)
	}
	return string(data)
}

type desktop struct {
	app                  *application.App
	shell, player, prefs *application.WebviewWindow
	presence             *presence
	updates              *updater.Updater
	playerJS             string
	settingsPath, exe    string

	mu       sync.Mutex
	cfg      settings.Settings
	quitting bool
	attached bool
}

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		fatal("YouTube Music could not find your user folder.", err)
	}
	profile := filepath.Join(home, ".youtube-music")
	if err := os.MkdirAll(profile, 0o755); err != nil {
		fatal("YouTube Music could not create its profile folder.", err)
	}
	d := &desktop{settingsPath: filepath.Join(profile, "settings.json"), playerJS: mustRead("frontend/player.js")}
	d.exe, _ = os.Executable()
	if d.cfg, err = settings.Load(d.settingsPath); err != nil {
		fatal(fmt.Sprintf("Your saved profile could not be opened. Please check access to %s.", profile), err)
	}

	browserArgs := []string{
		// Keep playback alive while minimised, hidden in the tray or covered.
		"--disable-background-timer-throttling",
		"--disable-renderer-backgrounding",
		"--disable-backgrounding-occluded-windows",
	}
	if port := os.Getenv("YTM_DEBUG_PORT"); port != "" {
		browserArgs = append(browserArgs, "--remote-debugging-port="+port)
	}

	d.app = application.New(application.Options{
		Name:        "YouTube Music",
		Description: "YouTube Music for Windows",
		Icon:        []byte(mustRead("assets/icon.ico")),
		Windows: application.WindowsOptions{
			WebviewUserDataPath:   filepath.Join(profile, "webview2"),
			AdditionalBrowserArgs: browserArgs,
		},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: appID,
			OnSecondInstanceLaunch: func(application.SecondInstanceData) {
				application.InvokeAsync(d.showMain)
			},
		},
		RawMessageHandler: d.onMessage,
		OnShutdown:        func() { d.presence.Close() },
	})

	d.presence = newPresence(func(string) { d.pushSettingsState() })
	d.presence.Configure(d.cfg.DiscordEnabled, d.cfg.DiscordAppID)
	d.updates = &updater.Updater{
		Feed:       updater.DefaultFeed,
		Current:    strings.TrimSuffix(version, "-dev"),
		Enabled:    updatesEnabled == "true",
		DownloadTo: filepath.Join(os.TempDir(), "youtube-music-update"),
		OnStatus:   func(updater.Status) { d.pushSettingsState() },
	}

	d.createWindows(slices.Contains(os.Args[1:], startup.HiddenFlag))
	d.createTray()
	d.app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		application.InvokeAsync(d.attachChildren)
		go d.updateLoop()
	})

	if err := d.app.Run(); err != nil {
		fatal("YouTube Music stopped unexpectedly.", err)
	}
}

func (d *desktop) createWindows(startHidden bool) {
	shellHTML := strings.NewReplacer(
		"{{VOLUME}}", strconv.Itoa(d.cfg.Volume),
		"{{DISCORD}}", strconv.FormatBool(d.cfg.DiscordEnabled),
	).Replace(mustRead("frontend/shell.html"))

	d.shell = d.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "shell",
		Title:            "YouTube Music",
		Width:            1280,
		Height:           800,
		MinWidth:         900,
		MinHeight:        600,
		Frameless:        true,
		Hidden:           startHidden,
		BackgroundColour: application.NewRGB(5, 5, 5),
		HTML:             shellHTML,
		Windows:          application.WindowsWindow{Theme: application.Dark},
	})
	child := application.WindowsWindow{HiddenOnTaskbar: true, DisableFramelessWindowDecorations: true, Theme: application.Dark}
	d.player = d.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "player",
		Title:            "YouTube Music Player",
		Frameless:        true,
		Hidden:           true,
		BackgroundColour: application.NewRGB(3, 3, 3),
		URL:              musicURL,
		Windows:          child,
	})
	d.prefs = d.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "settings",
		Title:            "YouTube Music Settings",
		Frameless:        true,
		Hidden:           true,
		BackgroundColour: application.NewRGB(23, 23, 23),
		HTML:             mustRead("frontend/settings.html"),
		Windows:          child,
	})

	// Wails holds ExecJS until its runtime reports ready from the page. That
	// never happens on a foreign origin, so mark each page ready ourselves
	// once it has loaded.
	for _, w := range []*application.WebviewWindow{d.shell, d.player, d.prefs} {
		w := w
		w.OnWindowEvent(events.Windows.WebViewNavigationCompleted, func(*application.WindowEvent) {
			w.HandleMessage("wails:runtime:ready")
			if w == d.player {
				w.ExecJS(d.playerJS)
				d.applyPlayerVolume()
			}
		})
	}

	d.shell.OnWindowEvent(events.Common.WindowDidResize, func(*application.WindowEvent) { d.layout() })
	d.shell.OnWindowEvent(events.Common.WindowMinimise, func(*application.WindowEvent) {
		if d.setting(func(s settings.Settings) bool { return s.MinimizeToTray }) {
			d.shell.Hide()
		}
	})
	d.shell.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		d.mu.Lock()
		toTray := !d.quitting && d.cfg.CloseToTray
		d.mu.Unlock()
		if toTray {
			e.Cancel()
			d.shell.Hide()
			return
		}
		d.quit()
	})
}

// attachChildren re-parents the player and settings windows into the shell.
func (d *desktop) attachChildren() {
	parent := uintptr(d.shell.NativeWindow())
	player, prefs := uintptr(d.player.NativeWindow()), uintptr(d.prefs.NativeWindow())
	if parent == 0 || player == 0 || prefs == 0 {
		time.AfterFunc(100*time.Millisecond, func() { application.InvokeAsync(d.attachChildren) })
		return
	}
	winhost.Attach(player, parent)
	winhost.Attach(prefs, parent)
	d.mu.Lock()
	d.attached = true
	d.mu.Unlock()
	d.player.Show()
	d.layout()
}

func (d *desktop) layout() {
	d.mu.Lock()
	attached := d.attached
	d.mu.Unlock()
	if !attached {
		return
	}
	parent := uintptr(d.shell.NativeWindow())
	winhost.FillBelow(uintptr(d.player.NativeWindow()), parent, titleBarDIP)
	winhost.FillBelow(uintptr(d.prefs.NativeWindow()), parent, titleBarDIP)
}

func (d *desktop) createTray() {
	menu := application.NewMenu()
	menu.Add("Open YouTube Music").OnClick(func(*application.Context) { d.showMain() })
	menu.Add("Settings…").OnClick(func(*application.Context) { d.openSettings("general") })
	menu.Add("Discord Rich Presence…").OnClick(func(*application.Context) { d.openSettings("discord") })
	menu.AddSeparator()
	menu.Add("Quit YouTube Music").OnClick(func(*application.Context) { d.quit() })

	tray := d.app.SystemTray.New()
	tray.SetIcon([]byte(mustRead("assets/icon.ico")))
	tray.SetTooltip("YouTube Music")
	tray.SetMenu(menu)
	tray.OnClick(d.showMain)
	tray.OnDoubleClick(d.showMain)
}

func (d *desktop) showMain() {
	if d.shell.IsMinimised() {
		d.shell.Restore()
	}
	d.shell.Show()
	d.shell.Focus()
}

func (d *desktop) openSettings(section string) {
	d.showMain()
	d.prefs.ExecJS(fmt.Sprintf("window.__app && window.__app.show(%q)", section))
	d.pushSettingsState()
	d.layout()
	// Wails' Show also tells WebView2 it is visible, so it renders.
	d.prefs.Show()
	winhost.Raise(uintptr(d.prefs.NativeWindow()))
	d.prefs.Focus()
}

// closeSettings hides the settings view. The player underneath was never
// hidden, so playback is unaffected.
func (d *desktop) closeSettings() {
	d.prefs.Hide()
	d.player.Focus()
}

func (d *desktop) quit() {
	d.mu.Lock()
	if d.quitting {
		d.mu.Unlock()
		return
	}
	d.quitting = true
	d.mu.Unlock()
	d.app.Quit()
}

func (d *desktop) setting(get func(settings.Settings) bool) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return get(d.cfg)
}

// update mutates the settings under lock and saves them.
func (d *desktop) update(change func(*settings.Settings)) settings.Settings {
	d.mu.Lock()
	change(&d.cfg)
	cfg := d.cfg
	d.mu.Unlock()
	if err := settings.Save(d.settingsPath, cfg); err != nil {
		log.Printf("saving settings: %v", err)
	}
	return cfg
}

func (d *desktop) applyPlayerVolume() {
	d.mu.Lock()
	level := d.cfg.Volume
	d.mu.Unlock()
	pref, _ := volume.EngineToSlider(float64(level))
	d.player.ExecJS(fmt.Sprintf("window.__ytmDesktop && window.__ytmDesktop.setVolume(%d, %d)", level, pref))
}

func (d *desktop) state() map[string]any {
	d.mu.Lock()
	cfg := d.cfg
	d.mu.Unlock()
	return map[string]any{
		"volume":           cfg.Volume,
		"discordEnabled":   cfg.DiscordEnabled,
		"minimizeToTray":   cfg.MinimizeToTray,
		"closeToTray":      cfg.CloseToTray,
		"startWithWindows": startup.Enabled(),
		"version":          strings.TrimSuffix(version, "-dev"),
		"discordStatus":    d.presence.Status(),
		"update":           d.updates.Status(),
	}
}

func (d *desktop) pushState(w *application.WebviewWindow) {
	data, _ := json.Marshal(d.state())
	w.ExecJS("window.__app && window.__app.state(" + string(data) + ")")
}

func (d *desktop) pushSettingsState() {
	if d.prefs != nil {
		d.pushState(d.prefs)
	}
}

func (d *desktop) pushAllState() {
	d.pushState(d.shell)
	d.pushSettingsState()
}

type inbound struct {
	Type     string          `json:"type"`
	Value    *float64        `json:"value"`
	Settings json.RawMessage `json:"settings"`
	Playback
}

// onMessage handles postMessage calls from the three pages. Each page may
// only send the message types that belong to it.
func (d *desktop) onMessage(window application.Window, message string, origin *application.OriginInfo) {
	var msg inbound
	if err := json.Unmarshal([]byte(message), &msg); err != nil {
		return
	}
	switch window.Name() {
	case "player":
		if !strings.HasPrefix(origin.Origin, musicURL) && !strings.HasPrefix(origin.TopOrigin, musicURL) {
			return
		}
		d.onPlayerMessage(msg)
	case "shell":
		d.onShellMessage(msg)
	case "settings":
		d.onSettingsMessage(msg)
	}
}

func (d *desktop) onPlayerMessage(msg inbound) {
	switch msg.Type {
	case "ready":
		d.applyPlayerVolume()
	case "playback":
		d.presence.Update(msg.Playback)
	case "volume-changed":
		if msg.Value == nil {
			return
		}
		d.update(func(s *settings.Settings) { s.SetVolume(*msg.Value) })
		// Re-applying writes the matching PREF value for the next launch; the
		// engine already has this level, so nothing audible changes.
		d.applyPlayerVolume()
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
			d.applyPlayerVolume()
		}
		d.pushAllState()
	case "open-settings":
		d.openSettings("general")
	case "open-discord":
		d.openSettings("discord")
	case "minimize":
		d.shell.Minimise()
	case "maximize":
		d.shell.ToggleMaximise()
	case "close":
		d.shell.Close()
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
	case "check-updates":
		go d.updates.Check(context.Background())
	case "install-update":
		if d.updates.Status().State == updater.Downloaded {
			if err := d.updates.Install(); err == nil {
				d.quit()
			}
		}
	case "save":
		var next struct {
			DiscordEnabled   *bool `json:"discordEnabled"`
			MinimizeToTray   *bool `json:"minimizeToTray"`
			CloseToTray      *bool `json:"closeToTray"`
			StartWithWindows *bool `json:"startWithWindows"`
		}
		if json.Unmarshal(msg.Settings, &next) != nil {
			return
		}
		cfg := d.update(func(s *settings.Settings) {
			if next.DiscordEnabled != nil {
				s.DiscordEnabled = *next.DiscordEnabled
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
		d.pushAllState()
	}
}

func (d *desktop) updateLoop() {
	time.Sleep(updateDelay)
	for {
		d.updates.Check(context.Background())
		time.Sleep(updatePeriod)
	}
}

func fatal(message string, err error) {
	text := message
	if err != nil {
		text += "\n\n" + err.Error()
	}
	title, _ := windows.UTF16PtrFromString("YouTube Music")
	body, _ := windows.UTF16PtrFromString(text)
	windows.MessageBox(0, body, title, windows.MB_OK|windows.MB_ICONERROR)
	os.Exit(1)
}
