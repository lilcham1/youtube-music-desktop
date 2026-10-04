package listen

import (
	"math"
	"time"
)

// Host decides when the host's playback is worth publishing. Only real
// events go out (a new song, play/pause, a seek), which keeps the relay's
// daily message limit in reach of a long listening session.
type Host struct {
	seq       int64
	published bool
	last      State
	lastAt    time.Time
}

// seekTolerance is how far the position may drift from the expected one
// before it counts as a seek.
const seekTolerance = 3 * time.Second

// Observe returns the state to publish, if this observation changes what
// listeners should hear.
func (h *Host) Observe(s State, now time.Time) (State, bool) {
	if s.VideoID == "" {
		return State{}, false
	}
	publish := !h.published || s.VideoID != h.last.VideoID || s.Playing != h.last.Playing
	if !publish {
		expected := h.last.Position
		if h.last.Playing {
			expected += now.Sub(h.lastAt).Seconds()
		}
		publish = math.Abs(s.Position-expected) > seekTolerance.Seconds()
	}
	// Keep tracking the position so later seeks are measured correctly.
	h.last, h.lastAt = s, now
	if !publish {
		return State{}, false
	}
	h.seq++
	h.published = true
	s.Seq, s.SentAtMS = h.seq, now.UnixMilli()
	h.last = s
	return s, true
}

// Current returns the latest state, re-stamped for publishing (used when a
// publish has to be retried or the session ends).
func (h *Host) Current(now time.Time) State {
	s := h.last
	if s.Playing {
		s.Position += now.Sub(h.lastAt).Seconds()
	}
	h.seq++
	s.Seq, s.SentAtMS = h.seq, now.UnixMilli()
	return s
}

// Guest filters and positions incoming states.
type Guest struct {
	lastSeq int64
}

// Accept reports whether s is newer than anything applied so far.
func (g *Guest) Accept(s State) bool {
	if s.Seq <= g.lastSeq {
		return false
	}
	g.lastSeq = s.Seq
	return true
}

// Target returns where the guest should be: the host's position plus the
// time since the relay received the message, if the host was playing.
// relayTime is the relay's receive time and serverOffset the guest's clock
// minus the relay's clock; live messages pass relayTime = zero.
func Target(s State, relayTime time.Time, serverOffset time.Duration, now time.Time) float64 {
	pos := s.Position
	if !s.Playing {
		return pos
	}
	elapsed := 0.3 // typical relay delay for a live message
	if !relayTime.IsZero() {
		elapsed = now.Add(-serverOffset).Sub(relayTime).Seconds()
	}
	return pos + math.Max(0, elapsed)
}
