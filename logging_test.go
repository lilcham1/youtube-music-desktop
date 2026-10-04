package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogFileRotatesAtCap(t *testing.T) {
	dir := t.TempDir()
	l, err := openLogFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	line := []byte(strings.Repeat("x", 1023) + "\n")
	for i := 0; i < (maxLogBytes/len(line))+10; i++ {
		if _, err := l.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	l.file.Close()
	current, err := os.Stat(filepath.Join(dir, "app.log"))
	if err != nil {
		t.Fatal(err)
	}
	previous, err := os.Stat(filepath.Join(dir, "app.log.1"))
	if err != nil {
		t.Fatal("the previous log must be kept as app.log.1")
	}
	if current.Size() > maxLogBytes || previous.Size() > maxLogBytes {
		t.Fatalf("logs exceed the cap: %d, %d", current.Size(), previous.Size())
	}
}
