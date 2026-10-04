package spotify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// PublicPageLimit is how many songs Spotify's public embed page may list;
// longer playlists are cut off there.
const PublicPageLimit = 100

var nextDataRe = regexp.MustCompile(`(?s)<script id="__NEXT_DATA__" type="application/json">(.*?)</script>`)

// PublicPlaylist reads a playlist from the page Spotify serves for embedding
// players on websites. It needs no developer app or account, but only works
// for public playlists and may list just the first PublicPageLimit songs.
func PublicPlaylist(ctx context.Context, client *http.Client, embedBase, id string) (Playlist, error) {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	if embedBase == "" {
		embedBase = "https://open.spotify.com/embed/playlist/"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, embedBase+id, nil)
	if err != nil {
		return Playlist{}, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36")
	resp, err := client.Do(req)
	if err != nil {
		return Playlist{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Playlist{}, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return Playlist{}, fmt.Errorf("spotify's playlist page: %s", resp.Status)
	}
	page, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return Playlist{}, err
	}
	return ParsePublicPage(page)
}

// ParsePublicPage extracts the playlist from an embed page.
func ParsePublicPage(page []byte) (Playlist, error) {
	m := nextDataRe.FindSubmatch(page)
	if m == nil {
		return Playlist{}, errors.New("spotify's playlist page has changed and can't be read")
	}
	var data struct {
		Props struct {
			PageProps struct {
				State struct {
					Data struct {
						Entity *struct {
							Type      string `json:"type"`
							Name      string `json:"name"`
							TrackList []struct {
								URI        string `json:"uri"`
								Title      string `json:"title"`
								Subtitle   string `json:"subtitle"`
								DurationMS int    `json:"duration"`
								EntityType string `json:"entityType"`
							} `json:"trackList"`
						} `json:"entity"`
					} `json:"data"`
				} `json:"state"`
			} `json:"pageProps"`
		} `json:"props"`
	}
	if err := json.Unmarshal(m[1], &data); err != nil {
		return Playlist{}, fmt.Errorf("spotify's playlist page has changed and can't be read: %w", err)
	}
	entity := data.Props.PageProps.State.Data.Entity
	if entity == nil || entity.Type != "playlist" {
		return Playlist{}, ErrNotFound
	}
	pl := Playlist{Name: entity.Name, FromPublicPage: true}
	for _, t := range entity.TrackList {
		if !strings.HasPrefix(t.URI, "spotify:track:") || t.Title == "" {
			pl.Skipped++
			continue
		}
		track := Track{Title: t.Title, Duration: time.Duration(t.DurationMS) * time.Millisecond}
		for _, artist := range strings.Split(t.Subtitle, ",") {
			if a := strings.TrimSpace(artist); a != "" {
				track.Artists = append(track.Artists, a)
			}
		}
		pl.Tracks = append(pl.Tracks, track)
	}
	return pl, nil
}
