package updater

import (
	"context"
	"crypto/sha512"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.2.0", "0.1.33", true},
		{"v0.2.0", "0.2.0", false},
		{"0.1.33", "0.2.0", false},
		{"1.0.0", "0.9.9", true},
		{"0.2.0", "0.2.0-beta.1", true},
		{"0.2.0-beta.2", "0.2.0-beta.1", true},
		{"0.2.0-beta.1", "0.2.0", false},
	}
	for _, c := range cases {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func TestPickInstallerSkipsBlockmapsAndYml(t *testing.T) {
	rel := release{}
	for _, n := range []string{"latest.yml", "YouTube-Music-Setup-0.2.0.exe.blockmap", "YouTube-Music-Setup-0.2.0.exe"} {
		rel.Assets = append(rel.Assets, struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int64  `json:"size"`
		}{n, "https://x/" + n, 3})
	}
	if name, _, _ := PickInstaller(rel); name != "YouTube-Music-Setup-0.2.0.exe" {
		t.Fatalf("picked %q", name)
	}
}

// releaseServer serves a release feed, its installer and latest.yml. served
// is the installer body actually returned; published is what latest.yml
// claims (they differ to simulate tampering).
func releaseServer(t *testing.T, served, published string, withYML bool) *httptest.Server {
	t.Helper()
	sum := sha512.Sum512([]byte(published))
	sha := base64.StdEncoding.EncodeToString(sum[:])
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/feed":
			yml := ""
			if withYML {
				yml = fmt.Sprintf(`,{"name":"latest.yml","browser_download_url":"%s/latest.yml","size":300}`, srv.URL)
			}
			fmt.Fprintf(w, `{"tag_name":"v0.3.0","assets":[{"name":"YouTube-Music-Setup-0.3.0.exe","browser_download_url":"%s/dl","size":%d}%s]}`, srv.URL, len(served), yml)
		case "/latest.yml":
			fmt.Fprintf(w, `version: 0.3.0
files:
  - url: YouTube-Music-Setup-0.3.0.exe
    sha512: %s
    size: %d
path: YouTube-Music-Setup-0.3.0.exe
sha512: %s
releaseDate: '2026-10-03T00:00:00.000Z'
`, sha, len(published), sha)
		default:
			w.Write([]byte(served))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCheckDownloadsAndVerifiesNewerRelease(t *testing.T) {
	payload := strings.Repeat("x", 1000)
	srv := releaseServer(t, payload, payload, true)
	var states []State
	u := &Updater{Feed: srv.URL + "/feed", Current: "0.2.0", Enabled: true, DownloadTo: t.TempDir(),
		OnStatus: func(s Status) { states = append(states, s.State) }}
	if s := u.Check(context.Background()); s.State != Downloaded || s.Message != "YouTube Music 0.3.0 is ready to install." {
		t.Fatalf("status = %+v (states %v)", s, states)
	}
	data, err := os.ReadFile(u.installer)
	if err != nil || string(data) != payload {
		t.Fatalf("installer not written: %v", err)
	}
	if states[0] != Checking || states[1] != Downloading {
		t.Fatalf("state sequence %v", states)
	}
}

func TestTamperedOrUnverifiableInstallerIsRejected(t *testing.T) {
	for name, srv := range map[string]*httptest.Server{
		"checksum mismatch": releaseServer(t, strings.Repeat("evil", 250), strings.Repeat("x", 1000), true),
		"no latest.yml":     releaseServer(t, strings.Repeat("x", 1000), strings.Repeat("x", 1000), false),
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			u := &Updater{Feed: srv.URL + "/feed", Current: "0.2.0", Enabled: true, DownloadTo: dir}
			if s := u.Check(context.Background()); s.State != Failed {
				t.Fatalf("status = %+v", s)
			}
			if u.installer != "" {
				t.Fatal("an unverified installer must not become installable")
			}
			if err := u.Install(); err == nil {
				t.Fatal("Install must refuse without a verified download")
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 0 {
				t.Fatalf("rejected download left files behind: %v", entries)
			}
		})
	}
}

func TestParseLatestYML(t *testing.T) {
	// The format build.ps1 writes.
	yml := `version: 0.2.1
files:
  - url: YouTube-Music-Setup-0.2.1.exe
    sha512: AAA==
    size: 4003017
path: YouTube-Music-Setup-0.2.1.exe
sha512: AAA==
releaseDate: '2026-10-02T18:16:57.000Z'
`
	if got, err := ParseLatestYML([]byte(yml), "YouTube-Music-Setup-0.2.1.exe"); err != nil || got != "AAA==" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := ParseLatestYML([]byte(yml), "Other.exe"); err == nil {
		t.Fatal("a checksum for a different file must not be used")
	}
	// Top-level path/sha512 only.
	if got, _ := ParseLatestYML([]byte("path: a.exe\nsha512: 'BBB='\n"), "a.exe"); got != "BBB=" {
		t.Fatalf("top-level got %q", got)
	}
}

func TestCheckReportsUpToDateAndDevBuilds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tag_name":"v0.2.0","assets":[]}`))
	}))
	defer srv.Close()
	u := &Updater{Feed: srv.URL, Current: "0.2.0", Enabled: true}
	if s := u.Check(context.Background()); s.State != Current || s.Message != "You’re up to date (v0.2.0)." {
		t.Fatalf("status = %+v", s)
	}
	dev := &Updater{Current: "0.2.0"}
	if s := dev.Check(context.Background()); s.State != Unavailable {
		t.Fatalf("dev status = %+v", s)
	}
}
