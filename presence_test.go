package main

import (
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"

	"youtube-music/internal/discord"
)

// fakeDiscord answers the handshake and records SET_ACTIVITY details.
type fakeDiscord struct {
	mu      sync.Mutex
	details []string
}

func (f *fakeDiscord) serve(conn net.Conn) {
	defer conn.Close()
	if _, _, err := discord.ReadFrame(conn); err != nil {
		return
	}
	_ = discord.WriteFrame(conn, 1, map[string]any{"cmd": "DISPATCH", "evt": "READY"})
	for {
		op, body, err := discord.ReadFrame(conn)
		if err != nil || op == 2 {
			return
		}
		var req struct {
			Cmd   string `json:"cmd"`
			Nonce string `json:"nonce"`
			Args  struct {
				Activity *struct {
					Details string `json:"details"`
				} `json:"activity"`
			} `json:"args"`
		}
		_ = json.Unmarshal(body, &req)
		if req.Args.Activity != nil {
			f.mu.Lock()
			f.details = append(f.details, req.Args.Activity.Details)
			f.mu.Unlock()
		}
		_ = discord.WriteFrame(conn, 1, map[string]any{"cmd": req.Cmd, "nonce": req.Nonce})
	}
}

func (f *fakeDiscord) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.details...)
}

func TestPresenceCoalescesBurstsAndSendsLatest(t *testing.T) {
	f := &fakeDiscord{}
	p := newPresence(nil)
	p.dial = func(string, time.Duration) (net.Conn, error) {
		client, server := net.Pipe()
		go f.serve(server)
		return client, nil
	}
	p.Configure(true, "123")

	// A seek-bar drag: many reports within a fraction of a second.
	for i := 0; i < 20; i++ {
		p.Update(Playback{Playing: true, Title: "Song", Artist: "Artist", PositionSeconds: float64(i * 7)})
		time.Sleep(15 * time.Millisecond)
	}
	p.Update(Playback{Playing: true, Title: "Last song", Artist: "Artist", PositionSeconds: 1})

	deadline := time.Now().Add(minActivityInterval + 2*time.Second)
	for time.Now().Before(deadline) {
		if s := f.sent(); len(s) > 0 && s[len(s)-1] == "Last song" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	sent := f.sent()
	if len(sent) == 0 || sent[len(sent)-1] != "Last song" {
		t.Fatalf("latest state not sent: %v", sent)
	}
	if len(sent) > 2 {
		t.Fatalf("burst of 21 reports sent %d activities, want at most 2: %v", len(sent), sent)
	}
	if got := p.Status(); got != "Connected — showing the current song." {
		t.Fatalf("status = %q", got)
	}
	p.Close()
	if got := p.Status(); got != "Off" {
		t.Fatalf("status after Close = %q", got)
	}
}
