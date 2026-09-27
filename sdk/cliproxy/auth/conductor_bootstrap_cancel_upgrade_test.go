package auth

import (
	"context"
	"errors"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestReadStreamBootstrapCanceledClosedSourceReturnsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	source := make(chan cliproxyexecutor.StreamChunk)
	cancel()
	close(source)
	for range 64 {
		buffered, closed, err := readStreamBootstrap(ctx, source)
		if !errors.Is(err, context.Canceled) || closed || len(buffered) != 0 {
			t.Fatalf("canceled closed source became upstream EOF: buffered=%v closed=%v error=%v", buffered, closed, err)
		}
	}
	buffered, closed, err := readStreamBootstrap(context.Background(), source)
	if err != nil || !closed || len(buffered) != 0 {
		t.Fatalf("uncanceled EOF was changed: buffered=%v closed=%v error=%v", buffered, closed, err)
	}
}
