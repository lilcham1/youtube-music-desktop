//go:build live

package discordbot

import (
	"context"
	"errors"
	"testing"
	"time"
)

// go test -tags live -run Live ./internal/discordbot/ — connects to the real
// Discord gateway with an invalid token.
func TestLiveInvalidTokenIsExplained(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := (&Client{Token: "invalid.token.value"}).Run(ctx)
	if !errors.Is(err, ErrBadToken) {
		t.Fatalf("err = %v", err)
	}
}
