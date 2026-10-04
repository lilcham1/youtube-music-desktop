package lastfm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Playing is what a Last.fm user is listening to right now.
type Playing struct {
	Artist, Title, Album string
}

// Key identifies the track, to notice changes.
func (p Playing) Key() string { return strings.ToLower(p.Artist + "\x00" + p.Title) }

// ErrUnknownUser means there is no Last.fm user with that name.
var ErrUnknownUser = errors.New("there's no Last.fm user with that name")

// Listening returns what user is playing now, or nil if nothing. It needs
// only the API key (no sign-in) and works for public profiles.
func (c *Client) Listening(ctx context.Context, user string) (*Playing, error) {
	if c.APIKey == "" {
		return nil, errors.New("add your Last.fm API key in Settings → Last.fm first")
	}
	endpoint := c.APIURL
	if endpoint == "" {
		endpoint = apiURL
	}
	q := url.Values{"method": {"user.getrecenttracks"}, "user": {user}, "limit": {"1"}, "api_key": {c.APIKey}, "format": {"json"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "youtube-music-desktop")
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	return parseRecent(data)
}

type recentTrack struct {
	Name   string `json:"name"`
	Artist struct {
		Text string `json:"#text"`
		Name string `json:"name"`
	} `json:"artist"`
	Album struct {
		Text string `json:"#text"`
	} `json:"album"`
	Attr struct {
		NowPlaying string `json:"nowplaying"`
	} `json:"@attr"`
}

func parseRecent(data []byte) (*Playing, error) {
	var body struct {
		Error       int    `json:"error"`
		Message     string `json:"message"`
		RecentTrack struct {
			// Last.fm sends an object instead of an array for one track.
			Track json.RawMessage `json:"track"`
		} `json:"recenttracks"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, err
	}
	if body.Error == 6 {
		return nil, ErrUnknownUser
	}
	if body.Error != 0 {
		return nil, &Error{Code: body.Error, Message: body.Message}
	}
	var tracks []recentTrack
	if err := json.Unmarshal(body.RecentTrack.Track, &tracks); err != nil {
		var one recentTrack
		if json.Unmarshal(body.RecentTrack.Track, &one) == nil {
			tracks = []recentTrack{one}
		}
	}
	for _, t := range tracks {
		if t.Attr.NowPlaying != "true" {
			continue
		}
		artist := t.Artist.Text
		if artist == "" {
			artist = t.Artist.Name
		}
		if t.Name == "" || artist == "" {
			continue
		}
		return &Playing{Artist: artist, Title: t.Name, Album: t.Album.Text}, nil
	}
	return nil, nil
}
