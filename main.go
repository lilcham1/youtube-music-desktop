// Encore for YouTube Music: a Windows wrapper around music.youtube.com built on
// Wails v3 and the system WebView2 runtime.
//
// Window layout mirrors the Electron releases: a frameless window renders
// the 40 px title bar (shell.html); the YouTube Music webview and the
// settings webview are separate Wails windows re-parented as Win32 children
// below the bar, so the site's own layout is never modified. The mini player
// is a separate always-on-top window.
package main

import (
	"context"
	"embed"
	"fmt"
	"log"
	"log/slog"
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

	"youtube-music/internal/listen"
	"youtube-music/internal/settings"
	"youtube-music/internal/startup"
	"youtube-music/internal/taskbar"
	"youtube-music/internal/updater"
	"youtube-music/internal/winhost"
)

// Set by release builds: -ldflags "-X main.version=0.2.0 -X main.updatesEnabled=true".
// The app's own Last.fm API account comes from the LASTFM_API_KEY and
// LASTFM_SHARED_SECRET build secrets; without it users add their own.
var (
	version         = "0.2.0-dev"
	updatesEnabled  = "false"
	lastfmAPIKey    = ""
	lastfmAppSecret = ""
)

const (
	musicURL    = "https://music.youtube.com/"
	appID       = "com.youtube.music.personal.desktop"
	titleBarDIP = 40
	// Width of the frame left around the YouTube view while the window is not
	// maximised: the title-bar page detects resize edges there.
	resizeBorderDIP = 6
	updateDelay     = 12 * time.Second
	updatePeriod    = 4 * time.Hour
)

//go:embed frontend/shell.html frontend/settings.html frontend/mini.html frontend/player.js assets/icon.ico
var files embed.FS

func mustRead(name string) string {
	data, err := files.ReadFile(name)
	if err != nil {
		panic(err)
	}
	return string(data)
}

type desktop struct {
	app                        *application.App
	shell, player, prefs, mini *application.WebviewWindow
	tray                       *trayControls
	thumbs                     taskbar.ThumbBar // UI thread only
	presence                   *presence
	updates                    *updater.Updater
	scrobbler                  *scrobbler
	queue                      *spotifyQueue
	listen                     *listenAlong
	follow                     *follower
	playerJS                   string
	profile, settingsPath, exe string
	startHidden                bool
	windowSaver, miniSaver     *debouncer
	saveMu                     sync.Mutex // writes settings.json in the order changes were made

	mu          sync.Mutex
	cfg         settings.Settings
	quitting    bool
	attached    bool
	maximized   bool
	playerReady bool
	now         Playback  // latest playback report
	nowAt       time.Time // when it arrived
	pendingJoin string    // listen-along code received before the player was ready
	ytVersion   string    // YouTube Music's web client version, for song search
}

// resizeEdges are the edge names Wails' "wails:resize:<edge>" accepts.
var resizeEdges = map[string]bool{
	"n-resize": true, "ne-resize": true, "e-resize": true, "se-resize": true,
	"s-resize": true, "sw-resize": true, "w-resize": true, "nw-resize": true,
}

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		fatal("Encore could not find your user folder.", err)
	}
	profile := filepath.Join(home, ".youtube-music")
	if err := os.MkdirAll(profile, 0o755); err != nil {
		fatal("Encore could not create its profile folder.", err)
	}
	logger := setupLogging(profile)
	d := &desktop{
		profile:      profile,
		settingsPath: filepath.Join(profile, "settings.json"),
		playerJS:     mustRead("frontend/player.js"),
		startHidden:  slices.Contains(os.Args[1:], startup.HiddenFlag),
	}
	d.exe, _ = os.Executable()
	if d.cfg, err = settings.Load(d.settingsPath); err != nil {
		log.Printf("loading settings: %v", err)
		fatal(fmt.Sprintf("Your saved profile could not be opened. Please check access to %s.", profile), err)
	}
	d.pendingJoin = joinCodeFromArgs(os.Args[1:])

	browserArgs := []string{
		// Keep playback alive while minimised, hidden in the tray or covered.
		"--disable-background-timer-throttling",
		"--disable-renderer-backgrounding",
		"--disable-backgrounding-occluded-windows",
	}
	if port := os.Getenv("YTM_DEBUG_PORT"); port != "" {
		browserArgs = append(browserArgs, "--remote-debugging-port="+port)
	}
	taskbar.ButtonCreatedMessage() // register once, outside the window procedure

	d.app = application.New(application.Options{
		Name:        "Encore",
		Description: "Encore for YouTube Music",
		Icon:        []byte(mustRead("assets/icon.ico")),
		Logger:      logger,
		LogLevel:    slog.LevelWarn,
		Windows: application.WindowsOptions{
			WebviewUserDataPath:   filepath.Join(profile, "webview2"),
			AdditionalBrowserArgs: browserArgs,
			WndProcInterceptor:    d.wndProc,
		},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: appID,
			OnSecondInstanceLaunch: func(data application.SecondInstanceData) {
				if code := joinCodeFromArgs(data.Args); code != "" {
					go d.joinFromLink(code)
				}
				application.InvokeAsync(d.showMain)
			},
		},
		RawMessageHandler: d.onMessage,
		PanicHandler: func(p *application.PanicDetails) {
			log.Printf("panic: %v\n%s", p.Error, p.FullStackTrace)
		},
		OnShutdown: d.shutdown,
	})

	d.presence = newPresence(func(string) { d.pushSettingsState() })
	d.presence.Configure(d.cfg.DiscordEnabled, d.cfg.DiscordAppID)
	d.updates = &updater.Updater{
		Feed:       updater.DefaultFeed,
		Current:    strings.TrimSuffix(version, "-dev"),
		Enabled:    updatesEnabled == "true",
		DownloadTo: filepath.Join(os.TempDir(), "encore-update"),
		OnStatus:   func(updater.Status) { d.pushSettingsState() },
	}
	d.scrobbler = newScrobbler(d)
	d.queue = &spotifyQueue{d: d}
	d.listen = &listenAlong{d: d, relay: &listen.Relay{}}
	d.follow = newFollower(d)
	d.windowSaver = newDebouncer(700*time.Millisecond, d.saveWindowState)
	d.miniSaver = newDebouncer(700*time.Millisecond, d.saveMiniState)

	d.createWindows()
	d.tray = newTrayControls(d)
	d.app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		application.InvokeAsync(d.attachChildren)
		go d.updateLoop()
		go d.scrobbler.run()
		d.follow.startBot()
	})

	if err := d.app.Run(); err != nil {
		log.Printf("run: %v", err)
		fatal("Encore stopped unexpectedly.", err)
	}
}

func (d *desktop) createWindows() {
	shellHTML := strings.NewReplacer(
		"{{VOLUME}}", strconv.Itoa(d.cfg.Volume),
		"{{DISCORD}}", strconv.FormatBool(d.cfg.DiscordEnabled),
	).Replace(mustRead("frontend/shell.html"))

	// The shell starts hidden; attachChildren restores its saved bounds and
	// then shows it, so it never flashes at the default position.
	d.shell = d.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "shell",
		Title:            "Encore",
		Width:            1280,
		Height:           800,
		MinWidth:         900,
		MinHeight:        600,
		Frameless:        true,
		Hidden:           true,
		BackgroundColour: application.NewRGB(5, 5, 5),
		HTML:             shellHTML,
		Windows:          application.WindowsWindow{Theme: application.Dark},
	})
	// The child windows must not be resizable: Wails would otherwise give them
	// a sizing frame on every page load.
	child := application.WindowsWindow{HiddenOnTaskbar: true, DisableFramelessWindowDecorations: true, Theme: application.Dark}
	d.player = d.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "player",
		Title:            "Encore Player",
		Frameless:        true,
		DisableResize:    true,
		Hidden:           true,
		BackgroundColour: application.NewRGB(3, 3, 3),
		URL:              musicURL,
		Windows:          child,
	})
	d.prefs = d.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "settings",
		Title:            "Encore Settings",
		Frameless:        true,
		DisableResize:    true,
		Hidden:           true,
		BackgroundColour: application.NewRGB(23, 23, 23),
		HTML:             mustRead("frontend/settings.html"),
		Windows:          child,
	})
	d.mini = d.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "mini",
		Title:            "Encore Mini Player",
		Width:            380,
		Height:           112,
		Frameless:        true,
		DisableResize:    true,
		AlwaysOnTop:      true,
		Hidden:           true,
		BackgroundColour: application.NewRGB(18, 18, 18),
		HTML:             mustRead("frontend/mini.html"),
		Windows:          application.WindowsWindow{HiddenOnTaskbar: true, Theme: application.Dark},
	})

	// Wails holds ExecJS until its runtime reports ready from the page. That
	// never happens on a foreign origin or a string-built page, so mark each
	// page ready ourselves once it has loaded.
	for _, w := range []*application.WebviewWindow{d.shell, d.player, d.prefs, d.mini} {
		w := w
		w.OnWindowEvent(events.Windows.WebViewNavigationCompleted, func(*application.WindowEvent) {
			w.HandleMessage("wails:runtime:ready")
			if w == d.player {
				w.ExecJS(d.playerJS)
				d.applyPlayerVolume(true)
			}
		})
	}

	d.shell.OnWindowEvent(events.Common.WindowDidResize, func(*application.WindowEvent) {
		d.layout()
		// The title bar shows a restore icon and drops its resize edges
		// while maximised.
		maximized := d.shell.IsMaximised()
		d.mu.Lock()
		changed := maximized != d.maximized
		d.maximized = maximized
		d.mu.Unlock()
		if changed {
			d.pushState(d.shell)
		}
		d.windowSaver.Trigger()
	})
	d.shell.OnWindowEvent(events.Common.WindowDidMove, func(*application.WindowEvent) { d.windowSaver.Trigger() })
	d.mini.OnWindowEvent(events.Common.WindowDidMove, func(*application.WindowEvent) { d.miniSaver.Trigger() })
	d.shell.OnWindowEvent(events.Common.WindowMinimise, func(*application.WindowEvent) {
		if d.setting(func(s settings.Settings) bool { return s.MinimizeToTray }) {
			d.shell.Hide()
		}
	})
	d.shell.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		d.mu.Lock()
		toTray := !d.quitting && d.cfg.CloseToTray
		d.mu.Unlock()
		d.saveWindowState()
		if toTray {
			e.Cancel()
			d.shell.Hide()
			return
		}
		d.quit()
	})
}

// attachChildren re-parents the player and settings windows into the shell,
// restores the window's last position and shows it.
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
	maximize := d.restoreWindowState()
	if !d.startHidden {
		d.shell.Show()
		if maximize {
			d.shell.Maximise()
		}
	}
	d.layout()
	if d.cfg.MiniPlayer != nil && d.cfg.MiniPlayer.Open {
		d.showMini()
	}
}

func (d *desktop) layout() {
	d.mu.Lock()
	attached := d.attached
	d.mu.Unlock()
	if !attached {
		return
	}
	parent := uintptr(d.shell.NativeWindow())
	border := resizeBorderDIP
	if d.shell.IsMaximised() || d.shell.IsFullscreen() {
		border = 0
	}
	winhost.FillBelow(uintptr(d.player.NativeWindow()), parent, titleBarDIP, border)
	winhost.FillBelow(uintptr(d.prefs.NativeWindow()), parent, titleBarDIP, border)
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

func (d *desktop) shutdown() {
	d.windowSaver.Flush()
	d.miniSaver.Flush()
	d.listen.Stop()
	d.follow.stopIfActive("")
	d.presence.Close()
	d.scrobbler.flush()
	log.Print("shut down")
}

func (d *desktop) setting(get func(settings.Settings) bool) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return get(d.cfg)
}

// update mutates the settings under lock and saves them.
func (d *desktop) update(change func(*settings.Settings)) settings.Settings {
	d.saveMu.Lock()
	defer d.saveMu.Unlock()
	d.mu.Lock()
	change(&d.cfg)
	cfg := d.cfg
	d.mu.Unlock()
	if err := settings.Save(d.settingsPath, cfg); err != nil {
		log.Printf("saving settings: %v", err)
	}
	return cfg
}

func (d *desktop) config() settings.Settings {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cfg
}

func (d *desktop) updateLoop() {
	time.Sleep(updateDelay)
	for {
		d.updates.Check(context.Background())
		time.Sleep(updatePeriod)
	}
}

// joinCodeFromArgs finds a ytm-desktop://join/<code> link among launch
// arguments (the installer registers the scheme).
func joinCodeFromArgs(args []string) string {
	for _, a := range args {
		if strings.HasPrefix(strings.ToLower(a), listen.Scheme+":") {
			return a
		}
	}
	return ""
}

func fatal(message string, err error) {
	text := message
	if err != nil {
		text += "\n\n" + err.Error()
	}
	title, _ := windows.UTF16PtrFromString("Encore")
	body, _ := windows.UTF16PtrFromString(text)
	windows.MessageBox(0, body, title, windows.MB_OK|windows.MB_ICONERROR)
	os.Exit(1)
}
