package main

import (
	"errors"
	"testing"

	"youtube-music/internal/secret"
	"youtube-music/internal/settings"
)

func TestLastfmAppPrefersTheUsersOwnAccount(t *testing.T) {
	defer func(k, s string) { lastfmAPIKey, lastfmAppSecret = k, s }(lastfmAPIKey, lastfmAppSecret)
	own, err := secret.Protect("own-secret")
	if err != nil {
		t.Fatal(err)
	}

	lastfmAPIKey, lastfmAppSecret = "", ""
	if _, _, _, err := lastfmApp(settings.LastFM{}); !errors.Is(err, errNoLastfmApp) {
		t.Fatalf("no account anywhere: err = %v", err)
	}
	if lastfmReadKey(settings.LastFM{}) != "" {
		t.Fatal("no key to read with")
	}

	lastfmAPIKey, lastfmAppSecret = "app-key", "app-secret"
	key, sec, builtIn, err := lastfmApp(settings.LastFM{})
	if err != nil || key != "app-key" || sec != "app-secret" || !builtIn {
		t.Fatalf("built-in: %q %q %v %v", key, sec, builtIn, err)
	}
	key, sec, builtIn, err = lastfmApp(settings.LastFM{APIKey: "own-key", Secret: own})
	if err != nil || key != "own-key" || sec != "own-secret" || builtIn {
		t.Fatalf("own account: %q %q %v %v", key, sec, builtIn, err)
	}
	// A key without its secret never borrows the built-in secret.
	if _, _, _, err := lastfmApp(settings.LastFM{APIKey: "own-key"}); !errors.Is(err, errNoLastfmApp) {
		t.Fatalf("own key without secret: err = %v", err)
	}
	if lastfmReadKey(settings.LastFM{APIKey: "own-key"}) != "own-key" || lastfmReadKey(settings.LastFM{}) != "app-key" {
		t.Fatal("read key precedence")
	}
}
