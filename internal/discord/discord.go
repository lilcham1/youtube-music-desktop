// Package discord is a minimal Discord Rich Presence client speaking the
// desktop client's local IPC protocol (\\.\pipe\discord-ipc-N): frames of
// little-endian uint32 opcode + uint32 length + JSON payload.
package discord

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"
)

const (
	opHandshake = 0
	opFrame     = 1
	opClose     = 2

	ioTimeout      = 5 * time.Second
	youtubeIconURL = "https://music.youtube.com/img/favicon_144.png"
	// The small badge on the status shows which app is playing.
	encoreIconURL = "https://lilcham1.github.io/youtube-music-desktop/img/encore-icon-256.png"
	encoreName    = "Encore for YouTube Music"
)

// Dialer opens a connection to one IPC pipe. Replaced in tests.
type Dialer func(path string, timeout time.Duration) (net.Conn, error)

// Client is one connection to the local Discord desktop client.
type Client struct {
	conn  net.Conn
	appID string
}

// Track is what is currently playing. Times are Unix milliseconds.
type Track struct {
	Title, Artist, Album, Artwork string
	StartMs, EndMs                int64
	// JoinURL, when set, adds a "Listen along" button to the status, and
	// DownloadURL a "Get Encore" button. Discord shows activity buttons to
	// other people only, never to yourself, and at most two.
	JoinURL, DownloadURL string
}

// Connect tries discord-ipc-0..9 and performs the handshake.
func Connect(dial Dialer, appID string) (*Client, error) {
	var lastErr error = errors.New("discord is not running")
	for i := 0; i < 10; i++ {
		conn, err := dial(fmt.Sprintf(`\\.\pipe\discord-ipc-%d`, i), time.Second)
		if err != nil {
			lastErr = err
			continue
		}
		c := &Client{conn: conn, appID: appID}
		if err := c.handshake(); err != nil {
			conn.Close()
			lastErr = err
			continue
		}
		return c, nil
	}
	return nil, lastErr
}

func (c *Client) handshake() error {
	if err := c.write(opHandshake, map[string]any{"v": 1, "client_id": c.appID}); err != nil {
		return err
	}
	op, msg, err := c.read()
	if err != nil {
		return err
	}
	if op == opClose || msg.Evt != "READY" {
		return fmt.Errorf("discord handshake rejected: %s", msg.errorText())
	}
	return nil
}

// AppID reports which application this connection was made for.
func (c *Client) AppID() string { return c.appID }

// SetActivity shows the track as a "Listening to" activity. If Discord
// rejects it, it retries without the artwork and then without the buttons,
// so the status still shows.
func (c *Client) SetActivity(t Track) error {
	activity := BuildActivity(t)
	send := func() error {
		return c.request("SET_ACTIVITY", map[string]any{"pid": os.Getpid(), "activity": activity})
	}
	err := send()
	// Step down rather than lose the status: first without the artwork
	// (Discord-side asset problems), then without the buttons.
	if err != nil {
		delete(activity, "assets")
		err = send()
	}
	if _, hasButtons := activity["buttons"]; err != nil && hasButtons {
		delete(activity, "buttons")
		err = send()
	}
	return err
}

// ClearActivity removes the status.
func (c *Client) ClearActivity() error {
	return c.request("SET_ACTIVITY", map[string]any{"pid": os.Getpid()})
}

func (c *Client) Close() error {
	_ = c.write(opClose, map[string]any{})
	return c.conn.Close()
}

// BuildActivity mirrors the Electron release's SET_ACTIVITY payload.
func BuildActivity(t Track) map[string]any {
	artist := t.Artist
	if artist == "" {
		artist = "YouTube Music"
	}
	artwork := t.Artwork
	if artwork == "" {
		artwork = youtubeIconURL
	}
	assets := map[string]any{
		"large_image": artwork,
		"small_image": encoreIconURL,
		"small_text":  encoreName,
	}
	// Single releases often use the song title as the album title; skip the
	// duplicate line in that case.
	if t.Album != "" && !strings.EqualFold(t.Album, t.Title) {
		assets["large_text"] = t.Album
	}
	activity := map[string]any{
		"type":                2, // Listening
		"status_display_type": 1, // compact row shows state (the artist)
		"name":                artist,
		"details":             t.Title,
		"state":               artist,
		"assets":              assets,
		"instance":            false,
	}
	var buttons []map[string]string
	if t.JoinURL != "" {
		buttons = append(buttons, map[string]string{"label": "Listen along", "url": t.JoinURL})
	}
	if t.DownloadURL != "" {
		buttons = append(buttons, map[string]string{"label": "Get Encore", "url": t.DownloadURL})
	}
	if len(buttons) > 0 {
		activity["buttons"] = buttons
	}
	if t.StartMs > 0 {
		ts := map[string]any{"start": t.StartMs}
		if t.EndMs > t.StartMs {
			ts["end"] = t.EndMs
		}
		activity["timestamps"] = ts
	}
	return activity
}

type message struct {
	Cmd   string          `json:"cmd"`
	Evt   string          `json:"evt"`
	Nonce string          `json:"nonce"`
	Data  json.RawMessage `json:"data"`
}

func (m message) errorText() string {
	var d struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(m.Data, &d)
	if d.Message != "" {
		return d.Message
	}
	return string(m.Data)
}

func (c *Client) request(cmd string, args map[string]any) error {
	nonce := newNonce()
	if err := c.write(opFrame, map[string]any{"cmd": cmd, "args": args, "nonce": nonce}); err != nil {
		return err
	}
	for {
		op, msg, err := c.read()
		if err != nil {
			return err
		}
		if op == opClose {
			return fmt.Errorf("discord closed the connection: %s", msg.errorText())
		}
		if msg.Nonce != nonce {
			continue
		}
		if msg.Evt == "ERROR" {
			return fmt.Errorf("discord %s failed: %s", cmd, msg.errorText())
		}
		return nil
	}
}

func (c *Client) write(op uint32, payload any) error {
	_ = c.conn.SetWriteDeadline(time.Now().Add(ioTimeout))
	return WriteFrame(c.conn, op, payload)
}

func (c *Client) read() (uint32, message, error) {
	_ = c.conn.SetReadDeadline(time.Now().Add(ioTimeout))
	op, body, err := ReadFrame(c.conn)
	if err != nil {
		return 0, message{}, err
	}
	var msg message
	if err := json.Unmarshal(body, &msg); err != nil {
		return 0, message{}, err
	}
	return op, msg, nil
}

// WriteFrame encodes one IPC frame.
func WriteFrame(w io.Writer, op uint32, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	frame := make([]byte, 8+len(body))
	binary.LittleEndian.PutUint32(frame[0:], op)
	binary.LittleEndian.PutUint32(frame[4:], uint32(len(body)))
	copy(frame[8:], body)
	_, err = w.Write(frame)
	return err
}

// ReadFrame decodes one IPC frame.
func ReadFrame(r io.Reader) (uint32, []byte, error) {
	var header [8]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, nil, err
	}
	n := binary.LittleEndian.Uint32(header[4:])
	if n > 1<<20 {
		return 0, nil, fmt.Errorf("discord frame too large: %d bytes", n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, nil, err
	}
	return binary.LittleEndian.Uint32(header[:4]), body, nil
}

func newNonce() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
