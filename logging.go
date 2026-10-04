package main

import (
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// maxLogBytes caps app.log; the previous log is kept as app.log.1.
const maxLogBytes = 1 << 20

// logFile is an io.Writer that rotates app.log when it grows too large.
type logFile struct {
	mu      sync.Mutex
	path    string
	file    *os.File
	written int64
}

func openLogFile(dir string) (*logFile, error) {
	l := &logFile{path: filepath.Join(dir, "app.log")}
	return l, l.open()
}

func (l *logFile) open() error {
	if info, err := os.Stat(l.path); err == nil && info.Size() >= maxLogBytes {
		_ = os.Rename(l.path, l.path+".1")
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	info, _ := f.Stat()
	l.file, l.written = f, 0
	if info != nil {
		l.written = info.Size()
	}
	return nil
}

func (l *logFile) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return len(p), nil
	}
	if l.written+int64(len(p)) > maxLogBytes {
		l.file.Close()
		if err := l.open(); err != nil {
			l.file = nil
			return len(p), nil
		}
	}
	n, err := l.file.Write(p)
	l.written += int64(n)
	return n, err
}

// setupLogging sends the app's log and Wails' warnings to
// %USERPROFILE%\.youtube-music\app.log. It never stops the app from starting:
// without a log file, output is discarded as before.
func setupLogging(profile string) *slog.Logger {
	var out io.Writer = io.Discard
	if f, err := openLogFile(profile); err == nil {
		out = f
	}
	log.SetOutput(out)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("YouTube Music %s starting", version)
	return slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: slog.LevelWarn}))
}
