package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"youtube-music/internal/secret"
	"youtube-music/internal/settings"
	"youtube-music/internal/spotify"
	"youtube-music/internal/ytmusic"
)

const matchWorkers = 4

// spotifyQueue plays a Spotify playlist on YouTube Music: each track is
// matched with a YouTube Music song, and the matches are played as
// temporary playlists of up to 50 songs, one after another.
type spotifyQueue struct {
	d *desktop

	mu       sync.Mutex
	cancel   context.CancelFunc
	busy     bool
	status   string
	name     string
	missing  []string // tracks with no match
	chunks   [][]string
	chunk    int
	lastSeen string // previous video ID reported while the queue ran
	yt       *ytmusic.Client
}

func (q *spotifyQueue) state() map[string]any {
	cfg := q.d.config().Spotify
	q.mu.Lock()
	defer q.mu.Unlock()
	status := q.status
	return map[string]any{
		"clientId":  cfg.ClientID,
		"hasSecret": cfg.ClientSecret != "",
		"busy":      q.busy,
		"active":    len(q.chunks) > 0,
		"status":    status,
		"missing":   q.missing,
	}
}

func (q *spotifyQueue) setStatus(format string, args ...any) {
	q.mu.Lock()
	q.status = fmt.Sprintf(format, args...)
	q.mu.Unlock()
	q.d.pushSettingsState()
}

func (q *spotifyQueue) saveCredentials(clientID, clientSecret string) {
	clientID, clientSecret = strings.TrimSpace(clientID), strings.TrimSpace(clientSecret)
	protected := ""
	if clientSecret != "" {
		var err error
		if protected, err = secret.Protect(clientSecret); err != nil {
			q.setStatus("Couldn't store the Client secret securely: %v", err)
			return
		}
	}
	q.d.update(func(c *settings.Settings) {
		if clientID != c.Spotify.ClientID {
			c.Spotify = settings.Spotify{ClientID: clientID}
		}
		if protected != "" {
			c.Spotify.ClientSecret = protected
		}
	})
	q.setStatus("Saved.")
}

// play reads the playlist, matches it and starts playing.
func (q *spotifyQueue) play(link string) {
	id, err := spotify.ParsePlaylistID(link)
	if err != nil {
		q.setStatus("%s", sentence(err))
		return
	}
	q.stop("")
	ctx, cancel := context.WithCancel(context.Background())
	q.d.mu.Lock()
	version := q.d.ytVersion
	q.d.mu.Unlock()
	q.mu.Lock()
	q.cancel, q.busy, q.missing = cancel, true, nil
	q.yt = &ytmusic.Client{ClientVersion: version}
	q.mu.Unlock()
	defer func() {
		q.mu.Lock()
		q.busy = false
		q.mu.Unlock()
		q.d.pushSettingsState()
	}()
	// Listening along and a Spotify queue would fight over the player.
	q.d.listen.leaveAsGuest()
	q.d.follow.stopIfActive("Stopped to play a Spotify playlist.")

	q.setStatus("Reading the playlist from Spotify…")
	pl, note, err := q.readPlaylist(ctx, id)
	if err != nil {
		log.Printf("spotify: %v", err)
		q.setStatus("%s", sentence(err))
		return
	}
	if len(pl.Tracks) == 0 {
		q.setStatus("“%s” has no songs that can be played.", pl.Name)
		return
	}

	ids, missing := q.match(ctx, pl)
	if ctx.Err() != nil {
		return
	}
	if len(ids) == 0 {
		q.setStatus("None of the %d songs in “%s” were found on YouTube Music.", len(pl.Tracks), pl.Name)
		return
	}
	var chunks [][]string
	for start := 0; start < len(ids); start += ytmusic.MaxPlaylistSize {
		chunks = append(chunks, ids[start:min(start+ytmusic.MaxPlaylistSize, len(ids))])
	}
	q.mu.Lock()
	q.name, q.missing, q.chunks, q.chunk, q.lastSeen = pl.Name, missing, chunks, 0, ""
	q.mu.Unlock()
	if err := q.playChunk(ctx, 0); err != nil {
		q.setStatus("YouTube couldn't create the queue: %s", sentence(err))
		q.stop("")
		return
	}
	found := fmt.Sprintf("%d of %d songs found", len(ids), len(pl.Tracks))
	if len(missing) == 0 {
		found = fmt.Sprintf("all %d songs found", len(ids))
	}
	q.setStatus("Playing “%s” (%s).%s", pl.Name, found, note)
	log.Printf("spotify: playing %q, %d/%d matched", pl.Name, len(ids), len(pl.Tracks))
}

// readPlaylist uses the official Web API when a Spotify app is set up and
// Spotify serves it, and otherwise Spotify's public playlist page (no
// account needed; Spotify may cut it off at 100 songs). note explains a
// fallback to the user.
func (q *spotifyQueue) readPlaylist(ctx context.Context, id string) (spotify.Playlist, string, error) {
	cfg := q.d.config().Spotify
	clientSecret, err := secret.Unprotect(cfg.ClientSecret)
	if err == nil && cfg.ClientID != "" && clientSecret != "" {
		client := &spotify.Client{ClientID: cfg.ClientID, ClientSecret: clientSecret}
		pl, apiErr := client.Playlist(ctx, id)
		switch {
		case apiErr == nil:
			return pl, "", nil
		case errors.Is(apiErr, spotify.ErrNotFound) || ctx.Err() != nil:
			return spotify.Playlist{}, "", apiErr
		}
		log.Printf("spotify: web api: %v; using the public page", apiErr)
		pl, pubErr := spotify.PublicPlaylist(ctx, nil, "", id)
		if pubErr != nil {
			return spotify.Playlist{}, "", apiErr
		}
		// Explain the fallback once; after that it just works quietly.
		note := ""
		if !cfg.FallbackNoted {
			why := "Spotify refused your Spotify app"
			if errors.Is(apiErr, spotify.ErrPremiumRequired) {
				why = "Your Spotify app needs Premium on its owner's account"
			}
			note = " " + why + ", so the songs came from Spotify's public playlist page. (This note is shown once.)"
			q.d.update(func(c *settings.Settings) { c.Spotify.FallbackNoted = true })
		}
		return pl, note + publicLimitNote(pl), nil
	}
	pl, err := spotify.PublicPlaylist(ctx, nil, "", id)
	if err != nil {
		return spotify.Playlist{}, "", err
	}
	return pl, publicLimitNote(pl), nil
}

// publicLimitNote warns when the public page may have cut a playlist short.
func publicLimitNote(pl spotify.Playlist) string {
	if len(pl.Tracks)+pl.Skipped >= spotify.PublicPageLimit {
		return fmt.Sprintf(" Spotify's public page lists at most %d songs, so a longer playlist is cut short.", spotify.PublicPageLimit)
	}
	return ""
}

// match finds a YouTube Music song for each track, in playlist order.
func (q *spotifyQueue) match(ctx context.Context, pl spotify.Playlist) (ids []string, missing []string) {
	q.mu.Lock()
	yt := q.yt
	q.mu.Unlock()
	results := make([]string, len(pl.Tracks))
	jobs := make(chan int)
	var done sync.WaitGroup
	var progress sync.Mutex
	finished := 0
	for w := 0; w < matchWorkers; w++ {
		done.Add(1)
		go func() {
			defer done.Done()
			for i := range jobs {
				t := pl.Tracks[i]
				track := ytmusic.Track{Title: t.Title, Artists: t.Artists, Duration: t.Duration}
				if songs, err := yt.SearchSongs(ctx, track.Query()); err == nil {
					if song, ok := ytmusic.BestMatch(track, songs); ok {
						results[i] = song.VideoID
					}
				}
				progress.Lock()
				finished++
				n := finished
				progress.Unlock()
				if n%5 == 0 || n == len(pl.Tracks) {
					q.setStatus("Finding “%s” on YouTube Music: %d of %d…", pl.Name, n, len(pl.Tracks))
				}
			}
		}()
	}
	for i := range pl.Tracks {
		select {
		case jobs <- i:
		case <-ctx.Done():
		}
	}
	close(jobs)
	done.Wait()
	seen := map[string]bool{}
	for i, id := range results {
		t := pl.Tracks[i]
		switch {
		case id == "":
			missing = append(missing, t.Title+" — "+strings.Join(t.Artists, ", "))
		case !seen[id]:
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, missing
}

func (q *spotifyQueue) playChunk(ctx context.Context, n int) error {
	q.mu.Lock()
	if n >= len(q.chunks) {
		q.mu.Unlock()
		return nil
	}
	videos, yt := q.chunks[n], q.yt
	q.mu.Unlock()
	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	playlistID, err := yt.TempPlaylist(reqCtx, videos)
	if err != nil {
		return err
	}
	q.mu.Lock()
	q.chunk, q.lastSeen = n, ""
	q.mu.Unlock()
	q.d.command("watch", map[string]any{"videoId": videos[0], "playlistId": playlistID})
	return nil
}

type queueAction int

const (
	queueKeep      queueAction = iota // still inside the current chunk
	queueNextChunk                    // the chunk finished; start the next
	queueFinished                     // the last chunk finished
	queueAbandoned                    // the user played something else
)

// nextQueueAction decides what a newly playing video means for the queue.
// prev is the previously reported video ("" right after a chunk started,
// while the old song may still be reported).
func nextQueueAction(chunk []string, prev, now string, hasNext bool) queueAction {
	if slices.Contains(chunk, now) || prev == "" || prev == now {
		return queueKeep
	}
	if prev == chunk[len(chunk)-1] {
		if hasNext {
			return queueNextChunk
		}
		return queueFinished
	}
	return queueAbandoned
}

// observe follows playback: when the last song of a chunk ends and YouTube
// moves on, the next chunk starts. Playing anything else stops the queue.
func (q *spotifyQueue) observe(pb Playback) {
	q.mu.Lock()
	if len(q.chunks) == 0 || q.busy || pb.VideoID == "" {
		q.mu.Unlock()
		return
	}
	prev := q.lastSeen
	q.lastSeen = pb.VideoID
	next := q.chunk + 1
	action := nextQueueAction(q.chunks[q.chunk], prev, pb.VideoID, next < len(q.chunks))
	q.mu.Unlock()

	switch action {
	case queueNextChunk:
		go func() {
			if err := q.playChunk(context.Background(), next); err != nil {
				q.setStatus("Couldn't continue the queue: %s", sentence(err))
			}
		}()
	case queueFinished:
		q.stop("Finished the playlist.")
	case queueAbandoned:
		q.stop("Stopped because you played something else.")
	}
}

// stopIfActive ends a running queue and explains why; it leaves the status
// alone when nothing was playing from Spotify.
func (q *spotifyQueue) stopIfActive(status string) {
	q.mu.Lock()
	active := len(q.chunks) > 0 || q.busy
	q.mu.Unlock()
	if active {
		q.stop(status)
	}
}

// stop ends the queue; status replaces the message when not empty.
func (q *spotifyQueue) stop(status string) {
	q.mu.Lock()
	if q.cancel != nil {
		q.cancel()
		q.cancel = nil
	}
	q.chunks, q.chunk, q.lastSeen = nil, 0, ""
	if status != "" {
		q.status = status
	}
	q.mu.Unlock()
	q.d.pushSettingsState()
}
