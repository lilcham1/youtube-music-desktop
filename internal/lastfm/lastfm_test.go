package lastfm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"
)

func TestSign(t *testing.T) {
	// md5("api_keyKmethodauth.getSessiontokenTsecret"), computed independently;
	// format is excluded from the signature.
	p := url.Values{"method": {"auth.getSession"}, "api_key": {"K"}, "token": {"T"}, "format": {"json"}}
	if got, want := Sign(p, "secret"), "37d55b7ad890b24ee678f74fac66d197"; got != want {
		t.Fatalf("Sign = %s; want %s", got, want)
	}
}

func TestClientAuthAndScrobble(t *testing.T) {
	var lastForm url.Values
	approved := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		lastForm = r.Form
		if r.Form.Get("api_sig") != Sign(withoutSig(r.Form), "S") {
			fmt.Fprint(w, `{"error":13,"message":"Invalid method signature supplied"}`)
			return
		}
		switch r.Form.Get("method") {
		case "auth.getToken":
			fmt.Fprint(w, `{"token":"tok"}`)
		case "auth.getSession":
			if !approved {
				fmt.Fprint(w, `{"error":14,"message":"This token has not been authorized"}`)
				return
			}
			fmt.Fprint(w, `{"session":{"name":"marou","key":"SK","subscriber":0}}`)
		case "track.scrobble", "track.updateNowPlaying":
			if r.Method != http.MethodPost || r.Form.Get("sk") != "SK" {
				fmt.Fprint(w, `{"error":9,"message":"Invalid session key"}`)
				return
			}
			fmt.Fprint(w, `{}`)
		}
	}))
	defer srv.Close()
	c := &Client{APIKey: "K", Secret: "S", APIURL: srv.URL}
	ctx := context.Background()

	token, approve, err := c.Token(ctx)
	if err != nil || token != "tok" || approve != "https://www.last.fm/api/auth/?api_key=K&token=tok" {
		t.Fatalf("Token = %q %q %v", token, approve, err)
	}
	if _, _, err := c.Session(ctx, token); !NotAuthorizedYet(err) {
		t.Fatalf("before approval: %v", err)
	}
	approved = true
	key, user, err := c.Session(ctx, token)
	if err != nil || key != "SK" || user != "marou" {
		t.Fatalf("Session = %q %q %v", key, user, err)
	}

	c.SessionKey = key
	start := time.Unix(1790000000, 0)
	tracks := []Track{{Artist: "A", Title: "One", Album: "X", Duration: 200 * time.Second, Started: start}, {Artist: "B", Title: "Two", Started: start.Add(time.Minute)}}
	if err := c.Scrobble(ctx, tracks); err != nil {
		t.Fatal(err)
	}
	if lastForm.Get("artist[1]") != "B" || lastForm.Get("timestamp[0]") != "1790000000" || lastForm.Get("duration[0]") != "200" || lastForm.Get("album[0]") != "X" {
		t.Fatalf("scrobble form = %v", lastForm)
	}
	c.SessionKey = "stale"
	if err := c.NowPlaying(ctx, tracks[0]); !SessionInvalid(err) {
		t.Fatalf("stale session: %v", err)
	}
}

func withoutSig(v url.Values) url.Values {
	out := url.Values{}
	for k, vals := range v {
		if k != "api_sig" {
			out[k] = vals
		}
	}
	return out
}

var t0 = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

func song(id string, length time.Duration) Observation {
	return Observation{ID: id, Track: Track{Artist: "Artist", Title: "Title " + id, Duration: length}}
}

func at(o Observation, playing bool, pos time.Duration) Observation {
	o.Playing, o.Position = playing, pos
	return o
}

// tickUntil ticks every 10 s (as the app does) from start to end and
// returns the first scrobble, if any.
func tickUntil(tr *Tracker, start, end time.Time) *Track {
	for now := start.Add(10 * time.Second); !now.After(end); now = now.Add(10 * time.Second) {
		if ev := tr.Tick(now); ev.Scrobble != nil {
			return ev.Scrobble
		}
	}
	return nil
}

func TestTrackerScrobblesAtHalfOrFourMinutes(t *testing.T) {
	var tr Tracker
	s := song("a", 3*time.Minute)
	ev := tr.Observe(at(s, true, 0), t0)
	if ev.NowPlaying == nil || ev.Scrobble != nil {
		t.Fatalf("start: %+v", ev)
	}
	if sc := tickUntil(&tr, t0, t0.Add(80*time.Second)); sc != nil {
		t.Fatal("must not scrobble before half the song")
	}
	ev = tr.Tick(t0.Add(90 * time.Second))
	if ev.Scrobble == nil || !ev.Scrobble.Started.Equal(t0) {
		t.Fatalf("at half: %+v", ev)
	}
	if ev := tr.Tick(t0.Add(150 * time.Second)); ev.Scrobble != nil {
		t.Fatal("a play is scrobbled once")
	}

	// A 20-minute mix scrobbles at four minutes.
	var long Tracker
	m := song("mix", 20*time.Minute)
	long.Observe(at(m, true, 0), t0)
	for i := 1; i <= 8; i++ { // ticks every 30 s
		if ev := long.Tick(t0.Add(time.Duration(i) * 30 * time.Second)); ev.Scrobble != nil {
			if i != 8 {
				t.Fatalf("scrobbled after %d s", i*30)
			}
			return
		}
	}
	t.Fatal("long track never scrobbled")
}

func TestTrackerCountsOnlyPlayingTime(t *testing.T) {
	var tr Tracker
	s := song("a", 2*time.Minute)
	tr.Observe(at(s, true, 0), t0)
	tr.Observe(at(s, false, 30*time.Second), t0.Add(30*time.Second)) // paused
	if ev := tr.Tick(t0.Add(10 * time.Minute)); ev.Scrobble != nil {
		t.Fatal("paused time must not count")
	}
	tr.Observe(at(s, true, 30*time.Second), t0.Add(10*time.Minute))
	if ev := tr.Tick(t0.Add(10*time.Minute + 29*time.Second)); ev.Scrobble != nil {
		t.Fatal("59 s played of 120 is not enough")
	}
	if ev := tr.Tick(t0.Add(10*time.Minute + 30*time.Second)); ev.Scrobble == nil {
		t.Fatal("60 s of 120 must scrobble")
	}
}

func TestTrackerSkipsShortSongsAndHandlesRepeats(t *testing.T) {
	var tr Tracker
	short := song("s", 25*time.Second)
	if ev := tr.Observe(at(short, true, 0), t0); ev.NowPlaying != nil {
		t.Fatal("songs of 30 s or less are never announced")
	}
	if ev := tr.Tick(t0.Add(25 * time.Second)); ev.Scrobble != nil {
		t.Fatal("songs of 30 s or less are never scrobbled")
	}

	var rep Tracker
	s := song("r", 2*time.Minute)
	rep.Observe(at(s, true, 0), t0)
	if sc := tickUntil(&rep, t0, t0.Add(100*time.Second)); sc == nil {
		t.Fatal("first play must scrobble")
	}
	rep.Observe(at(s, true, 115*time.Second), t0.Add(115*time.Second))
	// The same song restarts from the top: a second play.
	restart := t0.Add(121 * time.Second)
	ev := rep.Observe(at(s, true, 0), restart)
	if ev.NowPlaying == nil {
		t.Fatal("a repeat is a new play")
	}
	if sc := tickUntil(&rep, restart, restart.Add(60*time.Second)); sc == nil || !sc.Started.Equal(restart) {
		t.Fatalf("repeat scrobble: %+v", sc)
	}
}

func TestTrackerIgnoresSleepGaps(t *testing.T) {
	var tr Tracker
	s := song("a", 10*time.Minute)
	tr.Observe(at(s, true, 0), t0)
	// The PC slept for an hour while "playing": only 30 s may count.
	if ev := tr.Tick(t0.Add(time.Hour)); ev.Scrobble != nil {
		t.Fatal("a sleep gap must not count as listening")
	}
}

func TestQueuePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.json")
	q := LoadQueue(path)
	q.Add(Track{Artist: "A", Title: "1", Started: t0})
	q.Add(Track{Artist: "B", Title: "2", Started: t0})
	again := LoadQueue(path)
	if again.Len() != 2 || again.Peek(1)[0].Title != "1" {
		t.Fatalf("reloaded queue = %d items", again.Len())
	}
	again.Drop(2)
	if LoadQueue(path).Len() != 0 {
		t.Fatal("an emptied queue must stay empty")
	}
}

func TestParseRecent(t *testing.T) {
	// The shape of user.getRecentTracks with limit=1: the "now playing" track
	// plus the last scrobble.
	now := `{"recenttracks":{"track":[
		{"artist":{"mbid":"","#text":"Ne-Yo"},"streamable":"0","name":"So Sick","album":{"mbid":"","#text":"In My Own Words"},"@attr":{"nowplaying":"true"}},
		{"artist":{"#text":"Usher"},"name":"Burn","album":{"#text":"Confessions"},"date":{"uts":"1790000000","#text":"x"}}
	],"@attr":{"user":"friend","page":"1"}}}`
	p, err := parseRecent([]byte(now))
	if err != nil || p == nil || p.Artist != "Ne-Yo" || p.Title != "So Sick" || p.Album != "In My Own Words" {
		t.Fatalf("now playing = %+v, %v", p, err)
	}
	// Only a past scrobble, sent as a single object.
	idle := `{"recenttracks":{"track":{"artist":{"#text":"Usher"},"name":"Burn","date":{"uts":"1790000000"}},"@attr":{"user":"friend"}}}`
	if p, err := parseRecent([]byte(idle)); err != nil || p != nil {
		t.Fatalf("idle = %+v, %v", p, err)
	}
	if _, err := parseRecent([]byte(`{"error":6,"message":"User not found"}`)); !errors.Is(err, ErrUnknownUser) {
		t.Fatalf("unknown user: %v", err)
	}
	if (Playing{Artist: "A", Title: "Song"}).Key() != (Playing{Artist: "a", Title: "SONG"}).Key() {
		t.Fatal("keys ignore case")
	}
}
