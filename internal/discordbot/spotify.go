// Package discordbot connects to Discord as the user's own bot to see what
// people in the bot's servers are playing on Spotify. Discord shows a
// Spotify listener's track and position in their status; a bot with the
// Presence intent receives those statuses live.
package discordbot

import (
	"strings"
	"time"
)

// Spotify is what someone is playing on Spotify, from their Discord status.
type Spotify struct {
	TrackID string // Spotify track ID
	Title   string
	Artists []string
	Album   string
	Start   time.Time // when the track would have started, given the position
	End     time.Time
}

// Position is how far into the track the listener is at now.
func (s Spotify) Position(now time.Time) time.Duration {
	if now.Before(s.Start) {
		return 0
	}
	return now.Sub(s.Start)
}

func (s Spotify) Duration() time.Duration { return s.End.Sub(s.Start) }

type activity struct {
	Type       int    `json:"type"`
	Name       string `json:"name"`
	Details    string `json:"details"`
	State      string `json:"state"`
	SyncID     string `json:"sync_id"`
	Timestamps struct {
		Start int64 `json:"start"`
		End   int64 `json:"end"`
	} `json:"timestamps"`
	Assets struct {
		LargeText string `json:"large_text"`
	} `json:"assets"`
}

// spotifyFrom finds a Spotify "Listening to" activity. Discord removes it
// when the listener pauses or stops, so nil means not playing.
func spotifyFrom(activities []activity) *Spotify {
	for _, a := range activities {
		if a.Type != 2 || a.Name != "Spotify" || a.SyncID == "" || a.Details == "" {
			continue
		}
		s := &Spotify{TrackID: a.SyncID, Title: a.Details, Album: a.Assets.LargeText}
		for _, artist := range strings.Split(a.State, ";") {
			if name := strings.TrimSpace(artist); name != "" {
				s.Artists = append(s.Artists, name)
			}
		}
		if a.Timestamps.Start > 0 {
			s.Start = time.UnixMilli(a.Timestamps.Start)
		}
		if a.Timestamps.End > 0 {
			s.End = time.UnixMilli(a.Timestamps.End)
		}
		return s
	}
	return nil
}
