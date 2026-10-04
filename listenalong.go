package main

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"youtube-music/internal/listen"
)

const (
	minPublishGap   = time.Second
	rateLimitPause  = time.Minute
	joinWaitForPage = 30 * time.Second
)

// listenAlong runs a listen-along session, as host or guest.
type listenAlong struct {
	d     *desktop
	relay *listen.Relay

	mu           sync.Mutex
	mode         string // "", "host" or "guest"
	room         listen.Room
	host         listen.Host
	guest        listen.Guest
	cancel       context.CancelFunc
	status       string
	following    string // "Title — Artist" the guest hears
	serverOffset time.Duration

	// Host publishing: at most one message per minPublishGap; a newer state
	// replaces a pending one.
	pending     *listen.State
	lastPublish time.Time
	publishing  bool
	pausedUntil time.Time
}

func (l *listenAlong) state() map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := map[string]any{"mode": l.mode, "status": l.status, "following": l.following}
	if l.mode == "host" {
		out["code"] = l.room.Code()
		out["joinUrl"] = l.room.JoinURL()
	}
	return out
}

func (l *listenAlong) setStatus(status string) {
	l.mu.Lock()
	l.status = status
	l.mu.Unlock()
	l.d.pushAllState()
}

// Host starts sharing what this app plays.
func (l *listenAlong) Host() {
	l.Stop()
	room, err := listen.NewRoom()
	if err != nil {
		l.setStatus("Couldn't start a session: " + err.Error())
		return
	}
	l.mu.Lock()
	l.mode, l.room, l.host, l.status = "host", room, listen.Host{}, "Hosting. Share the code or link below; friends with this app can join."
	l.pending, l.lastPublish, l.pausedUntil = nil, time.Time{}, time.Time{}
	l.mu.Unlock()
	l.d.presence.SetJoinURL(room.JoinURL())
	log.Printf("listen along: hosting")
	if pb, _ := l.d.nowPlaying(); pb.VideoID != "" {
		l.hostObserve(pb)
	}
	l.d.pushAllState()
}

func stateOf(pb Playback) listen.State {
	return listen.State{VideoID: pb.VideoID, Title: pb.Title, Artist: pb.Artist, Position: pb.PositionSeconds, Playing: pb.Playing}
}

// hostObserve publishes playback changes while hosting.
func (l *listenAlong) hostObserve(pb Playback) {
	l.mu.Lock()
	if l.mode != "host" {
		l.mu.Unlock()
		return
	}
	st, ok := l.host.Observe(stateOf(pb), time.Now())
	if ok {
		l.pending = &st
	}
	l.mu.Unlock()
	if ok {
		l.flush()
	}
}

// flush publishes the pending state, respecting the minimum gap and any
// rate-limit pause.
func (l *listenAlong) flush() {
	l.mu.Lock()
	if l.mode != "host" || l.pending == nil || l.publishing {
		l.mu.Unlock()
		return
	}
	wait := max(time.Until(l.lastPublish.Add(minPublishGap)), time.Until(l.pausedUntil))
	if wait > 0 {
		l.mu.Unlock()
		time.AfterFunc(wait, l.flush)
		return
	}
	st, room := *l.pending, l.room
	l.pending, l.publishing, l.lastPublish = nil, true, time.Now()
	l.mu.Unlock()

	err := l.publish(room, st)

	l.mu.Lock()
	l.publishing = false
	if errors.Is(err, listen.ErrRateLimited) {
		l.pausedUntil = time.Now().Add(rateLimitPause)
		if l.pending == nil && l.mode == "host" {
			cur := l.host.Current(time.Now())
			l.pending = &cur
		}
		l.status = "The listen-along relay is limiting messages for now. Listeners will catch up in about a minute."
	} else if err != nil {
		l.status = "Couldn't reach the listen-along relay: " + err.Error()
	} else if l.mode == "host" {
		l.status = "Hosting. Share the code or link below; friends with this app can join."
	}
	more := l.pending != nil
	l.mu.Unlock()
	l.d.pushAllState()
	if more {
		l.flush()
	}
}

func (l *listenAlong) publish(room listen.Room, st listen.State) error {
	msg, err := room.Seal(st)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return l.relay.Publish(ctx, room.Topic(), msg)
}

// Join follows a host from a code or link.
func (l *listenAlong) Join(code string) {
	room, err := listen.ParseCode(code)
	if err != nil {
		l.setStatus(sentence(err))
		return
	}
	l.Stop()
	l.d.queue.stopIfActive("Stopped for listen along.")
	l.d.follow.stopIfActive("Stopped to listen along.")
	ctx, cancel := context.WithCancel(context.Background())
	l.mu.Lock()
	l.mode, l.room, l.guest, l.cancel = "guest", room, listen.Guest{}, cancel
	l.status, l.following = "Connecting to the session…", ""
	l.mu.Unlock()
	l.d.pushAllState()
	log.Printf("listen along: joining")
	go func() {
		err := l.relay.Subscribe(ctx, room.Topic(), func(serverTime time.Time) {
			l.mu.Lock()
			l.serverOffset = time.Since(serverTime)
			if l.status == "Connecting to the session…" {
				l.status = "Connected. Waiting for the host to play something…"
			}
			l.mu.Unlock()
			l.d.pushAllState()
		}, func(m listen.Message) { l.onGuestMessage(room, m) })
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("listen along: %v", err)
		}
	}()
}

func (l *listenAlong) onGuestMessage(room listen.Room, m listen.Message) {
	st, err := room.Open(m.Body)
	if err != nil {
		return // not from this room's host
	}
	l.mu.Lock()
	if l.mode != "guest" || l.room != room || !l.guest.Accept(st) {
		l.mu.Unlock()
		return
	}
	offset := l.serverOffset
	l.mu.Unlock()
	if st.Ended {
		l.leaveAsGuest()
		l.setStatus("The host ended the session.")
		return
	}
	relayTime := m.Time
	if m.Live {
		relayTime = time.Time{}
	}
	target := listen.Target(st, relayTime, offset, time.Now())
	l.d.command("follow", map[string]any{"videoId": st.VideoID, "positionSeconds": target, "playing": st.Playing})
	following := st.Title
	if st.Artist != "" {
		following += " — " + st.Artist
	}
	l.mu.Lock()
	l.following = following
	l.status = "Listening along."
	if !st.Playing {
		l.status = "Listening along. The host paused."
	}
	l.mu.Unlock()
	l.d.pushAllState()
}

// Stop ends hosting (telling listeners) or leaves as a guest.
func (l *listenAlong) Stop() {
	l.mu.Lock()
	mode, room := l.mode, l.room
	if l.cancel != nil {
		l.cancel()
		l.cancel = nil
	}
	var final listen.State
	if mode == "host" {
		final = l.host.Current(time.Now())
		final.Ended = true
	}
	l.mode, l.status, l.following, l.pending = "", "", "", nil
	l.mu.Unlock()
	if mode == "host" {
		l.d.presence.SetJoinURL("")
		if err := l.publish(room, final); err != nil {
			log.Printf("listen along: announcing the end: %v", err)
		}
	}
	if mode != "" {
		log.Printf("listen along: stopped (%s)", mode)
		l.d.pushAllState()
	}
}

// leaveAsGuest stops following without touching a hosted session.
func (l *listenAlong) leaveAsGuest() {
	l.mu.Lock()
	guest := l.mode == "guest"
	l.mu.Unlock()
	if guest {
		l.Stop()
	}
}

// joinFromLink handles a ytm-desktop://join/... link, waiting for the
// player page when the app was just started by the link.
func (d *desktop) joinFromLink(link string) {
	deadline := time.Now().Add(joinWaitForPage)
	for time.Now().Before(deadline) {
		d.mu.Lock()
		ready := d.playerReady
		d.mu.Unlock()
		if ready {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	d.listen.Join(link)
	d.openSettings("listen")
}
