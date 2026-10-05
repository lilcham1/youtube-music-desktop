package main

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestDebouncerCoalescesAndFlushes(t *testing.T) {
	var calls atomic.Int32
	b := newDebouncer(50*time.Millisecond, func() { calls.Add(1) })
	for i := 0; i < 10; i++ {
		b.Trigger()
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(120 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatalf("a burst must produce one call, got %d", calls.Load())
	}
	b.Trigger()
	b.Flush()
	if calls.Load() != 2 {
		t.Fatalf("Flush must run the pending call, got %d", calls.Load())
	}
	time.Sleep(80 * time.Millisecond)
	b.Flush()
	if calls.Load() != 2 {
		t.Fatalf("a flushed call must not run again, got %d", calls.Load())
	}
}

func TestJoinCodeFromArgs(t *testing.T) {
	if got := joinCodeFromArgs([]string{"--hidden", "ytm-desktop://join/ytm1-abc"}); got != "ytm-desktop://join/ytm1-abc" {
		t.Fatalf("got %q", got)
	}
	if got := joinCodeFromArgs([]string{"--hidden"}); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestSentence(t *testing.T) {
	for in, want := range map[string]string{
		"that code doesn't look right": "That code doesn't look right.",
		"Already a sentence.":          "Already a sentence.",
		"élan":                         "Élan.",
	} {
		if got := sentence(errors.New(in)); got != want {
			t.Errorf("sentence(%q) = %q; want %q", in, got, want)
		}
	}
}
