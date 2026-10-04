package discord

import (
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

type fakeDiscord struct {
	t        *testing.T
	rejectOn func(activity map[string]any) bool
	got      chan map[string]any
}

// serve answers a handshake and SET_ACTIVITY requests on conn.
func (f *fakeDiscord) serve(conn net.Conn) {
	defer conn.Close()
	op, body, err := ReadFrame(conn)
	if err != nil || op != opHandshake {
		return
	}
	var hs map[string]any
	_ = json.Unmarshal(body, &hs)
	_ = WriteFrame(conn, opFrame, map[string]any{"cmd": "DISPATCH", "evt": "READY", "data": map[string]any{"client_id": hs["client_id"]}})
	for {
		op, body, err := ReadFrame(conn)
		if err != nil || op == opClose {
			return
		}
		var req struct {
			Cmd   string         `json:"cmd"`
			Nonce string         `json:"nonce"`
			Args  map[string]any `json:"args"`
		}
		_ = json.Unmarshal(body, &req)
		activity, _ := req.Args["activity"].(map[string]any)
		f.got <- req.Args
		// An unrelated event first: the client must wait for its own nonce.
		_ = WriteFrame(conn, opFrame, map[string]any{"cmd": "DISPATCH", "evt": "ACTIVITY_JOIN", "nonce": nil})
		if activity != nil && f.rejectOn != nil && f.rejectOn(activity) {
			_ = WriteFrame(conn, opFrame, map[string]any{"cmd": req.Cmd, "evt": "ERROR", "nonce": req.Nonce, "data": map[string]any{"message": "bad asset"}})
			continue
		}
		_ = WriteFrame(conn, opFrame, map[string]any{"cmd": req.Cmd, "nonce": req.Nonce, "data": map[string]any{}})
	}
}

func (f *fakeDiscord) dialer(failFirst int) Dialer {
	attempt := 0
	return func(path string, _ time.Duration) (net.Conn, error) {
		if !strings.HasPrefix(path, `\\.\pipe\discord-ipc-`) {
			f.t.Fatalf("unexpected pipe %q", path)
		}
		attempt++
		if attempt <= failFirst {
			return nil, errors.New("no pipe")
		}
		client, server := net.Pipe()
		go f.serve(server)
		return client, nil
	}
}

func TestSetActivityAndArtworkFallback(t *testing.T) {
	f := &fakeDiscord{t: t, got: make(chan map[string]any, 4),
		rejectOn: func(a map[string]any) bool { _, has := a["assets"]; return has }}
	c, err := Connect(f.dialer(2), "123")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	track := Track{Title: "Song", Artist: "Artist", Album: "song", Artwork: "https://x/a.jpg", StartMs: 1000, EndMs: 5000}
	if err := c.SetActivity(track); err != nil {
		t.Fatal(err)
	}
	first, second := (<-f.got)["activity"].(map[string]any), (<-f.got)["activity"].(map[string]any)
	assets := first["assets"].(map[string]any)
	if first["type"] != 2.0 || first["details"] != "Song" || first["state"] != "Artist" || assets["large_image"] != "https://x/a.jpg" {
		t.Fatalf("activity = %v", first)
	}
	if _, dup := assets["large_text"]; dup {
		t.Fatal("album equal to title must not be repeated")
	}
	if _, has := second["assets"]; has {
		t.Fatal("retry must drop assets")
	}
	if ts := second["timestamps"].(map[string]any); ts["start"] != 1000.0 || ts["end"] != 5000.0 {
		t.Fatalf("timestamps = %v", ts)
	}
	if err := c.ClearActivity(); err != nil {
		t.Fatal(err)
	}
	if args := <-f.got; args["activity"] != nil {
		t.Fatalf("clear must omit activity: %v", args)
	}
}

func TestConnectFailsWhenDiscordIsClosed(t *testing.T) {
	f := &fakeDiscord{t: t}
	if _, err := Connect(f.dialer(10), "123"); err == nil {
		t.Fatal("expected error when no pipe answers")
	}
}

func TestJoinButtonOnlyWhenHosting(t *testing.T) {
	plain := BuildActivity(Track{Title: "Song", Artist: "A"})
	if _, ok := plain["buttons"]; ok {
		t.Fatal("no button without a join link")
	}
	hosting := BuildActivity(Track{Title: "Song", Artist: "A", JoinURL: "https://example.test/join/#ytm1-x"})
	buttons, ok := hosting["buttons"].([]map[string]string)
	if !ok || len(buttons) != 1 || buttons[0]["label"] != "Listen along" || buttons[0]["url"] != "https://example.test/join/#ytm1-x" {
		t.Fatalf("buttons = %#v", hosting["buttons"])
	}
}

func TestDownloadButtonFollowsListenAlong(t *testing.T) {
	only := BuildActivity(Track{Title: "Song", Artist: "A", DownloadURL: "https://example.test/"})
	if b, _ := only["buttons"].([]map[string]string); len(b) != 1 || b[0]["label"] != "Get Encore" || b[0]["url"] != "https://example.test/" {
		t.Fatalf("buttons = %#v", only["buttons"])
	}
	both := BuildActivity(Track{Title: "Song", Artist: "A", JoinURL: "https://example.test/join/#x", DownloadURL: "https://example.test/"})
	b, _ := both["buttons"].([]map[string]string)
	if len(b) != 2 || b[0]["label"] != "Listen along" || b[1]["label"] != "Get Encore" {
		t.Fatalf("buttons = %#v", both["buttons"])
	}
}

func TestRejectedButtonIsDroppedNotTheStatus(t *testing.T) {
	f := &fakeDiscord{t: t, got: make(chan map[string]any, 4),
		rejectOn: func(a map[string]any) bool { _, has := a["buttons"]; return has }}
	c, err := Connect(f.dialer(0), "123")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.SetActivity(Track{Title: "Song", Artist: "A", JoinURL: "https://example.test/join/#x"}); err != nil {
		t.Fatalf("the status must still be set: %v", err)
	}
	var last map[string]any
	for i := 0; i < 3; i++ {
		last = (<-f.got)["activity"].(map[string]any)
	}
	if _, has := last["buttons"]; has {
		t.Fatal("the final attempt must drop the button")
	}
	if last["details"] != "Song" {
		t.Fatalf("final activity = %v", last)
	}
}
