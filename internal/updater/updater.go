// Package updater checks the GitHub release feed, downloads a newer
// installer in the background and runs it silently on request.
package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const DefaultFeed = "https://api.github.com/repos/lilcham1/youtube-music-desktop/releases/latest"

type State string

const (
	Idle        State = "idle"
	Unavailable State = "unavailable"
	Checking    State = "checking"
	Downloading State = "downloading"
	Current     State = "current"
	Downloaded  State = "downloaded"
	Failed      State = "error"
)

type Status struct {
	State   State  `json:"state"`
	Message string `json:"message"`
}

type Updater struct {
	Feed       string
	Current    string // running version, e.g. "0.2.0"
	Enabled    bool   // false for development builds
	HTTP       *http.Client
	DownloadTo string // directory for installers
	OnStatus   func(Status)

	mu        sync.Mutex
	status    Status
	installer string
	busy      bool
}

func (u *Updater) Status() Status {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.status.State == "" {
		return Status{Idle, "Ready to check for updates."}
	}
	return u.status
}

func (u *Updater) set(state State, msg string) {
	u.mu.Lock()
	u.status = Status{state, msg}
	cb := u.OnStatus
	u.mu.Unlock()
	if cb != nil {
		cb(Status{state, msg})
	}
}

type release struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

// Check looks for a newer release and downloads its installer.
func (u *Updater) Check(ctx context.Context) Status {
	if !u.Enabled {
		u.set(Unavailable, "Updates are available from the installed app.")
		return u.Status()
	}
	u.mu.Lock()
	if u.busy || u.status.State == Downloaded {
		u.mu.Unlock()
		return u.Status()
	}
	u.busy = true
	u.mu.Unlock()
	defer func() { u.mu.Lock(); u.busy = false; u.mu.Unlock() }()

	u.set(Checking, "Checking for updates…")
	rel, err := u.fetch(ctx)
	if err != nil {
		u.set(Failed, "Updates are unavailable right now.")
		return u.Status()
	}
	latest := strings.TrimPrefix(rel.TagName, "v")
	if !Newer(latest, u.Current) {
		u.set(Current, fmt.Sprintf("You’re up to date (v%s).", u.Current))
		return u.Status()
	}
	name, url, size := PickInstaller(rel)
	if url == "" {
		u.set(Failed, "Updates are unavailable right now.")
		return u.Status()
	}
	u.set(Downloading, fmt.Sprintf("Downloading YouTube Music %s…", latest))
	path, err := u.download(ctx, name, url, size)
	if err != nil {
		u.set(Failed, "Updates are unavailable right now.")
		return u.Status()
	}
	u.mu.Lock()
	u.installer = path
	u.mu.Unlock()
	u.set(Downloaded, fmt.Sprintf("YouTube Music %s is ready to install.", latest))
	return u.Status()
}

// Install launches the downloaded installer silently. The caller quits the
// app afterwards so the installer can replace its files.
func (u *Updater) Install() error {
	u.mu.Lock()
	path := u.installer
	u.mu.Unlock()
	if path == "" {
		return errors.New("no update downloaded")
	}
	return exec.Command(path, "/S", "--run-after").Start()
}

func (u *Updater) client() *http.Client {
	if u.HTTP != nil {
		return u.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (u *Updater) fetch(ctx context.Context) (release, error) {
	var rel release
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.Feed, nil)
	if err != nil {
		return rel, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "youtube-music-desktop/"+u.Current)
	resp, err := u.client().Do(req)
	if err != nil {
		return rel, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return rel, fmt.Errorf("release feed: %s", resp.Status)
	}
	return rel, json.NewDecoder(resp.Body).Decode(&rel)
}

func (u *Updater) download(ctx context.Context, name, url string, size int64) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	client := *u.client()
	client.Timeout = 10 * time.Minute
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download: %s", resp.Status)
	}
	// The folder only ever holds the newest installer; drop older ones.
	_ = os.RemoveAll(u.DownloadTo)
	if err := os.MkdirAll(u.DownloadTo, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(u.DownloadTo, filepath.Base(name))
	tmp, err := os.Create(path + ".part")
	if err != nil {
		return "", err
	}
	total := resp.ContentLength
	if total <= 0 {
		total = size
	}
	written, err := io.Copy(tmp, &progress{r: resp.Body, total: total, report: func(pct int) {
		u.set(Downloading, fmt.Sprintf("Downloading update: %d%%", pct))
	}})
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil && size > 0 && written != size {
		err = fmt.Errorf("download incomplete: %d of %d bytes", written, size)
	}
	if err != nil {
		os.Remove(path + ".part")
		return "", err
	}
	return path, os.Rename(path+".part", path)
}

type progress struct {
	r      io.Reader
	total  int64
	read   int64
	last   int
	report func(int)
}

func (p *progress) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.read += int64(n)
	if p.total > 0 {
		if pct := int(p.read * 100 / p.total); pct >= p.last+5 {
			p.last = pct
			p.report(pct)
		}
	}
	return n, err
}

// PickInstaller returns the NSIS setup asset of a release.
func PickInstaller(rel release) (name, url string, size int64) {
	for _, a := range rel.Assets {
		lower := strings.ToLower(a.Name)
		if strings.HasSuffix(lower, ".exe") && strings.Contains(lower, "setup") {
			return a.Name, a.URL, a.Size
		}
	}
	return "", "", 0
}

// Newer reports whether version a is greater than b (dotted numeric, an
// optional pre-release suffix sorts before the plain release).
func Newer(a, b string) bool {
	pa, sa := parse(a)
	pb, sb := parse(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	if sa == sb {
		return false
	}
	return sa == "" || (sb != "" && sa > sb)
}

func parse(v string) ([3]int, string) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	core, suffix, _ := strings.Cut(v, "-")
	for i, part := range strings.SplitN(core, ".", 3) {
		out[i], _ = strconv.Atoi(part)
	}
	return out, suffix
}
