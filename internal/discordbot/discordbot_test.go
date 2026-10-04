package discordbot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// spotifyActivity is the shape Discord sends for a Spotify listener.
func spotifyActivity(track, title, artists string, start, end int64) map[string]any {
	return map[string]any{
		"type": 2, "name": "Spotify", "id": "spotify:1", "details": title, "state": artists,
		"sync_id": track, "timestamps": map[string]any{"start": start, "end": end},
		"assets": map[string]any{"large_image": "spotify:ab67", "large_text": "Album " + title},
		"party":  map[string]any{"id": "spotify:42"},
	}
}

func TestSpotifyFrom(t *testing.T) {
	raw, _ := json.Marshal([]any{
		map[string]any{"type": 0, "name": "Some Game"},
		map[string]any{"type": 4, "name": "Custom Status", "state": "busy"},
		spotifyActivity("T1", "Song", "Artist A; Artist B", 1000, 201000),
	})
	var acts []activity
	_ = json.Unmarshal(raw, &acts)
	s := spotifyFrom(acts)
	if s == nil || s.TrackID != "T1" || s.Title != "Song" || strings.Join(s.Artists, "|") != "Artist A|Artist B" || s.Album != "Album Song" {
		t.Fatalf("spotify = %+v", s)
	}
	if s.Duration() != 200*time.Second || s.Position(time.UnixMilli(61000)) != 60*time.Second {
		t.Fatalf("duration %v position %v", s.Duration(), s.Position(time.UnixMilli(61000)))
	}
	if spotifyFrom(acts[:2]) != nil {
		t.Fatal("no Spotify activity means not playing")
	}
}

// fakeGateway speaks enough of Discord's gateway: hello, identify (checked),
// heartbeat acks, then the events the test pushes.
type fakeGateway struct {
	t         *testing.T
	closeWith websocket.StatusCode
	identify  chan map[string]any
	push      chan any
	mu        sync.Mutex
	conns     int
}

func (g *fakeGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	g.mu.Lock()
	g.conns++
	g.mu.Unlock()
	ctx := r.Context()
	send := func(v any) { b, _ := json.Marshal(v); _ = conn.Write(ctx, websocket.MessageText, b) }
	send(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 100}})
	go func() {
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var p map[string]any
			_ = json.Unmarshal(data, &p)
			switch p["op"] {
			case float64(2):
				g.identify <- p["d"].(map[string]any)
				if g.closeWith != 0 {
					conn.Close(g.closeWith, "nope")
					return
				}
			case float64(1):
				send(map[string]any{"op": 11})
			}
		}
	}()
	seq := 0
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-g.push:
			seq++
			e := ev.(map[string]any)
			e["op"], e["s"] = 0, seq
			send(e)
		}
	}
}

func TestGatewayTracksSpotifyListeners(t *testing.T) {
	g := &fakeGateway{t: t, identify: make(chan map[string]any, 1), push: make(chan any, 10)}
	srv := httptest.NewServer(g)
	defer srv.Close()
	changes := make(chan string, 20)
	c := &Client{Token: "secret-token", URL: "ws" + strings.TrimPrefix(srv.URL, "http"), OnChange: func(id string) { changes <- id }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	id := <-g.identify
	if id["token"] != "secret-token" || id["intents"] != float64(intents) {
		t.Fatalf("identify = %v", id)
	}
	g.push <- map[string]any{"t": "READY", "d": map[string]any{"user": map[string]any{"id": "BOT1", "username": "My Bot"}, "guilds": []any{map[string]any{"id": "G1", "unavailable": true}}}}
	g.push <- map[string]any{"t": "GUILD_CREATE", "d": map[string]any{
		"id": "G1",
		"members": []any{
			map[string]any{"user": map[string]any{"id": "U1", "username": "alice", "global_name": "Alice"}},
			map[string]any{"user": map[string]any{"id": "U2", "username": "bob"}, "nick": "Bobby"},
			map[string]any{"user": map[string]any{"id": "BOT1", "username": "My Bot", "bot": true}},
		},
		"presences": []any{
			map[string]any{"user": map[string]any{"id": "U1"}, "activities": []any{spotifyActivity("T1", "Song One", "Artist", 1000, 200000)}},
			map[string]any{"user": map[string]any{"id": "U2"}, "activities": []any{}},
		},
	}}
	waitFor(t, changes, func() bool { return len(c.Listening()) == 1 })
	st := c.Status()
	if !st.Connected || st.BotID != "BOT1" || st.BotName != "My Bot" || st.Servers != 1 {
		t.Fatalf("status = %+v", st)
	}
	if l := c.Listening()[0]; l.Name != "Alice" || l.Spotify.Title != "Song One" {
		t.Fatalf("listener = %+v", l)
	}

	// Bob starts a song; Alice pauses (Discord drops her activity).
	g.push <- map[string]any{"t": "PRESENCE_UPDATE", "d": map[string]any{"user": map[string]any{"id": "U2"}, "activities": []any{spotifyActivity("T2", "Song Two", "Band", 5000, 99000)}}}
	g.push <- map[string]any{"t": "PRESENCE_UPDATE", "d": map[string]any{"user": map[string]any{"id": "U1"}, "activities": []any{}}}
	waitFor(t, changes, func() bool {
		l := c.Listening()
		return len(l) == 1 && l[0].Name == "Bobby"
	})
	if c.Get("U1").Spotify != nil {
		t.Fatal("a paused listener has no Spotify state")
	}
	if InviteURL("BOT1") != "https://discord.com/oauth2/authorize?client_id=BOT1&scope=bot&permissions=0" {
		t.Fatal("invite URL")
	}
}

func TestGatewayExplainsFatalCloseCodes(t *testing.T) {
	for code, want := range map[websocket.StatusCode]error{4004: ErrBadToken, 4014: ErrIntents} {
		g := &fakeGateway{t: t, identify: make(chan map[string]any, 1), push: make(chan any), closeWith: code}
		srv := httptest.NewServer(g)
		c := &Client{Token: "x", URL: "ws" + strings.TrimPrefix(srv.URL, "http")}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := c.Run(ctx)
		cancel()
		srv.Close()
		if !errors.Is(err, want) {
			t.Fatalf("close %d: err = %v, want %v", code, err, want)
		}
		if g.conns != 1 {
			t.Fatalf("close %d: a fatal error must not retry (%d connections)", code, g.conns)
		}
	}
}

func waitFor(t *testing.T, changes chan string, ok func() bool) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for !ok() {
		select {
		case <-changes:
		case <-deadline:
			t.Fatal("timed out waiting for the expected state")
		}
	}
}
