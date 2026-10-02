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
	for _, required := range []string{"setVolume(wantedVolume)", "isTrusted", "writePrefVolume", "mediaPlaying()",
		"location.origin !== 'https://music.youtube.com'"} {
		if !strings.Contains(code, required) {
			t.Errorf("player.js lost %q", required)
		}
	}
}

// Changing the volume must never reload the page (that loses the queue and
// position); only the one-time seeding in init may reload.
func TestPlayerScriptReloadsOnlyDuringInit(t *testing.T) {
	code := mustRead("frontend/player.js")
	if n := strings.Count(code, "location.reload("); n != 1 {
		t.Fatalf("location.reload appears %d times, want exactly 1", n)
	}
	initAt := strings.Index(code, "init(level, prefLevel) {")
	setAt := strings.Index(code, "setVolume(level, prefLevel) {")
	reloadAt := strings.Index(code, "location.reload(")
	if initAt < 0 || setAt < 0 || !(initAt < reloadAt && reloadAt < setAt) {
		t.Fatal("the only reload must be inside init, before setVolume")
	}
	if !strings.Contains(code[initAt:setAt], "lastUserInput === 0") {
		t.Fatal("init must not reload once the user has interacted with the page")
	}
}

// The title bar starts window moves and resizes itself (Wails' runtime
// script is not loaded into string-built pages); every edge it can send
// must be one the app accepts.
func TestShellSendsOnlyAcceptedResizeEdges(t *testing.T) {
	shell := mustRead("frontend/shell.html")
	for _, required := range []string{"send('drag')", "send('resize', { edge: at })", "e.buttons & 1"} {
		if !strings.Contains(shell, required) {
			t.Errorf("shell.html lost %q", required)
		}
	}
	found := regexp.MustCompile(`'([nsew]{1,2}-resize)'`).FindAllStringSubmatch(shell, -1)
	if len(found) < 8 {
		t.Fatalf("expected all 8 edges in shell.html, found %d", len(found))
	}
	for _, m := range found {
		if !resizeEdges[m[1]] {
			t.Errorf("shell.html sends %q, which main.go does not accept", m[1])
		}
	}
}
