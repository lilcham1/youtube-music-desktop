// Package listen implements "listen along": a host's song, position and
// play state are shared with friends through a public relay (ntfy.sh). The
// room code carries a random relay topic and a 256-bit key; every message
// is AES-GCM encrypted, so the relay only ever sees ciphertext.
package listen

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

const (
	codePrefix = "ytm1-"
	topicBytes = 9
	keyBytes   = 32
	// JoinPage is the web page a Discord "Listen along" button opens. It
	// hands the code (kept in the URL fragment, never sent to a server) to
	// the app through the ytm-desktop:// link.
	JoinPage = "https://lilcham1.github.io/youtube-music-desktop/join/"
	// Scheme is the app's link scheme, registered by the installer.
	Scheme = "ytm-desktop"
)

var ErrBadCode = errors.New("that isn't a listen-along code or link")

type Room struct {
	topic [topicBytes]byte
	key   [keyBytes]byte
}

func NewRoom() (Room, error) {
	var r Room
	if _, err := rand.Read(r.topic[:]); err != nil {
		return Room{}, err
	}
	if _, err := rand.Read(r.key[:]); err != nil {
		return Room{}, err
	}
	return r, nil
}

// Topic is the relay topic. It is unguessable but carries no key material.
func (r Room) Topic() string { return "ytm-listen-" + hex.EncodeToString(r.topic[:]) }

// Code is what the host shares.
func (r Room) Code() string {
	return codePrefix + base64.RawURLEncoding.EncodeToString(append(r.topic[:], r.key[:]...))
}

// JoinURL opens the join page, which hands the code to the app.
func (r Room) JoinURL() string { return JoinPage + "#" + r.Code() }

// ParseCode accepts a bare code, a join page link, or a ytm-desktop:// link.
func ParseCode(input string) (Room, error) {
	s := strings.TrimSpace(input)
	if i := strings.LastIndex(s, codePrefix); i >= 0 {
		s = s[i:]
	}
	if u, err := url.PathUnescape(s); err == nil {
		s = u
	}
	s = strings.TrimRight(s, "/ ")
	raw, ok := strings.CutPrefix(s, codePrefix)
	if !ok {
		return Room{}, ErrBadCode
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(data) != topicBytes+keyBytes {
		return Room{}, ErrBadCode
	}
	var r Room
	copy(r.topic[:], data[:topicBytes])
	copy(r.key[:], data[topicBytes:])
	return r, nil
}

// State is what the host shares.
type State struct {
	Seq      int64   `json:"seq"`
	SentAtMS int64   `json:"sentAt"` // host clock, informational
	VideoID  string  `json:"videoId,omitempty"`
	Title    string  `json:"title,omitempty"`
	Artist   string  `json:"artist,omitempty"`
	Position float64 `json:"position"`
	Playing  bool    `json:"playing"`
	Ended    bool    `json:"ended,omitempty"` // the host stopped the session
}

func (r Room) aead() (cipher.AEAD, error) {
	block, err := aes.NewCipher(r.key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Seal encrypts a state for the relay.
func (r Room) Seal(s State) (string, error) {
	plain, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	aead, err := r.aead()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nonce, nonce, plain, []byte(r.Topic()))
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

// Open decrypts a relay message. Anything not sealed with this room's key
// (spam on the topic, tampering) is rejected.
func (r Room) Open(msg string) (State, error) {
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(msg))
	if err != nil {
		return State{}, err
	}
	aead, err := r.aead()
	if err != nil {
		return State{}, err
	}
	if len(data) < aead.NonceSize() {
		return State{}, errors.New("listen: message too short")
	}
	plain, err := aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], []byte(r.Topic()))
	if err != nil {
		return State{}, errors.New("listen: message is not from this room")
	}
	var s State
	return s, json.Unmarshal(plain, &s)
}
