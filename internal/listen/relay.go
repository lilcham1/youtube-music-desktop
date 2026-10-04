package listen

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultRelay is the public ntfy.sh server.
const DefaultRelay = "https://ntfy.sh"

// repollDelay is how long after joining the latest message is fetched again.
var repollDelay = 2 * time.Second

// ErrRateLimited means the relay refused more messages for now (ntfy.sh
// limits anonymous publishing per day).
var ErrRateLimited = errors.New("the listen-along relay is rate limiting this connection")

type Relay struct {
	BaseURL string
	HTTP    *http.Client
}

func (r *Relay) base() string {
	if r.BaseURL != "" {
		return strings.TrimRight(r.BaseURL, "/")
	}
	return DefaultRelay
}

func (r *Relay) client() *http.Client {
	if r.HTTP != nil {
		return r.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// Publish sends one message to topic.
func (r *Relay) Publish(ctx context.Context, topic, message string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.base()+"/"+url.PathEscape(topic), strings.NewReader(message))
	if err != nil {
		return err
	}
	resp, err := r.client().Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return ErrRateLimited
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("listen-along relay: %s", resp.Status)
	}
	return nil
}

// Message is one relay delivery.
type Message struct {
	Body string
	// Time is when the relay received it. Live is false for the cached
	// message delivered on connect (the host's latest state).
	Time time.Time
	Live bool
}

type event struct {
	ID      string `json:"id"`
	Time    int64  `json:"time"`
	Event   string `json:"event"`
	Message string `json:"message"`
}

// Subscribe streams topic until ctx ends, reconnecting after failures. It
// first delivers the latest cached message, so a guest who joins mid-song
// starts in the right place. onOpen reports the relay's clock on every
// (re)connect.
//
// The latest message is fetched with a poll request: ntfy.sh's streaming
// "since=latest" can miss a message published moments earlier, while a poll
// returns it reliably. Streaming then resumes after that message (or from a
// few seconds before the poll), so nothing published in between is lost;
// duplicates are possible and guests ignore them by sequence number.
func (r *Relay) Subscribe(ctx context.Context, topic string, onOpen func(serverTime time.Time), onMessage func(Message)) error {
	// ntfy.sh makes a message fetchable about a second after it is
	// published, and a message published in that second before subscribing
	// is not streamed either. Polling once more shortly after joining
	// catches it.
	go func() {
		select {
		case <-ctx.Done():
		case <-time.After(repollDelay):
			_ = r.poll(ctx, topic, func(e event) {
				if e.Event == "message" {
					onMessage(Message{Body: e.Message, Time: time.Unix(e.Time, 0), Live: false})
				}
			})
		}
	}()
	since := ""
	backoff := time.Second
	for {
		connected := time.Now()
		if since == "" {
			since = fmt.Sprint(connected.Add(-5 * time.Second).Unix())
			if err := r.poll(ctx, topic, func(e event) {
				if e.Event == "message" {
					since = e.ID
					onMessage(Message{Body: e.Message, Time: time.Unix(e.Time, 0), Live: false})
				}
			}); err != nil && ctx.Err() != nil {
				return ctx.Err()
			}
		}
		err := r.stream(ctx, topic, since, func(e event) {
			switch e.Event {
			case "open":
				backoff = time.Second
				if onOpen != nil {
					onOpen(time.Unix(e.Time, 0))
				}
			case "message":
				since = e.ID
				t := time.Unix(e.Time, 0)
				onMessage(Message{Body: e.Message, Time: t, Live: t.After(connected.Add(-2 * time.Second))})
			}
		})
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_ = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < time.Minute {
			backoff *= 2
		}
	}
}

// poll fetches the latest cached message, if any.
func (r *Relay) poll(ctx context.Context, topic string, handle func(event)) error {
	return r.read(ctx, r.base()+"/"+url.PathEscape(topic)+"/json?poll=1&since=latest", false, handle)
}

func (r *Relay) stream(ctx context.Context, topic, since string, handle func(event)) error {
	return r.read(ctx, r.base()+"/"+url.PathEscape(topic)+"/json?since="+url.QueryEscape(since), true, handle)
}

// read requests newline-delimited JSON events. A stream is long-lived, so it
// has no overall deadline.
func (r *Relay) read(ctx context.Context, rawURL string, streaming bool, handle func(event)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	client := *r.client()
	if streaming {
		client.Timeout = 0
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("listen-along relay: %s", resp.Status)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 64<<10)
	for sc.Scan() {
		var e event
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			handle(e)
		}
	}
	return sc.Err()
}
