package main

import (
	"math"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Microsoft/go-winio"

	"youtube-music/internal/discord"
)

// Playback is the latest track state reported by the player page.
type Playback struct {
	VideoID         string  `json:"videoId"`
	Playing         bool    `json:"playing"`
	Title           string  `json:"title"`
	Artist          string  `json:"artist"`
	Album           string  `json:"album"`
	Artwork         string  `json:"artwork"`
	PositionSeconds float64 `json:"positionSeconds"`
	DurationSeconds float64 `json:"durationSeconds"`
	startedAtMs     int64
}

// presence keeps Discord Rich Presence in step with playback. All Discord
// I/O happens on one worker goroutine; callers only update state and kick it.
type presence struct {
	dial     discord.Dialer
	onStatus func(string)

	mu       sync.Mutex
	enabled  bool
	appID    string
	joinURL  string
	playback Playback
	status   string

	kick        chan struct{}
	retryDue    chan struct{}
	throttleDue chan struct{}

	// Owned by the worker goroutine.
	client       *discord.Client
	lastKey      string
	lastSent     time.Time
	throttled    bool
	retryAttempt int
	retryTimer   *time.Timer
}

// minActivityInterval spaces out SET_ACTIVITY calls. Seeking fires bursts of
// playback reports; Discord rate-limits rapid updates, so the latest state
// is sent once the interval has passed.
const minActivityInterval = 2 * time.Second

func newPresence(onStatus func(string)) *presence {
	p := &presence{
		dial: func(path string, timeout time.Duration) (net.Conn, error) {
			return winio.DialPipe(path, &timeout)
		},
		onStatus:    onStatus,
		status:      "Off",
		kick:        make(chan struct{}, 1),
		retryDue:    make(chan struct{}, 1),
		throttleDue: make(chan struct{}, 1),
	}
	go p.run()
	return p
}

func (p *presence) Status() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

func (p *presence) setStatus(s string) {
	p.mu.Lock()
	changed := p.status != s
	p.status = s
	p.mu.Unlock()
	if changed && p.onStatus != nil {
		p.onStatus(s)
	}
}

// Configure applies the Discord settings.
func (p *presence) Configure(enabled bool, appID string) {
	p.mu.Lock()
	p.enabled, p.appID = enabled, appID
	p.mu.Unlock()
	p.poke()
}

// SetJoinURL shows a "Listen along" button on the status while hosting.
func (p *presence) SetJoinURL(url string) {
	p.mu.Lock()
	p.joinURL = url
	p.mu.Unlock()
	p.poke()
}

// Update merges a playback report, mirroring the Electron release: the
// start time is derived from the reported position while playing.
func (p *presence) Update(next Playback) {
	p.mu.Lock()
	prev := p.playback
	next.Title = strings.TrimSpace(next.Title)
	next.Artist = strings.TrimSpace(next.Artist)
	next.Album = strings.TrimSpace(next.Album)
	next.Artwork = strings.TrimSpace(next.Artwork)
	next.PositionSeconds = finiteNonNegative(next.PositionSeconds)
	next.DurationSeconds = finiteNonNegative(next.DurationSeconds)
	if next.Playing {
		next.startedAtMs = time.Now().UnixMilli() - int64(next.PositionSeconds*1000)
	} else {
		next.startedAtMs = prev.startedAtMs
	}
	p.playback = next
	p.mu.Unlock()
	p.poke()
}

func (p *presence) poke() {
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

func (p *presence) run() {
	for {
		select {
		case <-p.kick:
		case <-p.retryDue:
			p.retryTimer = nil
		case <-p.throttleDue:
			p.throttled = false
		}
		p.sync()
	}
}

// Close disables presence and waits briefly for the worker to clear the
// status and disconnect. Safe to call at shutdown.
func (p *presence) Close() {
	p.mu.Lock()
	p.enabled = false
	p.mu.Unlock()
	p.poke()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && p.Status() != "Off" {
		time.Sleep(20 * time.Millisecond)
	}
}

func (p *presence) disconnect() {
	p.lastKey = ""
	if p.client == nil {
		return
	}
	_ = p.client.ClearActivity()
	_ = p.client.Close()
	p.client = nil
}

func (p *presence) stopRetry() {
	if p.retryTimer != nil {
		p.retryTimer.Stop()
		p.retryTimer = nil
	}
	p.retryAttempt = 0
}

func (p *presence) sync() {
	p.mu.Lock()
	enabled, appID, pb, joinURL := p.enabled, p.appID, p.playback, p.joinURL
	p.mu.Unlock()

	if !enabled || appID == "" {
		p.stopRetry()
		p.disconnect()
		p.setStatus("Off")
		return
	}
	if !pb.Playing || pb.Title == "" {
		p.setStatus("Waiting for a song to play.")
		if p.client != nil && p.lastKey != "" {
			p.lastKey = ""
			_ = p.client.ClearActivity()
		}
		return
	}

	key := strings.Join([]string{pb.Title, pb.Artist, pb.Album, strconv.FormatInt(pb.startedAtMs/1000, 10), joinURL}, "|")
	if key == p.lastKey && p.client != nil && p.client.AppID() == appID {
		return
	}
	if wait := minActivityInterval - time.Since(p.lastSent); wait > 0 {
		if !p.throttled {
			p.throttled = true
			time.AfterFunc(wait, func() {
				select {
				case p.throttleDue <- struct{}{}:
				default:
				}
			})
		}
		return
	}
	if p.client != nil && p.client.AppID() != appID {
		p.disconnect()
	}
	if p.client == nil {
		client, err := discord.Connect(p.dial, appID)
		if err != nil {
			p.scheduleRetry()
			return
		}
		p.client = client
	}
	track := discord.Track{Title: pb.Title, Artist: pb.Artist, Album: pb.Album, Artwork: pb.Artwork, StartMs: pb.startedAtMs, JoinURL: joinURL}
	if pb.DurationSeconds > 0 && pb.startedAtMs > 0 {
		track.EndMs = pb.startedAtMs + int64(pb.DurationSeconds*1000)
	}
	p.lastSent = time.Now()
	if err := p.client.SetActivity(track); err != nil {
		_ = p.client.Close()
		p.client = nil
		p.lastKey = ""
		p.scheduleRetry()
		return
	}
	p.lastKey = key
	p.stopRetry()
	p.setStatus("Connected — showing the current song.")
}

func (p *presence) scheduleRetry() {
	if p.retryTimer != nil {
		return
	}
	delay := time.Duration(math.Min(30, 5*math.Pow(2, float64(p.retryAttempt)))) * time.Second
	p.retryAttempt = min(p.retryAttempt+1, 3)
	p.setStatus("Discord is busy. Retrying in " + strconv.Itoa(int(delay.Seconds())) + " seconds…")
	p.retryTimer = time.AfterFunc(delay, func() {
		select {
		case p.retryDue <- struct{}{}:
		default:
		}
	})
}

func finiteNonNegative(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	return v
}
