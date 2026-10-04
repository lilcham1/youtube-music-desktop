//go:build live

package ytmusic

import (
	"context"
	"testing"
	"time"
)

// go test -tags live -run Live ./internal/ytmusic/ — searches the real
// YouTube Music with Spotify-style track data.
func TestLiveMatchingAndPlaylist(t *testing.T) {
	c := &Client{}
	tracks := []Track{
		{Title: "Hey Jude - Remastered 2015", Artists: []string{"The Beatles"}, Duration: 431 * time.Second},
		{Title: "Bad Habits", Artists: []string{"Ed Sheeran"}, Duration: 231 * time.Second},
		{Title: "Despacito (feat. Daddy Yankee)", Artists: []string{"Luis Fonsi", "Daddy Yankee"}, Duration: 229 * time.Second},
		{Title: "Blinding Lights", Artists: []string{"The Weeknd"}, Duration: 200 * time.Second},
		{Title: "La Vie en rose", Artists: []string{"Édith Piaf"}, Duration: 187 * time.Second},
		{Title: "Gangnam Style (강남스타일)", Artists: []string{"PSY"}, Duration: 219 * time.Second},
		{Title: "Bohemian Rhapsody - Remastered 2011", Artists: []string{"Queen"}, Duration: 354 * time.Second},
		{Title: "Lose Yourself", Artists: []string{"Eminem"}, Duration: 326 * time.Second},
		{Title: "Tum Hi Ho", Artists: []string{"Mithoon", "Arijit Singh"}, Duration: 262 * time.Second},
		{Title: "Wonderwall - Remastered", Artists: []string{"Oasis"}, Duration: 258 * time.Second},
	}
	var ids []string
	for _, tr := range tracks {
		songs, err := c.SearchSongs(context.Background(), tr.Query())
		if err != nil {
			t.Fatalf("%s: %v", tr.Title, err)
		}
		song, ok := BestMatch(tr, songs)
		if !ok {
			t.Errorf("no match for %q by %v (query %q, %d results)", tr.Title, tr.Artists, tr.Query(), len(songs))
			continue
		}
		t.Logf("%-38s → %s %q by %v (%v)", tr.Title, song.VideoID, song.Title, song.Artists, song.Duration)
		ids = append(ids, song.VideoID)
	}
	id, err := c.TempPlaylist(context.Background(), ids)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("temporary playlist %s with %d songs", id, len(ids))
}
