// Package settings loads, migrates and atomically saves settings.json. The
// file format is shared with the Electron releases (0.1.x): known keys keep
// their names and unknown keys are preserved on save.
package settings

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"youtube-music/internal/volume"
)

// DiscordApplicationID is the public Discord application used by every
// installation. It is an identifier, not a secret.
const DiscordApplicationID = "1547064138604347462"

// defaultVolume is an engine level; 20 matches the old slider's midpoint.
const defaultVolume = 20

// EngineScale marks a volume stored as the player engine level. Electron
// 0.1.32–0.1.33 wrote "slider" (old player-bar scale); earlier releases
// wrote engine levels without a marker.
const EngineScale = "engine"

type Settings struct {
	DiscordEnabled   bool
	DiscordAppID     string
	MinimizeToTray   bool
	CloseToTray      bool
	StartWithWindows bool
	Volume           int

	// Migration markers, see Load.
	WindowBehaviorVersion int
	VolumeScale           string

	// Window is the main window's last bounds; nil until first saved.
	Window *WindowState
	// MiniPlayer is the mini player's last position and visibility.
	MiniPlayer *MiniPlayer
	LastFM     LastFM
	Spotify    Spotify
	// DiscordBot is the user's own bot, used to follow Spotify listeners.
	DiscordBot DiscordBot

	extra map[string]json.RawMessage
}

// WindowState is in the units Wails uses for window position and size.
type WindowState struct {
	X         int  `json:"x"`
	Y         int  `json:"y"`
	Width     int  `json:"width"`
	Height    int  `json:"height"`
	Maximized bool `json:"maximized"`
}

type MiniPlayer struct {
	X    int  `json:"x"`
	Y    int  `json:"y"`
	Open bool `json:"open"`
}

// LastFM holds scrobbling credentials. Secret and SessionKey are stored
// protected with Windows DPAPI (see internal/secret), never in plain text.
type LastFM struct {
	APIKey     string `json:"apiKey,omitempty"`
	Secret     string `json:"secret,omitempty"`
	SessionKey string `json:"sessionKey,omitempty"`
	Username   string `json:"username,omitempty"`
	Enabled    bool   `json:"enabled,omitempty"`
}

func (l LastFM) empty() bool { return l == LastFM{} }

// Spotify holds the user's own Spotify developer app credentials.
// ClientSecret is stored protected with Windows DPAPI.
type Spotify struct {
	ClientID     string `json:"clientId,omitempty"`
	ClientSecret string `json:"clientSecret,omitempty"`
	// FallbackNoted is set once the user has been told that this app is
	// refused and playlists come from Spotify's public page instead.
	FallbackNoted bool `json:"fallbackNoted,omitempty"`
}

func (sp Spotify) empty() bool { return sp == Spotify{} }

// DiscordBot holds the bot token, stored protected with Windows DPAPI.
type DiscordBot struct {
	Token string `json:"token,omitempty"`
}

func (b DiscordBot) empty() bool { return b == DiscordBot{} }

func Defaults() Settings {
	return Settings{
		DiscordAppID:          DiscordApplicationID,
		Volume:                defaultVolume,
		WindowBehaviorVersion: 1,
		VolumeScale:           EngineScale,
	}
}

// raw mirrors the file with pointers so absent keys can be told apart.
type raw struct {
	DiscordEnabled        *bool    `json:"discordEnabled"`
	DiscordAppID          *string  `json:"discordAppId"`
	MinimizeToTray        *bool    `json:"minimizeToTray"`
	CloseToTray           *bool    `json:"closeToTray"`
	StartWithWindows      *bool    `json:"startWithWindows"`
	Volume                *float64 `json:"volume"`
	WindowBehaviorVersion *int     `json:"windowBehaviorVersion"`
	VolumeScale           *string  `json:"volumeScale"`
	SliderVolume          *float64 `json:"sliderVolume"`

	Window     *WindowState `json:"window"`
	MiniPlayer *MiniPlayer  `json:"miniPlayer"`
	LastFM     *LastFM      `json:"lastfm"`
	Spotify    *Spotify     `json:"spotify"`
	DiscordBot *DiscordBot  `json:"discordBot"`
}

// Load reads path, applies migrations and rewrites the file if anything
// changed. A missing or corrupt file falls back to defaults; only genuine
// access failures are returned as errors.
func Load(path string) (Settings, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return Settings{}, err
		}
		return writeDefaults(path)
	}
	var r raw
	var extra map[string]json.RawMessage
	if json.Unmarshal(data, &r) != nil || json.Unmarshal(data, &extra) != nil {
		return writeDefaults(path)
	}

	s := Defaults()
	s.extra = extra
	set := func(dst *bool, src *bool) {
		if src != nil {
			*dst = *src
		}
	}
	set(&s.DiscordEnabled, r.DiscordEnabled)
	set(&s.MinimizeToTray, r.MinimizeToTray)
	set(&s.CloseToTray, r.CloseToTray)
	set(&s.StartWithWindows, r.StartWithWindows)
	if r.DiscordAppID != nil && *r.DiscordAppID != "" {
		s.DiscordAppID = *r.DiscordAppID
	}
	if r.Window != nil && r.Window.Width > 0 && r.Window.Height > 0 {
		s.Window = r.Window
	}
	s.MiniPlayer = r.MiniPlayer
	if r.LastFM != nil {
		s.LastFM = *r.LastFM
	}
	if r.Spotify != nil {
		s.Spotify = *r.Spotify
	}
	if r.DiscordBot != nil {
		s.DiscordBot = *r.DiscordBot
	}

	changed := false
	// v0.1.16 changed the window behaviour: minimize keeps the taskbar entry
	// and close exits. Choices made afterwards are preserved.
	if r.WindowBehaviorVersion == nil || *r.WindowBehaviorVersion != 1 {
		s.MinimizeToTray, s.CloseToTray = false, false
		changed = true
	}

	// Normalise the volume to the engine scale:
	//   "engine"                        → already an engine level;
	//   "slider" with a matching copy   → written by Electron 0.1.32–0.1.33
	//                                     on the old slider curve, convert;
	//   anything else (no marker, or a stale "slider" marker kept by 0.1.31)
	//                                   → written by an engine-scale release.
	level, ok := defaultVolume, false
	if r.Volume != nil {
		switch {
		case r.VolumeScale != nil && *r.VolumeScale == EngineScale:
			level, ok = volume.Clamp(*r.Volume)
		case r.VolumeScale != nil && *r.VolumeScale == "slider" && r.SliderVolume != nil && *r.SliderVolume == *r.Volume:
			level, ok = volume.SliderToEngine(*r.Volume)
		default:
			level, ok = volume.Clamp(*r.Volume)
		}
	}
	if !ok {
		level = defaultVolume
	}
	s.Volume = level
	if r.Volume == nil || *r.Volume != float64(level) || r.VolumeScale == nil || *r.VolumeScale != EngineScale || r.SliderVolume != nil {
		changed = true
	}

	if changed {
		if err := Save(path, s); err != nil && errors.Is(err, fs.ErrPermission) {
			return Settings{}, err
		}
	}
	return s, nil
}

func writeDefaults(path string) (Settings, error) {
	s := Defaults()
	if err := Save(path, s); err != nil && errors.Is(err, fs.ErrPermission) {
		return Settings{}, err
	}
	return s, nil
}

// SetVolume stores an engine level.
func (s *Settings) SetVolume(v float64) bool {
	level, ok := volume.Clamp(v)
	if !ok {
		return false
	}
	s.Volume = level
	return true
}

// MarshalJSON writes known keys over any preserved unknown keys.
func (s Settings) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, len(s.extra)+13)
	for k, v := range s.extra {
		out[k] = v
	}
	// Superseded by the engine scale; a stale copy would confuse older builds.
	delete(out, "sliderVolume")
	out["discordEnabled"] = s.DiscordEnabled
	out["discordAppId"] = s.DiscordAppID
	out["minimizeToTray"] = s.MinimizeToTray
	out["closeToTray"] = s.CloseToTray
	out["startWithWindows"] = s.StartWithWindows
	out["volume"] = s.Volume
	out["windowBehaviorVersion"] = s.WindowBehaviorVersion
	out["volumeScale"] = s.VolumeScale
	setOrDelete := func(key string, value any, present bool) {
		if present {
			out[key] = value
		} else {
			delete(out, key)
		}
	}
	setOrDelete("window", s.Window, s.Window != nil)
	setOrDelete("miniPlayer", s.MiniPlayer, s.MiniPlayer != nil)
	setOrDelete("lastfm", s.LastFM, !s.LastFM.empty())
	setOrDelete("spotify", s.Spotify, !s.Spotify.empty())
	setOrDelete("discordBot", s.DiscordBot, !s.DiscordBot.empty())
	return json.MarshalIndent(out, "", "  ")
}

// Save writes s to a temporary file and renames it over path, so the last
// valid settings file is only ever replaced by a complete one.
func Save(path string, s Settings) error {
	data, err := s.MarshalJSON()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
