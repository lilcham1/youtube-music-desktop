// Package spotify reads public Spotify playlists with the Web API, using
// the user's own developer app (client-credentials flow: no Spotify sign-in,
// only playlists anyone can see).
package spotify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Track struct {
	Title    string
	Artists  []string
	Duration time.Duration
}

type Playlist struct {
	Name   string
	Tracks []Track
	// Skipped counts local files, podcast episodes and removed tracks.
	Skipped int
	// FromPublicPage is set when the list came from Spotify's public embed
	// page rather than the Web API (it may stop at PublicPageLimit songs).
	FromPublicPage bool
}

var (
	ErrCredentials  = errors.New("Spotify rejected the Client ID or Client secret. Check them in Settings")
	ErrNotFound     = errors.New("Spotify couldn't find that playlist. It may be private or deleted")
	ErrForbidden    = errors.New("Spotify doesn't let apps read this playlist. Spotify's own playlists (like Today's Top Hits) are blocked; playlists made by users work")
	ErrNoCredential = errors.New("add your Spotify Client ID and Client secret in Settings first")
	// ErrPremiumRequired: Spotify only serves the Web API to developer apps
	// whose owner has an active Spotify Premium subscription.
	ErrPremiumRequired = errors.New("Spotify only lets developer apps read playlists when the account that created the app has Spotify Premium")
	ErrBadLink         = errors.New("that doesn't look like a Spotify playlist link")
)

var idRe = regexp.MustCompile(`^[0-9A-Za-z]{22}$`)

// ParsePlaylistID accepts an open.spotify.com link (any locale path, with or
// without ?si=), a spotify:playlist: URI, or a bare playlist ID.
func ParsePlaylistID(input string) (string, error) {
	s := strings.TrimSpace(input)
	if idRe.MatchString(s) {
		return s, nil
	}
	if rest, ok := strings.CutPrefix(s, "spotify:playlist:"); ok && idRe.MatchString(rest) {
		return rest, nil
	}
	u, err := url.Parse(s)
	if err != nil || (u.Host != "open.spotify.com" && u.Host != "play.spotify.com") {
		return "", ErrBadLink
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "playlist" && idRe.MatchString(parts[i+1]) {
			return parts[i+1], nil
		}
	}
	return "", ErrBadLink
}

type Client struct {
	ClientID, ClientSecret string
	HTTP                   *http.Client
	TokenURL, APIBase      string // overridable in tests

	mu      sync.Mutex
	token   string
	expires time.Time
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func (c *Client) accessToken(ctx context.Context) (string, error) {
	if c.ClientID == "" || c.ClientSecret == "" {
		return "", ErrNoCredential
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.expires) {
		return c.token, nil
	}
	tokenURL := c.TokenURL
	if tokenURL == "" {
		tokenURL = "https://accounts.spotify.com/api/token"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader("grant_type=client_credentials"))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(c.ClientID, c.ClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized {
		return "", ErrCredentials
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("spotify token: %s", resp.Status)
	}
	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	c.token = body.AccessToken
	c.expires = time.Now().Add(time.Duration(body.ExpiresIn)*time.Second - time.Minute)
	return c.token, nil
}

func (c *Client) get(ctx context.Context, rawURL string, out any) error {
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out)
	case http.StatusUnauthorized:
		c.mu.Lock()
		c.token = ""
		c.mu.Unlock()
		return ErrCredentials
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusForbidden:
		// Spotify explains a 403 in the body; tell the Premium rule apart
		// from playlists it blocks for apps.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		if strings.Contains(strings.ToLower(string(body)), "premium") {
			return ErrPremiumRequired
		}
		return ErrForbidden
	default:
		return fmt.Errorf("spotify: %s", resp.Status)
	}
}

type trackObject struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	IsLocal    bool   `json:"is_local"`
	DurationMS int    `json:"duration_ms"`
	Artists    []struct {
		Name string `json:"name"`
	} `json:"artists"`
}

type page struct {
	Items []struct {
		Track *trackObject `json:"track"`
		Item  *trackObject `json:"item"` // newer response shape
	} `json:"items"`
	Next string `json:"next"`
}

// maxTracks bounds very large playlists (about 2,000 songs).
const maxTracks = 2000

// Playlist reads a playlist's name and tracks.
func (c *Client) Playlist(ctx context.Context, id string) (Playlist, error) {
	base := c.APIBase
	if base == "" {
		base = "https://api.spotify.com/v1"
	}
	var meta struct {
		Name string `json:"name"`
	}
	if err := c.get(ctx, base+"/playlists/"+id+"?fields=name", &meta); err != nil {
		return Playlist{}, err
	}
	pl := Playlist{Name: meta.Name}
	next := base + "/playlists/" + id + "/tracks?limit=100&additional_types=track"
	for next != "" && len(pl.Tracks) < maxTracks {
		var p page
		if err := c.get(ctx, next, &p); err != nil {
			return Playlist{}, err
		}
		for _, it := range p.Items {
			t := it.Track
			if t == nil {
				t = it.Item
			}
			if t == nil || t.IsLocal || (t.Type != "" && t.Type != "track") || t.Name == "" {
				pl.Skipped++
				continue
			}
			track := Track{Title: t.Name, Duration: time.Duration(t.DurationMS) * time.Millisecond}
			for _, a := range t.Artists {
				track.Artists = append(track.Artists, a.Name)
			}
			pl.Tracks = append(pl.Tracks, track)
		}
		next = p.Next
	}
	return pl, nil
}
