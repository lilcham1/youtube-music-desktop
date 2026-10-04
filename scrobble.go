package main

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"youtube-music/internal/lastfm"
	"youtube-music/internal/secret"
	"youtube-music/internal/settings"
)

const (
	scrobbleTick     = 10 * time.Second
	lastfmApproveFor = 3 * time.Minute
)

// scrobbler connects playback to Last.fm.
type scrobbler struct {
	d      *desktop
	queue  *lastfm.Queue
	sendMu sync.Mutex // one send at a time, so a batch is never sent twice

	mu         sync.Mutex
	tracker    lastfm.Tracker
	client     *lastfm.Client // nil unless connected and enabled
	status     string
	connecting bool
}

func newScrobbler(d *desktop) *scrobbler {
	s := &scrobbler{d: d, queue: lastfm.LoadQueue(filepath.Join(d.profile, "lastfm-queue.json"))}
	s.configure()
	return s
}

// configure builds the client from the saved settings.
func (s *scrobbler) configure() {
	cfg := s.d.config().LastFM
	s.mu.Lock()
	defer s.mu.Unlock()
	s.client = nil
	if cfg.APIKey == "" || cfg.Secret == "" || cfg.SessionKey == "" || !cfg.Enabled {
		return
	}
	sharedSecret, err1 := secret.Unprotect(cfg.Secret)
	sessionKey, err2 := secret.Unprotect(cfg.SessionKey)
	if err1 != nil || err2 != nil {
		s.status = "Your saved Last.fm sign-in can't be read on this PC. Connect again."
		return
	}
	s.client = &lastfm.Client{APIKey: cfg.APIKey, Secret: sharedSecret, SessionKey: sessionKey}
}

// state is what the settings page shows.
func (s *scrobbler) state() map[string]any {
	cfg := s.d.config().LastFM
	s.mu.Lock()
	status, connecting := s.status, s.connecting
	s.mu.Unlock()
	if status == "" {
		switch {
		case cfg.APIKey == "" || cfg.Secret == "":
			status = "Add your Last.fm API key and shared secret to get started."
		case cfg.SessionKey == "":
			status = "Ready to connect your Last.fm account."
		case !cfg.Enabled:
			status = fmt.Sprintf("Connected as %s. Scrobbling is paused.", cfg.Username)
		default:
			status = fmt.Sprintf("Scrobbling as %s.", cfg.Username)
		}
		if n := s.queue.Len(); n > 0 && cfg.SessionKey != "" {
			status += fmt.Sprintf(" %d scrobble(s) waiting to send.", n)
		}
	}
	return map[string]any{
		"apiKey":     cfg.APIKey,
		"hasSecret":  cfg.Secret != "",
		"connected":  cfg.SessionKey != "",
		"username":   cfg.Username,
		"enabled":    cfg.Enabled,
		"connecting": connecting,
		"status":     status,
	}
}

func (s *scrobbler) setStatus(status string) {
	s.mu.Lock()
	s.status = status
	s.mu.Unlock()
	s.d.pushSettingsState()
}

// saveCredentials stores the API key and (encrypted) shared secret. A new
// key invalidates the old session.
func (s *scrobbler) saveCredentials(apiKey, sharedSecret string) {
	apiKey = strings.TrimSpace(apiKey)
	sharedSecret = strings.TrimSpace(sharedSecret)
	protected := ""
	if sharedSecret != "" {
		var err error
		if protected, err = secret.Protect(sharedSecret); err != nil {
			s.setStatus("Couldn't store the shared secret securely: " + err.Error())
			return
		}
	}
	s.d.update(func(c *settings.Settings) {
		if apiKey != c.LastFM.APIKey {
			c.LastFM = settings.LastFM{APIKey: apiKey}
		}
		if protected != "" {
			c.LastFM.Secret = protected
		}
	})
	s.configure()
	s.setStatus("")
}

// connect runs the desktop sign-in: the user approves in the browser while
// this polls for the session.
func (s *scrobbler) connect() {
	cfg := s.d.config().LastFM
	sharedSecret, err := secret.Unprotect(cfg.Secret)
	if cfg.APIKey == "" || err != nil || sharedSecret == "" {
		s.setStatus("Save your Last.fm API key and shared secret first.")
		return
	}
	s.mu.Lock()
	if s.connecting {
		s.mu.Unlock()
		return
	}
	s.connecting = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.connecting = false
		s.mu.Unlock()
		s.d.pushSettingsState()
	}()

	client := &lastfm.Client{APIKey: cfg.APIKey, Secret: sharedSecret}
	ctx, cancel := context.WithTimeout(context.Background(), lastfmApproveFor+30*time.Second)
	defer cancel()
	token, approveURL, err := client.Token(ctx)
	if err != nil {
		s.setStatus("Last.fm didn't accept the API key or secret: " + err.Error())
		return
	}
	s.d.openExternal(approveURL)
	s.setStatus("Approve YouTube Music in the Last.fm page that just opened in your browser…")
	deadline := time.Now().Add(lastfmApproveFor)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			s.setStatus("Connecting timed out. Try again.")
			return
		case <-time.After(3 * time.Second):
		}
		key, user, err := client.Session(ctx, token)
		if lastfm.NotAuthorizedYet(err) {
			continue
		}
		if err != nil {
			s.setStatus("Last.fm sign-in failed: " + err.Error())
			return
		}
		protected, err := secret.Protect(key)
		if err != nil {
			s.setStatus("Couldn't store the Last.fm session securely: " + err.Error())
			return
		}
		s.d.update(func(c *settings.Settings) {
			c.LastFM.SessionKey, c.LastFM.Username, c.LastFM.Enabled = protected, user, true
		})
		s.configure()
		s.setStatus("")
		log.Printf("last.fm connected as %s", user)
		return
	}
	s.setStatus("You didn't approve in time. Press Connect to try again.")
}

func (s *scrobbler) disconnect() {
	s.d.update(func(c *settings.Settings) {
		c.LastFM.SessionKey, c.LastFM.Username, c.LastFM.Enabled = "", "", false
	})
	s.configure()
	s.setStatus("")
}

func (s *scrobbler) setEnabled(enabled bool) {
	s.d.update(func(c *settings.Settings) { c.LastFM.Enabled = enabled && c.LastFM.SessionKey != "" })
	s.configure()
	s.setStatus("")
}

func observation(pb Playback) lastfm.Observation {
	id := pb.VideoID
	if id == "" {
		id = pb.Title + "\x00" + pb.Artist
	}
	return lastfm.Observation{
		ID: id, Playing: pb.Playing, Position: time.Duration(pb.PositionSeconds * float64(time.Second)),
		Track: lastfm.Track{Artist: pb.Artist, Title: pb.Title, Album: pb.Album, Duration: time.Duration(pb.DurationSeconds * float64(time.Second))},
	}
}

func (s *scrobbler) observe(pb Playback) {
	s.mu.Lock()
	client := s.client
	var ev lastfm.Events
	if client != nil {
		ev = s.tracker.Observe(observation(pb), time.Now())
	}
	s.mu.Unlock()
	if client != nil {
		go s.handle(client, ev)
	}
}

// run ticks the tracker so long songs scrobble without further events, and
// retries queued scrobbles.
func (s *scrobbler) run() {
	for range time.Tick(scrobbleTick) {
		s.mu.Lock()
		client := s.client
		var ev lastfm.Events
		if client != nil {
			ev = s.tracker.Tick(time.Now())
		}
		s.mu.Unlock()
		if client != nil {
			s.handle(client, ev)
		}
	}
}

func (s *scrobbler) handle(client *lastfm.Client, ev lastfm.Events) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if ev.NowPlaying != nil {
		if err := client.NowPlaying(ctx, *ev.NowPlaying); err != nil {
			s.onError(err)
		}
	}
	if ev.Scrobble != nil {
		s.queue.Add(*ev.Scrobble)
	}
	if ev.Scrobble != nil || s.queue.Len() > 0 {
		s.send(ctx, client)
	}
}

// send delivers queued scrobbles, oldest first.
func (s *scrobbler) send(ctx context.Context, client *lastfm.Client) {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	for s.queue.Len() > 0 {
		batch := s.queue.Peek(lastfm.MaxBatch)
		if err := client.Scrobble(ctx, batch); err != nil {
			s.onError(err)
			return
		}
		s.queue.Drop(len(batch))
		log.Printf("last.fm: scrobbled %d", len(batch))
	}
	s.d.pushSettingsState()
}

func (s *scrobbler) onError(err error) {
	log.Printf("last.fm: %v", err)
	if lastfm.SessionInvalid(err) {
		s.d.update(func(c *settings.Settings) { c.LastFM.SessionKey, c.LastFM.Enabled = "", false })
		s.configure()
		s.setStatus("Last.fm signed this app out. Connect again to keep scrobbling (queued scrobbles are kept).")
	}
}

// flush tries once to send queued scrobbles at shutdown.
func (s *scrobbler) flush() {
	s.mu.Lock()
	client := s.client
	s.mu.Unlock()
	if client == nil || s.queue.Len() == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s.send(ctx, client)
}
