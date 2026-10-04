package discordbot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	gatewayURL = "wss://gateway.discord.gg/?v=10&encoding=json"
	// Intents: guilds, guild members and guild presences. Members and
	// presences are "privileged": they must be switched on for the bot in
	// the Discord developer portal.
	intents = 1<<0 | 1<<1 | 1<<8
)

var (
	ErrBadToken = errors.New("Discord rejected the bot token. Copy it again from the Bot page of your application (Reset Token)")
	ErrIntents  = errors.New(`Discord refused the bot's permissions. In the Discord developer portal, open your application → Bot and switch on "Presence Intent" and "Server Members Intent"`)
)

// Listener is someone the bot can see.
type Listener struct {
	UserID  string
	Name    string
	Spotify *Spotify // nil when not playing on Spotify
}

// Client keeps a live view of who is playing what on Spotify.
type Client struct {
	Token string
	URL   string // gateway URL; overridable in tests
	// OnChange is called after someone's Spotify status changes, or the
	// connection state changes.
	OnChange func(userID string)

	mu        sync.Mutex
	connected bool
	botID     string
	botName   string
	guilds    map[string]bool
	names     map[string]string
	spotify   map[string]*Spotify
	lastErr   error
}

func (c *Client) changed(userID string) {
	if c.OnChange != nil {
		c.OnChange(userID)
	}
}

// Status describes the connection for the settings page.
type Status struct {
	Connected bool
	BotID     string
	BotName   string
	Servers   int
	Err       error
}

func (c *Client) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Status{Connected: c.connected, BotID: c.botID, BotName: c.botName, Servers: len(c.guilds), Err: c.lastErr}
}

// Listening returns everyone currently playing on Spotify, by name.
func (c *Client) Listening() []Listener {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []Listener
	for id, s := range c.spotify {
		if s != nil {
			out = append(out, Listener{UserID: id, Name: c.nameLocked(id), Spotify: s})
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// Get returns one person's current state.
func (c *Client) Get(userID string) Listener {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Listener{UserID: userID, Name: c.nameLocked(userID), Spotify: c.spotify[userID]}
}

func (c *Client) nameLocked(id string) string {
	if n := c.names[id]; n != "" {
		return n
	}
	return "user " + id
}

// InviteURL adds the bot to a server (no permissions needed: it only reads
// statuses, which come with the Presence intent).
func InviteURL(botID string) string {
	return "https://discord.com/oauth2/authorize?client_id=" + botID + "&scope=bot&permissions=0"
}

// Run stays connected until ctx ends, reconnecting after drops. It returns
// early for errors that retrying cannot fix (bad token, intents disabled).
func (c *Client) Run(ctx context.Context) error {
	backoff := time.Second
	for {
		err := c.session(ctx)
		c.mu.Lock()
		c.connected = false
		c.lastErr = err
		c.mu.Unlock()
		c.changed("")
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, ErrBadToken) || errors.Is(err, ErrIntents) {
			return err
		}
		jitter := time.Duration(rand.Int64N(int64(time.Second)))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff + jitter):
		}
		if backoff < time.Minute {
			backoff *= 2
		}
	}
}

type payload struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d"`
	S  *int64          `json:"s"`
	T  string          `json:"t"`
}

type member struct {
	User struct {
		ID         string `json:"id"`
		Username   string `json:"username"`
		GlobalName string `json:"global_name"`
		Bot        bool   `json:"bot"`
	} `json:"user"`
	Nick string `json:"nick"`
}

type presence struct {
	User struct {
		ID string `json:"id"`
	} `json:"user"`
	Activities []activity `json:"activities"`
}

func displayName(m member) string {
	switch {
	case m.Nick != "":
		return m.Nick
	case m.User.GlobalName != "":
		return m.User.GlobalName
	default:
		return m.User.Username
	}
}

// session runs one gateway connection: hello, identify, heartbeats, events.
func (c *Client) session(ctx context.Context) error {
	url := c.URL
	if url == "" {
		url = gatewayURL
	}
	dialCtx, cancelDial := context.WithTimeout(ctx, 20*time.Second)
	conn, _, err := websocket.Dial(dialCtx, url, nil)
	cancelDial()
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(64 << 20) // guild snapshots can be large

	var hello struct {
		HeartbeatInterval int64 `json:"heartbeat_interval"`
	}
	p, err := read(ctx, conn)
	if err != nil {
		return err
	}
	if p.Op != 10 || json.Unmarshal(p.D, &hello) != nil || hello.HeartbeatInterval <= 0 {
		return fmt.Errorf("discord: unexpected first message (op %d)", p.Op)
	}
	identify := map[string]any{"op": 2, "d": map[string]any{
		"token":      c.Token,
		"intents":    intents,
		"properties": map[string]string{"os": "windows", "browser": "youtube-music-desktop", "device": "youtube-music-desktop"},
	}}
	if err := write(ctx, conn, identify); err != nil {
		return err
	}

	var seqMu sync.Mutex
	var seq *int64
	ackMissed := make(chan struct{}, 1)
	acked := true
	var ackMu sync.Mutex
	hbCtx, stopHeartbeat := context.WithCancel(ctx)
	defer stopHeartbeat()
	go func() {
		interval := time.Duration(hello.HeartbeatInterval) * time.Millisecond
		// The first heartbeat goes after a random fraction of the interval.
		timer := time.NewTimer(time.Duration(rand.Float64() * float64(interval)))
		defer timer.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-timer.C:
			}
			ackMu.Lock()
			if !acked {
				ackMu.Unlock()
				ackMissed <- struct{}{}
				return
			}
			acked = false
			ackMu.Unlock()
			seqMu.Lock()
			s := seq
			seqMu.Unlock()
			if write(hbCtx, conn, map[string]any{"op": 1, "d": s}) != nil {
				return
			}
			timer.Reset(interval)
		}
	}()

	for {
		select {
		case <-ackMissed:
			return errors.New("discord: heartbeat not acknowledged")
		default:
		}
		p, err := read(ctx, conn)
		if err != nil {
			return closeError(err)
		}
		if p.S != nil {
			seqMu.Lock()
			seq = p.S
			seqMu.Unlock()
		}
		switch p.Op {
		case 11: // heartbeat ACK
			ackMu.Lock()
			acked = true
			ackMu.Unlock()
		case 1: // heartbeat request
			seqMu.Lock()
			s := seq
			seqMu.Unlock()
			_ = write(ctx, conn, map[string]any{"op": 1, "d": s})
		case 7, 9: // reconnect / invalid session
			return errors.New("discord asked the bot to reconnect")
		case 0:
			c.dispatch(p.T, p.D)
		}
	}
}

func (c *Client) dispatch(event string, data json.RawMessage) {
	switch event {
	case "READY":
		var ready struct {
			User struct {
				ID       string `json:"id"`
				Username string `json:"username"`
			} `json:"user"`
		}
		_ = json.Unmarshal(data, &ready)
		c.mu.Lock()
		c.connected, c.lastErr = true, nil
		c.botID, c.botName = ready.User.ID, ready.User.Username
		c.guilds, c.names, c.spotify = map[string]bool{}, map[string]string{}, map[string]*Spotify{}
		c.mu.Unlock()
		c.changed("")
	case "GUILD_CREATE":
		var guild struct {
			ID          string     `json:"id"`
			Unavailable bool       `json:"unavailable"`
			Members     []member   `json:"members"`
			Presences   []presence `json:"presences"`
		}
		if json.Unmarshal(data, &guild) != nil || guild.Unavailable {
			return
		}
		c.mu.Lock()
		c.guilds[guild.ID] = true
		for _, m := range guild.Members {
			if !m.User.Bot && m.User.ID != "" {
				c.names[m.User.ID] = displayName(m)
			}
		}
		var updated []string
		for _, p := range guild.Presences {
			c.spotify[p.User.ID] = spotifyFrom(p.Activities)
			updated = append(updated, p.User.ID)
		}
		c.mu.Unlock()
		for _, id := range updated {
			c.changed(id)
		}
		c.changed("")
	case "GUILD_DELETE":
		var guild struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(data, &guild)
		c.mu.Lock()
		delete(c.guilds, guild.ID)
		c.mu.Unlock()
		c.changed("")
	case "GUILD_MEMBER_ADD", "GUILD_MEMBER_UPDATE":
		var m member
		if json.Unmarshal(data, &m) == nil && !m.User.Bot && m.User.ID != "" {
			c.mu.Lock()
			c.names[m.User.ID] = displayName(m)
			c.mu.Unlock()
		}
	case "PRESENCE_UPDATE":
		var p presence
		if json.Unmarshal(data, &p) != nil || p.User.ID == "" {
			return
		}
		s := spotifyFrom(p.Activities)
		c.mu.Lock()
		old := c.spotify[p.User.ID]
		c.spotify[p.User.ID] = s
		c.mu.Unlock()
		// The same person in several servers produces repeated updates.
		if !sameSpotify(old, s) {
			c.changed(p.User.ID)
		}
	}
}

func sameSpotify(a, b *Spotify) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.TrackID == b.TrackID && a.Start.Equal(b.Start) && a.End.Equal(b.End)
}

// closeError turns Discord's close codes into errors a user can act on.
func closeError(err error) error {
	switch websocket.CloseStatus(err) {
	case 4004:
		return ErrBadToken
	case 4013, 4014:
		return ErrIntents
	}
	return err
}

func read(ctx context.Context, conn *websocket.Conn) (payload, error) {
	_, data, err := conn.Read(ctx)
	if err != nil {
		return payload{}, err
	}
	var p payload
	if err := json.Unmarshal(data, &p); err != nil {
		return payload{}, fmt.Errorf("discord: bad message: %w", err)
	}
	return p, nil
}

func write(ctx context.Context, conn *websocket.Conn, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return conn.Write(wctx, websocket.MessageText, data)
}
