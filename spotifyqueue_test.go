package main

import "testing"

func TestNextQueueAction(t *testing.T) {
	chunk := []string{"a", "b", "c"}
	cases := []struct {
		name      string
		prev, now string
		hasNext   bool
		want      queueAction
	}{
		{"old song still reported after starting", "", "x", true, queueKeep},
		{"same song reported again", "x", "x", true, queueKeep},
		{"next song in the chunk", "a", "b", true, queueKeep},
		{"user skips back within the chunk", "c", "a", true, queueKeep},
		{"autoplay after the last song", "c", "radio", true, queueNextChunk},
		{"autoplay after the very last song", "c", "radio", false, queueFinished},
		{"user picks another song mid-chunk", "b", "other", true, queueAbandoned},
	}
	for _, c := range cases {
		if got := nextQueueAction(chunk, c.prev, c.now, c.hasNext); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
