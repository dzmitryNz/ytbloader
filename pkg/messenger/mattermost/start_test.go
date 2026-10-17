package mattermost

import (
	"bytes"
	"context"
	"log"
	"os"
	"strings"
	"testing"
	"time"
)

func TestStartLogsSendOnlyOnceAndStopsWithContext(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	m := &Messenger{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Start(ctx) }()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start returned %v, want nil", err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("Start did not return soon after the context was cancelled")
	}

	if n := strings.Count(logs.String(), "mattermost: receiving is not supported"); n != 1 {
		t.Errorf("send-only notice logged %d times, want 1; log: %q", n, logs.String())
	}
}
