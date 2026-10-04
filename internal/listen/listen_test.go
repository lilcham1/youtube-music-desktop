package listen

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCodeRoundTripAndLinks(t *testing.T) {
	r, err := NewRoom()
	if err != nil {
		t.Fatal(err)
	}
	code := r.Code()
	if !strings.HasPrefix(code, "ytm1-") || strings.ContainsAny(code, "+/=") {
		t.Fatalf("code %q must be URL-safe", code)
	}
	for _, in := range []string{code, "  " + code + "\n", r.JoinURL(), "ytm-desktop://join/" + code, "ytm-desktop://join/" + code + "/"} {
		got, err := ParseCode(in)
		if err != nil || got != r {
			t.Errorf("ParseCode(%q) = %v", in, err)
		}
	}
	for _, bad := range []string{"", "hello", "ytm1-short", "ytm1-" + strings.Repeat("A", 60)} {
		if _, err := ParseCode(bad); err != ErrBadCode {
			t.Errorf("ParseCode(%q) err = %v", bad, err)
		}
	}
	other, _ := NewRoom()
	if r.Topic() == other.Topic() || r.Code() == other.Code() {
		t.Fatal("rooms must be random")
	}
	if strings.Contains(r.Topic(), code[5:20]) {
		t.Fatal("the topic must not reveal the key")
	}
}

func TestSealOpenRejectsOtherRoomsAndTampering(t *testing.T) {
	r, _ := NewRoom()
	s := State{Seq: 3, VideoID: "abc", Title: "Song", Position: 42.5, Playing: true}
	msg, err := r.Seal(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(msg, "abc") || strings.Contains(msg, "Song") {
		t.Fatal("the relay must not see song details")
	}
	if got, err := r.Open(msg); err != nil || got != s {
		t.Fatalf("Open = %+v, %v", got, err)
	}
	other, _ := NewRoom()
	if _, err := other.Open(msg); err == nil {
		t.Fatal("another room's key must not open the message")
	}
	tampered := []byte(msg)
	tampered[len(tampered)/2] ^= 1
	if _, err := r.Open(string(tampered)); err == nil {
		t.Fatal("a tampered message must be rejected")
	}
	if _, err := r.Open("spam on the topic"); err == nil {
		t.Fatal("plain text must be rejected")
	}
}

var now0 = time.Date(2026, 10, 3, 20, 0, 0, 0, time.UTC)

func TestHostPublishesOnlyRealChanges(t *testing.T) {
	var h Host
	st := func(id string, pos float64, playing bool) State {
		return State{VideoID: id, Position: pos, Playing: playing}
	}
	type step struct {
		at      time.Duration
		s       State
		publish bool
	}
	steps := []step{
		{0, st("a", 0, true), true},                    // first state
		{5 * time.Second, st("a", 5, true), false},     // normal progress
		{10 * time.Second, st("a", 10.5, true), false}, // small jitter
		{20 * time.Second, st("a", 80, true), true},    // seek forward
		{25 * time.Second, st("a", 85, false), true},   // pause
		{90 * time.Second, st("a", 85, false), false},  // still paused
		{95 * time.Second, st("a", 85, true), true},    // resume
		{96 * time.Second, st("b", 0, true), true},     // next song
		{97 * time.Second, State{}, false},             // nothing loaded
	}
	var last int64
	for i, sp := range steps {
		out, ok := h.Observe(sp.s, now0.Add(sp.at))
		if ok != sp.publish {
			t.Fatalf("step %d: publish = %v, want %v", i, ok, sp.publish)
		}
		if ok {
			if out.Seq <= last {
				t.Fatalf("step %d: seq %d not increasing", i, out.Seq)
			}
			last = out.Seq
		}
	}
}

func TestGuestOrderingAndTarget(t *testing.T) {
	var g Guest
	if !g.Accept(State{Seq: 2}) || g.Accept(State{Seq: 2}) || g.Accept(State{Seq: 1}) || !g.Accept(State{Seq: 5}) {
		t.Fatal("guests apply each state once, in order")
	}
	playing := State{Position: 100, Playing: true}
	if got := Target(playing, time.Time{}, 0, now0); math.Abs(got-100.3) > 1e-9 {
		t.Fatalf("live target = %v", got)
	}
	// Joined late: the relay got the message 40 s ago; the guest's clock is
	// 5 s ahead of the relay's.
	if got := Target(playing, now0.Add(-40*time.Second), 5*time.Second, now0.Add(5*time.Second)); math.Abs(got-140) > 1e-9 {
		t.Fatalf("late-join target = %v", got)
	}
	if got := Target(State{Position: 100}, now0.Add(-40*time.Second), 0, now0); got != 100 {
		t.Fatalf("paused target = %v", got)
	}
}

// fakeNtfy is a minimal ntfy server like ntfy.sh: POST stores; a poll
// (poll=1&since=latest) returns the latest message; a stream sends "open",
// the messages after since (a message ID or a Unix time), then live ones.
// Like ntfy.sh, its streaming since=latest misses messages, so the client
// must not rely on it.
type fakeNtfy struct {
	mu   sync.Mutex
	msgs []fakeMsg
	subs []chan string
	cnt  int
}

type fakeMsg struct {
	id   string
	time int64
	line string
}

func (f *fakeNtfy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.cnt++
		m := fakeMsg{id: fmt.Sprintf("m%d", f.cnt), time: time.Now().Unix()}
		m.line = fmt.Sprintf(`{"id":%q,"time":%d,"event":"message","message":%q}`, m.id, m.time, body)
		f.msgs = append(f.msgs, m)
		for _, c := range f.subs {
			c <- m.line
		}
		f.mu.Unlock()
		return
	}
	since := r.URL.Query().Get("since")
	if r.URL.Query().Get("poll") == "1" {
		f.mu.Lock()
		if since == "latest" && len(f.msgs) > 0 {
			fmt.Fprintln(w, f.msgs[len(f.msgs)-1].line)
		}
		f.mu.Unlock()
		return
	}
	flusher := w.(http.Flusher)
	c := make(chan string, 10)
	f.mu.Lock()
	fmt.Fprintf(w, `{"id":"o","time":%d,"event":"open"}`+"\n", time.Now().Unix())
	after := -1
	for i, m := range f.msgs {
		if m.id == since {
			after = i
		}
	}
	for i, m := range f.msgs {
		ts, err := strconv.ParseInt(since, 10, 64)
		if (after >= 0 && i > after) || (err == nil && m.time >= ts) {
			fmt.Fprintln(w, m.line)
		}
	}
	f.subs = append(f.subs, c)
	f.mu.Unlock()
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case line := <-c:
			fmt.Fprintln(w, line)
			flusher.Flush()
		}
	}
}

func TestRelayDeliversLatestThenLive(t *testing.T) {
	srv := httptest.NewServer(&fakeNtfy{})
	defer srv.Close()
	relay := &Relay{BaseURL: srv.URL}
	room, _ := NewRoom()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	seal := func(seq int64) string { m, _ := room.Seal(State{Seq: seq, VideoID: "v"}); return m }
	if err := relay.Publish(ctx, room.Topic(), seal(1)); err != nil {
		t.Fatal(err)
	}
	if err := relay.Publish(ctx, room.Topic(), seal(2)); err != nil {
		t.Fatal(err)
	}
	got := make(chan State, 4)
	opened := make(chan struct{}, 1)
	go relay.Subscribe(ctx, room.Topic(), func(time.Time) { opened <- struct{}{} }, func(m Message) {
		if s, err := room.Open(m.Body); err == nil {
			got <- s
		}
	})
	<-opened
	if s := <-got; s.Seq != 2 {
		t.Fatalf("a late joiner must get the latest state first, got seq %d", s.Seq)
	}
	if err := relay.Publish(ctx, room.Topic(), seal(3)); err != nil {
		t.Fatal(err)
	}
	select {
	case s := <-got:
		if s.Seq != 3 {
			t.Fatalf("live seq %d", s.Seq)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("live message not delivered")
	}
}
