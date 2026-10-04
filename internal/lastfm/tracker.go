package lastfm

import "time"

// Last.fm's scrobbling rules: a track must be longer than 30 seconds and is
// scrobbled once it has been played for half its length or four minutes,
// whichever comes first.
const (
	minTrackLength = 30 * time.Second
	maxThreshold   = 4 * time.Minute
	// maxTickGap caps the time counted between two observations, so a
	// sleeping PC or a stalled report never inflates the played time.
	maxTickGap = 30 * time.Second
)

// Observation is the player state at one moment.
type Observation struct {
	ID       string // identifies the song (video ID)
	Playing  bool
	Position time.Duration
	Track    Track // Started is ignored; the tracker sets it
}

// Tracker turns a stream of observations into now-playing updates and
// scrobbles. It counts only time actually spent playing.
type Tracker struct {
	id          string
	track       Track
	played      time.Duration
	lastSeen    time.Time
	lastPos     time.Duration
	playing     bool
	scrobbled   bool
	announced   bool
	initialised bool
}

// Events are what the caller should send to Last.fm.
type Events struct {
	NowPlaying *Track
	Scrobble   *Track
}

func threshold(length time.Duration) time.Duration {
	if half := length / 2; half < maxThreshold {
		return half
	}
	return maxThreshold
}

// Observe records the state at now.
func (t *Tracker) Observe(o Observation, now time.Time) Events {
	var ev Events
	t.advance(now)
	newPlay := !t.initialised || o.ID != t.id
	// The same song starting over (repeat) after it was scrobbled is a new play.
	if !newPlay && t.scrobbled && o.Position+30*time.Second < t.lastPos {
		newPlay = true
	}
	if newPlay {
		t.id, t.track, t.played = o.ID, o.Track, 0
		t.track.Started = now.Add(-o.Position).Truncate(time.Second)
		t.scrobbled, t.announced, t.initialised = false, false, true
	} else if o.Track.Duration > 0 {
		t.track.Duration = o.Track.Duration
	}
	t.playing, t.lastPos, t.lastSeen = o.Playing, o.Position, now
	if t.playing && !t.announced && t.eligible() {
		t.announced = true
		np := t.track
		ev.NowPlaying = &np
	}
	ev.Scrobble = t.maybeScrobble()
	return ev
}

// Tick advances time while nothing else is reported, so a long song that
// plays without any event is still scrobbled.
func (t *Tracker) Tick(now time.Time) Events {
	t.advance(now)
	return Events{Scrobble: t.maybeScrobble()}
}

func (t *Tracker) advance(now time.Time) {
	if t.playing && !t.lastSeen.IsZero() {
		gap := now.Sub(t.lastSeen)
		if gap > maxTickGap {
			gap = maxTickGap
		}
		if gap > 0 {
			t.played += gap
		}
	}
	t.lastSeen = now
}

func (t *Tracker) eligible() bool {
	return t.track.Title != "" && t.track.Artist != "" && t.track.Duration > minTrackLength
}

func (t *Tracker) maybeScrobble() *Track {
	if t.scrobbled || !t.eligible() || t.played < threshold(t.track.Duration) {
		return nil
	}
	t.scrobbled = true
	s := t.track
	return &s
}
