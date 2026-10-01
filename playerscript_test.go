package main

import (
	"regexp"
	"strings"
	"testing"
)

// The injected script must never silence, pause or start playback, and
// must never write the <video> element's volume behind YouTube's back.
// Earlier releases went silent mid-song because of exactly such guards.
func TestPlayerScriptNeverSilencesOrForcesPlayback(t *testing.T) {
	src := mustRead("frontend/player.js")
	code := regexp.MustCompile(`(?m)^\s*//.*$`).ReplaceAllString(src, "")
	for _, forbidden := range []string{
		".muted", ".mute(", "unMute(", "setAudioMuted",
		".play(", ".pause(", "playVideo", "pauseVideo",
		"'stalled'", "'waiting'", "'error'",
	} {
		if strings.Contains(code, forbidden) {
			t.Errorf("player.js must not contain %q", forbidden)
		}
	}
	if regexp.MustCompile(`video\w*\.volume\s*=[^=]`).MatchString(code) {
		t.Error("player.js must not assign a media element's volume")
	}
	for _, required := range []string{"setVolume(wantedVolume)", "isTrusted", "writePrefVolume", "mediaPlaying()"} {
		if !strings.Contains(code, required) {
			t.Errorf("player.js lost %q", required)
		}
	}
}
