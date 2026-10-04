package spotify

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestParsePlaylistID(t *testing.T) {
	const id = "37i9dQZF1DXcBWIGoYBM5M"
	for _, in := range []string{
		id,
		"spotify:playlist:" + id,
		"https://open.spotify.com/playlist/" + id,
		"https://open.spotify.com/playlist/" + id + "?si=abc123",
		"https://open.spotify.com/intl-de/playlist/" + id,
		"  https://open.spotify.com/playlist/" + id + "  ",
	} {
		if got, err := ParsePlaylistID(in); err != nil || got != id {
			t.Errorf("ParsePlaylistID(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "hello", "https://example.com/playlist/" + id, "https://open.spotify.com/album/" + id, "spotify:album:" + id} {
		if _, err := ParsePlaylistID(bad); !errors.Is(err, ErrBadLink) {
			t.Errorf("ParsePlaylistID(%q) err = %v", bad, err)
		}
	}
}

// fakeSpotify serves a token endpoint and a two-page playlist.
func fakeSpotify(t *testing.T, status int) (*httptest.Server, *int) {
	tokens := 0
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			user, pass, _ := r.BasicAuth()
			if user != "id" || pass != "secret" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			tokens++
			fmt.Fprint(w, `{"access_token":"tok","expires_in":3600}`)
		case r.Header.Get("Authorization") != "Bearer tok":
			w.WriteHeader(http.StatusUnauthorized)
		case status != http.StatusOK:
			w.WriteHeader(status)
		case r.URL.Path == "/v1/playlists/PL" && r.URL.Query().Get("fields") == "name":
			fmt.Fprint(w, `{"name":"Road trip"}`)
		case r.URL.Path == "/v1/playlists/PL/tracks" && r.URL.Query().Get("offset") == "":
			fmt.Fprintf(w, `{"items":[
				{"track":{"name":"Song A","type":"track","duration_ms":200000,"artists":[{"name":"Artist 1"},{"name":"Artist 2"}]}},
				{"track":{"name":"Local file","type":"track","is_local":true,"artists":[]}},
				{"track":{"name":"An episode","type":"episode","artists":[]}},
				{"track":null}
			],"next":"%s/v1/playlists/PL/tracks?offset=100"}`, srv.URL)
		case r.URL.Path == "/v1/playlists/PL/tracks":
			fmt.Fprint(w, `{"items":[{"item":{"name":"Song B","type":"track","duration_ms":61000,"artists":[{"name":"Artist 3"}]}}],"next":null}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &tokens
}

func client(srv *httptest.Server, secret string) *Client {
	return &Client{ClientID: "id", ClientSecret: secret, TokenURL: srv.URL + "/token", APIBase: srv.URL + "/v1"}
}

func TestPlaylistReadsAllPagesAndSkipsNonSongs(t *testing.T) {
	srv, tokens := fakeSpotify(t, http.StatusOK)
	c := client(srv, "secret")
	pl, err := c.Playlist(context.Background(), "PL")
	if err != nil {
		t.Fatal(err)
	}
	if pl.Name != "Road trip" || len(pl.Tracks) != 2 || pl.Skipped != 3 {
		t.Fatalf("playlist = %+v", pl)
	}
	a, b := pl.Tracks[0], pl.Tracks[1]
	if a.Title != "Song A" || strings.Join(a.Artists, ",") != "Artist 1,Artist 2" || a.Duration != 200*time.Second {
		t.Fatalf("track A = %+v", a)
	}
	if b.Title != "Song B" || b.Artists[0] != "Artist 3" || b.Duration != 61*time.Second {
		t.Fatalf("track B = %+v", b)
	}
	if _, err := c.Playlist(context.Background(), "PL"); err != nil || *tokens != 1 {
		t.Fatalf("token must be reused, fetched %d times (%v)", *tokens, err)
	}
}

func TestPlaylistErrorsAreExplained(t *testing.T) {
	srv, _ := fakeSpotify(t, http.StatusOK)
	if _, err := client(srv, "wrong").Playlist(context.Background(), "PL"); !errors.Is(err, ErrCredentials) {
		t.Fatalf("bad secret: %v", err)
	}
	if _, err := (&Client{}).Playlist(context.Background(), "PL"); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("no credentials: %v", err)
	}
	for status, want := range map[int]error{http.StatusNotFound: ErrNotFound, http.StatusForbidden: ErrForbidden} {
		srv, _ := fakeSpotify(t, status)
		if _, err := client(srv, "secret").Playlist(context.Background(), "PL"); !errors.Is(err, want) {
			t.Fatalf("status %d: %v", status, err)
		}
	}
}

func TestPremiumRequirementIsRecognised(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			fmt.Fprint(w, `{"access_token":"tok","expires_in":3600}`)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		// The body Spotify returned for this installation on 2026-10-04.
		fmt.Fprint(w, `{"error":{"status":403,"message":"Active premium subscription required for the owner of the app. When the subscription status changes, it can take a few hours before requests are allowed again."}}`)
	}))
	defer srv.Close()
	if _, err := client(srv, "secret").Playlist(context.Background(), "PL"); !errors.Is(err, ErrPremiumRequired) {
		t.Fatalf("err = %v", err)
	}
}

// testdata/embed-playlist.html is Spotify's real embed page for its public
// "Spotify Web API Testing playlist".
func TestParsePublicPage(t *testing.T) {
	page, err := os.ReadFile("testdata/embed-playlist.html")
	if err != nil {
		t.Fatal(err)
	}
	pl, err := ParsePublicPage(page)
	if err != nil {
		t.Fatal(err)
	}
	if pl.Name != "Spotify Web API Testing playlist" || len(pl.Tracks) != 5 || !pl.FromPublicPage {
		t.Fatalf("playlist = %+v", pl)
	}
	first := pl.Tracks[0]
	if first.Title != "Api" || len(first.Artists) != 1 || first.Artists[0] != "Odiseo" || first.Duration != 376*time.Second {
		t.Fatalf("first track = %+v", first)
	}
	var multi Track
	for _, tr := range pl.Tracks {
		if tr.Title == "Endpoints" {
			multi = tr
		}
	}
	if strings.Join(multi.Artists, "|") != "Glenn Horiuchi|Glenn Horiuchi Trio" {
		t.Fatalf("artists = %v", multi.Artists)
	}
	if _, err := ParsePublicPage([]byte("<html>not a player</html>")); err == nil {
		t.Fatal("an unrecognised page must be an error")
	}
}

func TestPublicPlaylistNotFound(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	if _, err := PublicPlaylist(context.Background(), nil, srv.URL+"/", "PL"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}
