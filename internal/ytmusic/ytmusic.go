// Package ytmusic finds songs on YouTube Music and builds temporary
// playlists from them, for playing Spotify playlists as a queue.
//
// Search uses YouTube Music's public web API (the same request the site
// makes, without signing in). Temporary playlists come from YouTube's
// watch_videos link, which turns up to 50 video IDs into a "TLGG…" playlist
// that YouTube Music plays as a normal queue.
package ytmusic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	searchURL = "https://music.youtube.com/youtubei/v1/search?prettyPrint=false"
	// songsFilter restricts search results to songs.
	songsFilter = "EgWKAQIIAWoMEA4QChADEAQQCRAF"
	// FallbackClientVersion is used until the page reports its own version.
	FallbackClientVersion = "1.20260928.13.00"
	userAgent             = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36"
	// MaxPlaylistSize is the most video IDs one watch_videos link accepts.
	MaxPlaylistSize = 50
)

type Song struct {
	VideoID  string
	Title    string
	Artists  []string
	Album    string
	Duration time.Duration
}

type Client struct {
	HTTP          *http.Client
	ClientVersion string
	SearchURL     string // overridable in tests
	WatchVideoURL string // overridable in tests
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// SearchSongs returns YouTube Music's song results for query.
func (c *Client) SearchSongs(ctx context.Context, query string) ([]Song, error) {
	version := c.ClientVersion
	if version == "" {
		version = FallbackClientVersion
	}
	body, _ := json.Marshal(map[string]any{
		"context": map[string]any{"client": map[string]any{"clientName": "WEB_REMIX", "clientVersion": version, "hl": "en", "gl": "US"}},
		"query":   query,
		"params":  songsFilter,
	})
	endpoint := c.SearchURL
	if endpoint == "" {
		endpoint = searchURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://music.youtube.com")
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("youtube music search: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	return ParseSearch(data)
}

type run struct {
	Text               string `json:"text"`
	NavigationEndpoint struct {
		BrowseEndpoint struct {
			Configs struct {
				Music struct {
					PageType string `json:"pageType"`
				} `json:"browseEndpointContextMusicConfig"`
			} `json:"browseEndpointContextSupportedConfigs"`
		} `json:"browseEndpoint"`
	} `json:"navigationEndpoint"`
}

type listItem struct {
	FlexColumns []struct {
		Column struct {
			Text struct {
				Runs []run `json:"runs"`
			} `json:"text"`
		} `json:"musicResponsiveListItemFlexColumnRenderer"`
	} `json:"flexColumns"`
	PlaylistItemData struct {
		VideoID string `json:"videoId"`
	} `json:"playlistItemData"`
}

var durationRe = regexp.MustCompile(`^(?:(\d+):)?(\d{1,2}):(\d{2})$`)

// ParseSearch extracts songs from a search response. It walks the whole
// document for list items, so small layout changes do not break it.
func ParseSearch(data []byte) ([]Song, error) {
	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	var songs []Song
	var walk func(any)
	walk = func(v any) {
		switch node := v.(type) {
		case map[string]any:
			if item, ok := node["musicResponsiveListItemRenderer"]; ok {
				raw, _ := json.Marshal(item)
				var li listItem
				if json.Unmarshal(raw, &li) == nil {
					if song, ok := toSong(li); ok {
						songs = append(songs, song)
					}
				}
				return
			}
			for _, child := range node {
				walk(child)
			}
		case []any:
			for _, child := range node {
				walk(child)
			}
		}
	}
	walk(doc)
	return songs, nil
}

func toSong(li listItem) (Song, bool) {
	if li.PlaylistItemData.VideoID == "" || len(li.FlexColumns) < 2 {
		return Song{}, false
	}
	song := Song{VideoID: li.PlaylistItemData.VideoID}
	for _, r := range li.FlexColumns[0].Column.Text.Runs {
		song.Title += r.Text
	}
	runs := li.FlexColumns[1].Column.Text.Runs
	var untagged []string
	for _, r := range runs {
		text := strings.TrimSpace(r.Text)
		switch r.NavigationEndpoint.BrowseEndpoint.Configs.Music.PageType {
		case "MUSIC_PAGE_TYPE_ARTIST":
			song.Artists = append(song.Artists, text)
		case "MUSIC_PAGE_TYPE_ALBUM":
			song.Album = text
		default:
			if m := durationRe.FindStringSubmatch(text); m != nil {
				h, _ := strconv.Atoi(m[1])
				mins, _ := strconv.Atoi(m[2])
				secs, _ := strconv.Atoi(m[3])
				song.Duration = time.Duration(h*3600+mins*60+secs) * time.Second
			} else if text != "" && text != "•" && text != "&" && text != "," {
				untagged = append(untagged, text)
			}
		}
	}
	// Artists without their own page are plain text before the first "•".
	if len(song.Artists) == 0 && len(untagged) > 0 {
		song.Artists = []string{untagged[0]}
	}
	return song, song.Title != ""
}

// Track is a song to look for, e.g. from a Spotify playlist.
type Track struct {
	Title    string
	Artists  []string
	Duration time.Duration
}

// Query is the search text for a track.
func (t Track) Query() string {
	q := cleanTitle(t.Title)
	if len(t.Artists) > 0 {
		q += " " + t.Artists[0]
	}
	return q
}

// BestMatch picks the song that matches the track's title and artist,
// preferring a close duration. ok is false when nothing is convincing.
func BestMatch(t Track, songs []Song) (Song, bool) {
	best, bestScore := Song{}, 0.0
	for _, s := range songs {
		titleScore := similarity(cleanTitle(t.Title), cleanTitle(s.Title))
		artistScore := 0.0
		for _, a := range t.Artists {
			for _, b := range s.Artists {
				artistScore = math.Max(artistScore, similarity(a, b))
			}
		}
		durationScore := 0.0
		if t.Duration > 0 && s.Duration > 0 {
			diff := math.Abs((t.Duration - s.Duration).Seconds())
			durationScore = math.Max(0, 1-diff/30)
		}
		score := 0.55*titleScore + 0.3*artistScore + 0.15*durationScore
		convincing := titleScore >= 0.6 && (artistScore >= 0.5 || durationScore >= 0.7)
		if convincing && score > bestScore {
			best, bestScore = s, score
		}
	}
	return best, bestScore > 0
}

var (
	featRe    = regexp.MustCompile(`(?i)[\(\[]\s*(feat\.?|ft\.?|with)\s[^\)\]]*[\)\]]`)
	versionRe = regexp.MustCompile(`(?i)\s+-\s+.*(remaster|version|edit|mono|stereo|live|mix).*$`)
)

// cleanTitle drops featuring credits and "- Remastered 2011"-style suffixes.
func cleanTitle(s string) string {
	s = featRe.ReplaceAllString(s, "")
	s = versionRe.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

func words(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
		out[w] = true
	}
	return out
}

// similarity is the word-overlap (Jaccard) score of two strings, 0–1.
func similarity(a, b string) float64 {
	wa, wb := words(a), words(b)
	if len(wa) == 0 || len(wb) == 0 {
		return 0
	}
	shared := 0
	for w := range wa {
		if wb[w] {
			shared++
		}
	}
	return float64(shared) / float64(len(wa)+len(wb)-shared)
}

var listRe = regexp.MustCompile(`[?&]list=([\w-]+)`)

// TempPlaylist asks YouTube for a temporary playlist of up to 50 videos and
// returns its ID ("TLGG…").
func (c *Client) TempPlaylist(ctx context.Context, videoIDs []string) (string, error) {
	if len(videoIDs) == 0 || len(videoIDs) > MaxPlaylistSize {
		return "", fmt.Errorf("a temporary playlist needs 1–%d videos, got %d", MaxPlaylistSize, len(videoIDs))
	}
	endpoint := c.WatchVideoURL
	if endpoint == "" {
		endpoint = "https://www.youtube.com/watch_videos"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?video_ids="+url.QueryEscape(strings.Join(videoIDs, ",")), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	client := *c.http()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	if m := listRe.FindStringSubmatch(resp.Header.Get("Location")); m != nil {
		return m[1], nil
	}
	return "", errors.New("youtube did not create a playlist")
}
