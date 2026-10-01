package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readMap(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMissingAndCorruptFilesFallBackToDefaults(t *testing.T) {
	for name, content := range map[string]string{"missing": "", "corrupt": `{"volume": 4`} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			if content != "" {
				path = write(t, content)
			}
			s, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if s.Volume != defaultVolume || s.DiscordAppID != DiscordApplicationID {
				t.Fatalf("defaults not applied: %+v", s)
			}
			if readMap(t, path)["volumeScale"] != EngineScale {
				t.Fatal("defaults must be written with the engine marker")
			}
		})
	}
}

func TestVolumeMigrations(t *testing.T) {
	cases := []struct {
		name, file string
		want       int
	}{
		// This installation today: Electron 0.1.33 converted engine 1 to slider 5.
		{"electron 0.1.33 slider", `{"volume":5,"sliderVolume":5,"volumeScale":"slider","windowBehaviorVersion":1}`, 1},
		{"electron 0.1.32 slider 27", `{"volume":27,"sliderVolume":27,"volumeScale":"slider","windowBehaviorVersion":1}`, 7},
		// 0.1.31 ran after 0.1.32: engine value 1 with a stale marker, no copy.
		{"stale slider marker", `{"volume":1,"volumeScale":"slider","windowBehaviorVersion":1}`, 1},
		{"no marker (engine releases)", `{"volume":12,"windowBehaviorVersion":1}`, 12},
		{"already engine", `{"volume":3,"volumeScale":"engine","windowBehaviorVersion":1}`, 3},
		{"out of range", `{"volume":250,"volumeScale":"engine","windowBehaviorVersion":1}`, 100},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := write(t, c.file)
			s, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if s.Volume != c.want {
				t.Fatalf("volume = %d; want %d", s.Volume, c.want)
			}
			m := readMap(t, path)
			if m["volume"] != float64(c.want) || m["volumeScale"] != EngineScale {
				t.Fatalf("file not migrated: %v", m)
			}
			if _, stale := m["sliderVolume"]; stale {
				t.Fatal("sliderVolume must be removed")
			}
		})
	}
}

func TestUnknownKeysAndChoicesArePreserved(t *testing.T) {
	path := write(t, `{"discordEnabled":true,"closeToTray":true,"volume":5,"sliderVolume":5,"volumeScale":"slider","windowBehaviorVersion":1,"custom":"kept"}`)
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !s.DiscordEnabled || !s.CloseToTray || s.DiscordAppID != DiscordApplicationID {
		t.Fatalf("got %+v", s)
	}
	if readMap(t, path)["custom"] != "kept" {
		t.Fatal("unknown key lost")
	}
}

func TestUpToDateFileIsNotRewritten(t *testing.T) {
	path := write(t, `{"volume":27,"volumeScale":"engine","windowBehaviorVersion":1,"closeToTray":true}`)
	before, _ := os.ReadFile(path)
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("an up-to-date file must not be rewritten")
	}
}

func TestLegacyWindowBehaviourIsReset(t *testing.T) {
	s, err := Load(write(t, `{"minimizeToTray":true,"closeToTray":true,"volume":20}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.MinimizeToTray || s.CloseToTray || s.Volume != 20 {
		t.Fatalf("got %+v", s)
	}
}

func TestSaveIsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s := Defaults()
	if !s.SetVolume(1.4) || s.Volume != 1 {
		t.Fatalf("SetVolume = %+v", s)
	}
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("temporary file left behind")
	}
	if got, _ := Load(path); got.Volume != 1 {
		t.Fatalf("round trip: %+v", got)
	}
}
