// Package lastfm scrobbles to Last.fm with the user's own API account:
// desktop sign-in, now-playing updates and batched scrobbles.
// API reference: https://www.last.fm/api
package lastfm

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	apiURL  = "https://ws.audioscrobbler.com/2.0/"
	authURL = "https://www.last.fm/api/auth/"
)

// Error is a Last.fm API error.
type Error struct {
	Code    int
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("last.fm error %d: %s", e.Code, e.Message) }

// Retryable reports whether the request may succeed later (service
// offline, temporarily unavailable, rate limited, or a network failure).
func Retryable(err error) bool {
	var e *Error
	if errors.As(err, &e) {
		return e.Code == 11 || e.Code == 16 || e.Code == 29 || e.Code == 8
	}
	return err != nil
}

// SessionInvalid reports whether the stored session no longer works and
// the user has to connect again.
func SessionInvalid(err error) bool {
	var e *Error
	return errors.As(err, &e) && (e.Code == 9 || e.Code == 4 || e.Code == 26)
}

// NotAuthorizedYet is returned by Session while the user has not approved
// the token in the browser.
func NotAuthorizedYet(err error) bool {
	var e *Error
	return errors.As(err, &e) && (e.Code == 14 || e.Code == 17)
}

type Client struct {
	APIKey, Secret string
	SessionKey     string
	HTTP           *http.Client
	APIURL         string // overridable in tests
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

// Sign returns the api_sig for params: md5 of the sorted key+value pairs
// (excluding format and callback) followed by the shared secret.
func Sign(params url.Values, secret string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		if k != "format" && k != "callback" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString(params.Get(k))
	}
	b.WriteString(secret)
	sum := md5.Sum([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func (c *Client) call(ctx context.Context, method string, params url.Values, post bool, out any) error {
	if c.APIKey == "" || c.Secret == "" {
		return errors.New("add your Last.fm API key and shared secret first")
	}
	params.Set("method", method)
	params.Set("api_key", c.APIKey)
	params.Set("api_sig", Sign(params, c.Secret))
	params.Set("format", "json")
	endpoint := c.APIURL
	if endpoint == "" {
		endpoint = apiURL
	}
	var req *http.Request
	var err error
	if post {
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(params.Encode()))
		if req != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	} else {
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+params.Encode(), nil)
	}
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "youtube-music-desktop")
	resp, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var apiErr struct {
		Error   int    `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &apiErr) == nil && apiErr.Error != 0 {
		return &Error{Code: apiErr.Error, Message: apiErr.Message}
	}
	if resp.StatusCode != http.StatusOK {
		return &Error{Code: 16, Message: resp.Status}
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// Token starts the desktop sign-in and returns the token and the page where
// the user approves it.
func (c *Client) Token(ctx context.Context) (token, approveURL string, err error) {
	var out struct {
		Token string `json:"token"`
	}
	if err := c.call(ctx, "auth.getToken", url.Values{}, false, &out); err != nil {
		return "", "", err
	}
	return out.Token, authURL + "?api_key=" + url.QueryEscape(c.APIKey) + "&token=" + url.QueryEscape(out.Token), nil
}

// Session exchanges an approved token for a session key.
func (c *Client) Session(ctx context.Context, token string) (key, username string, err error) {
	var out struct {
		Session struct {
			Name string `json:"name"`
			Key  string `json:"key"`
		} `json:"session"`
	}
	if err := c.call(ctx, "auth.getSession", url.Values{"token": {token}}, false, &out); err != nil {
		return "", "", err
	}
	return out.Session.Key, out.Session.Name, nil
}

// Track is one play.
type Track struct {
	Artist   string        `json:"artist"`
	Title    string        `json:"title"`
	Album    string        `json:"album,omitempty"`
	Duration time.Duration `json:"duration"`
	Started  time.Time     `json:"started"`
}

func (c *Client) NowPlaying(ctx context.Context, t Track) error {
	p := url.Values{"artist": {t.Artist}, "track": {t.Title}, "sk": {c.SessionKey}}
	if t.Album != "" {
		p.Set("album", t.Album)
	}
	if t.Duration > 0 {
		p.Set("duration", strconv.Itoa(int(t.Duration.Seconds())))
	}
	return c.call(ctx, "track.updateNowPlaying", p, true, nil)
}

// MaxBatch is the most scrobbles one request accepts.
const MaxBatch = 50

func (c *Client) Scrobble(ctx context.Context, tracks []Track) error {
	if len(tracks) == 0 {
		return nil
	}
	if len(tracks) > MaxBatch {
		return fmt.Errorf("at most %d scrobbles per request", MaxBatch)
	}
	p := url.Values{"sk": {c.SessionKey}}
	for i, t := range tracks {
		n := "[" + strconv.Itoa(i) + "]"
		p.Set("artist"+n, t.Artist)
		p.Set("track"+n, t.Title)
		p.Set("timestamp"+n, strconv.FormatInt(t.Started.Unix(), 10))
		if t.Album != "" {
			p.Set("album"+n, t.Album)
		}
		if t.Duration > 0 {
			p.Set("duration"+n, strconv.Itoa(int(t.Duration.Seconds())))
		}
	}
	return c.call(ctx, "track.scrobble", p, true, nil)
}
