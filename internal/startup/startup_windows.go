// Package startup manages the per-user "Start with Windows" entry.
package startup

import (
	"errors"

	"golang.org/x/sys/windows/registry"
)

const (
	runKey    = `Software\Microsoft\Windows\CurrentVersion\Run`
	valueName = "Encore"
	// HiddenFlag starts the app in the notification area.
	HiddenFlag = "--hidden"
)

// Enabled reports whether a Run entry exists for this app.
func Enabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(valueName)
	return err == nil
}

// Set adds or removes the Run entry for exe.
func Set(exe string, enabled bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if enabled {
		return k.SetStringValue(valueName, `"`+exe+`" `+HiddenFlag)
	}
	if err := k.DeleteValue(valueName); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}
