package main

import (
	"regexp"
	"strings"
	"testing"
)

// splitCommands returns player.js without comments, split into the
// <commands> block (playback actions the user asked for) and the rest.
func splitCommands(t *testing.T) (commands, rest string) {
	t.Helper()
	src := mustRead("frontend/player.js")
	start, end := strings.Index(src, "// <commands>"), strings.Index(src, "// </commands>")
	if start < 0 || end < start {
		t.Fatal("player.js must keep its playback commands inside // <commands> ... // </commands>")
	}
	strip := regexp.MustCompile(`(?m)^\s*//.*$`)
	return strip.ReplaceAllString(src[start:end], ""), strip.ReplaceAllString(src[:start]+src[end:], "")
}

// The injected script must never silence, pause or start playback on its
// own, and must never write the <video> element's volume behind YouTube's
// back. Earlier releases went silent mid-song because of exactly such
// guards. Playback calls may only appear in the <commands> block, which the
// app invokes when the user presses a control.
func TestPlayerScriptNeverSilencesOrForcesPlayback(t *testing.T) {
	commands, code := splitCommands(t)
	for _, forbidden := range []string{".muted", ".mute(", "unMute(", "setAudioMuted", "'stalled'", "'waiting'", "'error'"} {
		if strings.Contains(code+commands, forbidden) {
			t.Errorf("player.js must not contain %q", forbidden)
		}
	}
	for _, playback := range []string{".play(", ".pause(", "playVideo", "pauseVideo", "nextVideo", "previousVideo", "seekTo", "resolveCommand"} {
		if strings.Contains(code, playback) {
			t.Errorf("%q may only appear inside the <commands> block", playback)
		}
	}
	// Outside the block, the only way in is the command() entry point and the
	// listen-along settle step, which itself only acts on a pending follow.
	for _, name := range []string{"setPlaying(", "watch(", "follow("} {
		if strings.Contains(code, name) {
			t.Errorf("%s is called outside the <commands> block", name)
		}
	}
	if !strings.Contains(commands, "if (!pendingFollow") {
		t.Error("settleFollow must do nothing unless a follow is pending")
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
