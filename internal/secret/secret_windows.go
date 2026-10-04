// Package secret encrypts small values (API secrets, session keys) with the
// Windows Data Protection API, so settings.json never holds them in plain
// text and they can only be decrypted by the same Windows user.
package secret

import (
	"encoding/base64"
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

const prefix = "dpapi:"

// entropy ties the ciphertext to this app.
var entropy = []byte("youtube-music-desktop")

func blob(b []byte) *windows.DataBlob {
	if len(b) == 0 {
		return &windows.DataBlob{}
	}
	return &windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
}

// Protect returns plain encrypted for the current user. Empty stays empty.
func Protect(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	var out windows.DataBlob
	if err := windows.CryptProtectData(blob([]byte(plain)), nil, blob(entropy), 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return "", err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return prefix + base64.StdEncoding.EncodeToString(unsafe.Slice(out.Data, out.Size)), nil
}

// Unprotect reverses Protect. Empty stays empty.
func Unprotect(stored string) (string, error) {
	if stored == "" {
		return "", nil
	}
	if len(stored) < len(prefix) || stored[:len(prefix)] != prefix {
		return "", errors.New("secret: not a protected value")
	}
	data, err := base64.StdEncoding.DecodeString(stored[len(prefix):])
	if err != nil {
		return "", err
	}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(blob(data), nil, blob(entropy), 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return "", err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return string(unsafe.Slice(out.Data, out.Size)), nil
}
