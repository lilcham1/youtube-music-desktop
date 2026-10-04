//go:build live

package listen

import (
	"context"
	"testing"
	"time"
)

// go test -tags live -run Live ./internal/listen/ — talks to ntfy.sh.
func TestLiveLatestThenLive(t *testing.T) {
	relay := &Relay{}
	room, _ := NewRoom()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	seal := func(seq int64) string { m, _ := room.Seal(State{Seq: seq, VideoID: "v"}); return m }
	if err := relay.Publish(ctx, room.Topic(), seal(1)); err != nil {
		t.Fatal(err)
	}
	got := make(chan Message, 4)
	go relay.Subscribe(ctx, room.Topic(), func(st time.Time) { t.Logf("open, relay time %v", st) }, func(m Message) {
		t.Logf("message live=%v time=%v len=%d", m.Live, m.Time, len(m.Body))
		got <- m
	})
	select {
	case m := <-got:
		s, err := room.Open(m.Body)
		if err != nil || s.Seq != 1 || m.Live {
			t.Fatalf("catch-up: %+v %v live=%v", s, err, m.Live)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("the latest cached message was not delivered")
	}
}
