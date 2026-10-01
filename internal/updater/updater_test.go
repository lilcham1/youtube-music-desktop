package updater

import (
	"context"
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

func TestCheckDownloadsNewerRelease(t *testing.T) {
	payload := strings.Repeat("x", 1000)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/feed" {
			fmt.Fprintf(w, `{"tag_name":"v0.3.0","assets":[{"name":"YouTube-Music-Setup-0.3.0.exe","browser_download_url":"%s/dl","size":%d}]}`, srv.URL, len(payload))
			return
		}
		w.Write([]byte(payload))
	}))
	defer srv.Close()

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
