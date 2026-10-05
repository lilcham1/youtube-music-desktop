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
	// A guest checks in this often; a host that heard a check-in within
	// listenerWindow keeps publishing.
	checkInEvery   = 10 * time.Minute
	listenerWindow = 25 * time.Minute
	// A cached state older than this is from before the guest joined (a
	// session paused for long, or an earlier one); the host's answer to the
	// check-in brings the current state instead.
	staleCached = 2 * time.Minute
	// Leave time for the end-of-session message, but never hold up quitting.
	publishTimeout  = 15 * time.Second
	shutdownTimeout = 2 * time.Second
)

// listenAlong runs a listen-along session, as host or guest.
type listenAlong struct {
	d     *desktop
	relay *listen.Relay

	mu           sync.Mutex
	mode         string // "", "host" or "guest"
	auto         bool   // hosting only for the Discord "Listen along" button
	autoOff      bool   // the user stopped hosting; don't restart it this run
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
	// listenersUntil is when the last check-in from a guest runs out.
	listenersUntil time.Time
}

// audience reports whether playback changes should go to the relay: always
// for a session started from Settings (friends on versions before check-ins
// can still follow it), and for the Discord button's always-on session only
// while someone has checked in. That keeps an always-on session within the
// relay's daily message limit (250 per IP on ntfy.sh). Callers hold l.mu.
func (l *listenAlong) audience(now time.Time) bool {
	return !l.auto || now.Before(l.listenersUntil)
}

func (l *listenAlong) state() map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := map[string]any{"mode": l.mode, "status": l.status, "following": l.following, "auto": l.auto}
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

const (
	hostingStatus     = "Hosting. Share the code or link below; friends with this app can join."
	autoHostingStatus = "Sharing on your Discord status: friends can press “Listen along” there, or join with the code below."
)

func (l *listenAlong) hostingStatus() string {
	if l.auto {
		return autoHostingStatus
	}
	return hostingStatus
}

// Host starts sharing what this app plays. A session already running for
// the Discord button is kept (same code) and becomes a regular one.
func (l *listenAlong) Host() {
	l.mu.Lock()
	if l.mode == "host" {
		wasAuto := l.auto
		l.auto, l.status = false, hostingStatus
		// Changes made while nobody listened weren't sent; catch up now.
		if cur := l.host.Current(time.Now()); wasAuto && cur.VideoID != "" {
			l.pending = &cur
		}
		l.mu.Unlock()
		l.d.pushAllState()
		l.flush()
		return
	}
	l.mu.Unlock()
	l.startHosting(false)
}

// ensureAuto keeps a session running for the Discord "Listen along"
// button: started while that option and the Discord status are on and
// listen along isn't otherwise in use, ended when either is turned off.
func (l *listenAlong) ensureAuto() {
	cfg := l.d.config()
	want := cfg.DiscordEnabled && cfg.DiscordListenAlong
	l.mu.Lock()
	mode, auto, off := l.mode, l.auto, l.autoOff
	l.mu.Unlock()
	switch {
	case want && mode == "" && !off:
		l.startHosting(true)
	case !want && mode == "host" && auto:
		l.Stop()
	}
}

// autoSettingChanged lets the Discord button start a session again after
// the user stopped one, once they change the option.
func (l *listenAlong) autoSettingChanged() {
	l.mu.Lock()
	l.autoOff = false
	l.mu.Unlock()
}

// StopByUser is the Stop button: a stopped session stays stopped for this
// run, and leaving someone else's session brings the Discord button back.
func (l *listenAlong) StopByUser() {
	l.mu.Lock()
	mode := l.mode
	if mode == "host" {
		l.autoOff = true
	}
	l.mu.Unlock()
	l.Stop()
	if mode == "guest" {
		l.ensureAuto()
	}
}

func (l *listenAlong) startHosting(auto bool) {
	l.Stop()
	room, err := listen.NewRoom()
	if err != nil {
		l.setStatus("Couldn't start a session: " + err.Error())
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	l.mu.Lock()
	l.mode, l.room, l.host, l.auto, l.cancel = "host", room, listen.Host{}, auto, cancel
	l.status = l.hostingStatus()
	l.pending, l.lastPublish, l.pausedUntil, l.listenersUntil = nil, time.Time{}, time.Time{}, time.Time{}
	l.mu.Unlock()
	go l.watchCheckIns(ctx, room)
	l.d.presence.SetJoinURL(room.JoinURL())
	log.Printf("listen along: hosting (for the Discord button: %v)", auto)
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
	now := time.Now()
	st, ok := l.host.Observe(stateOf(pb), now)
	ok = ok && l.audience(now)
	if ok {
		l.pending = &st
	}
	l.mu.Unlock()
	if ok {
		l.flush()
	}
}

// watchCheckIns listens on the host's own topic for guests checking in.
func (l *listenAlong) watchCheckIns(ctx context.Context, room listen.Room) {
	err := l.relay.Subscribe(ctx, room.Topic(), nil, func(m listen.Message) {
		// A cached check-in counts if it is recent: someone is still there.
		if room.OpenHello(m.Body) && (m.Live || time.Since(m.Time) < listenerWindow) {
			l.onCheckIn(room)
		}
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("listen along: watching for listeners: %v", err)
	}
}

// onCheckIn starts (or extends) publishing and sends the current state at
// once, so a guest who just joined starts in the right place.
func (l *listenAlong) onCheckIn(room listen.Room) {
	now := time.Now()
	l.mu.Lock()
	if l.mode != "host" || l.room != room {
		l.mu.Unlock()
		return
	}
	first := !now.Before(l.listenersUntil)
	l.listenersUntil = now.Add(listenerWindow)
	cur := l.host.Current(now)
	if cur.VideoID != "" {
		l.pending = &cur
	}
	l.mu.Unlock()
	if first {
		log.Print("listen along: someone is listening")
	}
	l.flush()
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

	err := l.publish(room, st, publishTimeout)

	l.mu.Lock()
	l.publishing = false
	if errors.Is(err, listen.ErrRateLimited) {
		l.pausedUntil = time.Now().Add(rateLimitPause)
		if l.pending == nil && l.mode == "host" && l.audience(time.Now()) {
			cur := l.host.Current(time.Now())
			l.pending = &cur
		}
		l.status = "The listen-along relay is limiting messages for now. Listeners will catch up in about a minute."
	} else if err != nil {
		l.status = "Couldn't reach the listen-along relay: " + err.Error()
	} else if l.mode == "host" {
		l.status = l.hostingStatus()
	}
	more := l.pending != nil
	l.mu.Unlock()
	l.d.pushAllState()
	if more {
		l.flush()
	}
}

func (l *listenAlong) publish(room listen.Room, st listen.State, timeout time.Duration) error {
	msg, err := room.Seal(st)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return l.relay.Publish(ctx, room.Topic(), msg)
}

// checkIn tells the host someone is listening.
func (l *listenAlong) checkIn(ctx context.Context, room listen.Room) {
	msg, err := room.SealHello()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	if err := l.relay.Publish(ctx, room.Topic(), msg); err != nil && ctx.Err() == nil {
		log.Printf("listen along: checking in: %v", err)
	}
}

// Join follows a host from a code or link.
func (l *listenAlong) Join(code string) {
	room, err := listen.ParseCode(code)
	if err != nil {
		l.setStatus(sentence(err))
		return
	}
	l.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	l.mu.Lock()
	l.mode, l.room, l.guest, l.cancel = "guest", room, listen.Guest{}, cancel
	l.status, l.following = "Connecting to the session…", ""
	l.mu.Unlock()
	l.d.pushAllState()
	log.Printf("listen along: joining")
	// Check in once connected (so the host's answer is received), on every
	// reconnect, and every checkInEvery while listening.
	connected := make(chan struct{}, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-connected:
			case <-time.After(checkInEvery):
			}
			l.checkIn(ctx, room)
		}
	}()
	go func() {
		err := l.relay.Subscribe(ctx, room.Topic(), func(serverTime time.Time) {
			l.mu.Lock()
			l.serverOffset = time.Since(serverTime)
			if l.status == "Connecting to the session…" {
				l.status = "Connected. Waiting for the host to play something…"
			}
			l.mu.Unlock()
			l.d.pushAllState()
			select {
			case connected <- struct{}{}:
			default:
			}
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
	if !m.Live && time.Now().Add(-offset).Sub(m.Time) > staleCached {
		return // from before joining; the host answers the check-in
	}
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
func (l *listenAlong) Stop() { l.stop(publishTimeout) }

// Shutdown is Stop for quitting: the end-of-session message gets a short
// timeout, so being offline never keeps the app running after its window
// closed.
func (l *listenAlong) Shutdown() { l.stop(shutdownTimeout) }

func (l *listenAlong) stop(timeout time.Duration) {
	l.mu.Lock()
	mode, room := l.mode, l.room
	if l.cancel != nil {
		l.cancel()
		l.cancel = nil
	}
	var final listen.State
	// Tell listeners the session ended, unless nobody could be listening.
	announce := mode == "host" && l.audience(time.Now())
	if announce {
		final = l.host.Current(time.Now())
		final.Ended = true
	}
	l.mode, l.status, l.following, l.pending, l.auto = "", "", "", nil, false
	l.mu.Unlock()
	if mode == "host" {
		l.d.presence.SetJoinURL("")
	}
	if announce {
		if err := l.publish(room, final, timeout); err != nil {
			log.Printf("listen along: announcing the end: %v", err)
		}
	}
	if mode != "" {
		log.Printf("listen along: stopped (%s)", mode)
		l.d.pushAllState()
	}
}

// leaveAsGuest stops following without touching a hosted session; the
// Discord button's session comes back.
func (l *listenAlong) leaveAsGuest() {
	l.mu.Lock()
	guest := l.mode == "guest"
	l.mu.Unlock()
	if guest {
		l.Stop()
		l.ensureAuto()
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
