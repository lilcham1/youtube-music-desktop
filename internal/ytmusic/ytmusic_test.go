package ytmusic

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// testdata/search-songs.json is a real response for
// "Thinking Out Loud Ed Sheeran" with the songs filter.
func fixture(t *testing.T) []Song {
	t.Helper()
	data, err := os.ReadFile("testdata/search-songs.json")
	if err != nil {
		t.Fatal(err)
	}
	songs, err := ParseSearch(data)
	if err != nil {
		t.Fatal(err)
	}
	return songs
}

func TestParseSearchReadsRealResponse(t *testing.T) {
	songs := fixture(t)
	if len(songs) < 10 {
		t.Fatalf("parsed %d songs", len(songs))
	}
	first := songs[0]
	if first.VideoID != "fdz_cabS9BU" || first.Title != "Thinking out Loud" || len(first.Artists) != 1 ||
		first.Artists[0] != "Ed Sheeran" || first.Album != "x (Deluxe Edition)" || first.Duration != 4*time.Minute+42*time.Second {
		t.Fatalf("first song = %+v", first)
	}
}

func TestBestMatchPrefersRightArtistAndDuration(t *testing.T) {
	songs := fixture(t)
	got, ok := BestMatch(Track{Title: "Thinking out Loud", Artists: []string{"Ed Sheeran"}, Duration: 281 * time.Second}, songs)
	if !ok || got.VideoID != "fdz_cabS9BU" {
		t.Fatalf("got %+v, %v", got, ok)
	}
	// A cover with the same title but another artist is not the original.
	if got, ok := BestMatch(Track{Title: "Thinking Out Loud", Artists: []string{"Fiona Culley"}, Duration: 373 * time.Second}, songs); !ok || got.Artists[0] != "Fiona Culley" {
		t.Fatalf("cover match = %+v, %v", got, ok)
	}
	if _, ok := BestMatch(Track{Title: "Bohemian Rhapsody", Artists: []string{"Queen"}}, songs); ok {
		t.Fatal("an unrelated track must not match")
	}
}

func TestCleanTitleAndQuery(t *testing.T) {
	for in, want := range map[string]string{
		"Shape of You":                         "Shape of You",
		"Heroes (feat. Someone)":               "Heroes",
		"Hey Jude - Remastered 2015":           "Hey Jude",
		"Bad Habits [with Ed Sheeran]":         "Bad Habits",
		"Wonderwall - Live at Knebworth, 1996": "Wonderwall",
	} {
		if got := cleanTitle(in); got != want {
			t.Errorf("cleanTitle(%q) = %q; want %q", in, got, want)
		}
	}
	if q := (Track{Title: "Hey Jude - Remastered 2015", Artists: []string{"The Beatles", "x"}}).Query(); q != "Hey Jude The Beatles" {
		t.Fatalf("query = %q", q)
	}
}

func TestSearchSongsSendsSongsFilter(t *testing.T) {
	data, _ := os.ReadFile("testdata/search-songs.json")
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Write(data)
	}))
	defer srv.Close()
	c := &Client{SearchURL: srv.URL, ClientVersion: "1.2.3"}
	songs, err := c.SearchSongs(context.Background(), "Thinking Out Loud Ed Sheeran")
	if err != nil || len(songs) == 0 {
		t.Fatalf("songs %d, %v", len(songs), err)
	}
	for _, want := range []string{songsFilter, `"clientVersion":"1.2.3"`, `"clientName":"WEB_REMIX"`, "Thinking Out Loud Ed Sheeran"} {
		if !strings.Contains(body, want) {
			t.Errorf("request body lacks %q: %s", want, body)
		}
	}
}

func TestTempPlaylistReadsRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("video_ids") != "a,b,c" {
			t.Errorf("video_ids = %q", r.URL.Query().Get("video_ids"))
		}
		http.Redirect(w, r, "/watch?v=a&list=TLGGtest123", http.StatusSeeOther)
	}))
	defer srv.Close()
	c := &Client{WatchVideoURL: srv.URL + "/watch_videos"}
	id, err := c.TempPlaylist(context.Background(), []string{"a", "b", "c"})
	if err != nil || id != "TLGGtest123" {
		t.Fatalf("id %q, %v", id, err)
	}
	if _, err := c.TempPlaylist(context.Background(), make([]string, 51)); err == nil {
		t.Fatal("more than 50 videos must be rejected")
	}
}
