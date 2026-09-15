package executor

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestConsumeDevinFramesToInteractions_ToolLimitRejectsContinuation(t *testing.T) {
	body := devinToolLimitFrames()
	_, responseLog, err := consumeDevinFramesToInteractions(bytes.NewReader(body), "devin/swe-2", "tool-limit")
	if err != nil {
		t.Fatalf("consumeDevinFramesToInteractions() error = %v", err)
	}
	if len(responseLog.ToolCalls) != maxDevinToolCalls {
		t.Fatalf("tool calls = %d, want %d", len(responseLog.ToolCalls), maxDevinToolCalls)
	}
	for i, call := range responseLog.ToolCalls {
		if want := fmt.Sprintf("call_%d", i); call.ID != want {
			t.Errorf("tool call %d ID = %q, want %q", i, call.ID, want)
		}
		wantArgs := fmt.Sprintf(`{"accepted":%d}`, i)
		if i == 0 {
			wantArgs = `{"keep":true}`
		}
		if call.Arguments != wantArgs {
			t.Errorf("tool call %d arguments = %q, want %q", i, call.Arguments, wantArgs)
		}
	}
}

func TestStreamDevinFrames_ToolLimitMatchesNonStream(t *testing.T) {
	body := devinToolLimitFrames()
	exec := NewDevinExecutor(&config.Config{})
	out := make(chan cliproxyexecutor.StreamChunk)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		defer close(out)
		exec.streamDevinFrames(ctx, bytes.NewReader(body), cliproxyexecutor.Request{Model: "devin/swe-2"},
			cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatInteractions}, "tool-limit",
			sdktranslator.FormatInteractions, nil, out)
	}()

	ids := make(map[int]string)
	args := make(map[int]*strings.Builder)
	stops := make(map[int]int)
	completed := 0
	for chunk := range out {
		if chunk.Err != nil {
			t.Errorf("stream chunk error = %v", chunk.Err)
			continue
		}
		for _, line := range strings.Split(string(chunk.Payload), "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			event := gjson.Parse(strings.TrimPrefix(line, "data: "))
			index := int(event.Get("index").Int())
			switch event.Get("event_type").String() {
			case "step.start":
				if event.Get("step.type").String() != "function_call" {
					t.Errorf("unexpected step type: %s", event.Raw)
					continue
				}
				ids[index] = event.Get("step.id").String()
				if args[index] == nil {
					args[index] = &strings.Builder{}
				}
			case "step.delta":
				if args[index] == nil || stops[index] != 0 {
					t.Errorf("arguments arrived outside active step %d", index)
					continue
				}
				args[index].WriteString(event.Get("delta.arguments").String())
			case "step.stop":
				stops[index]++
			case "interaction.completed":
				completed++
			}
		}
	}
	if len(ids) != maxDevinToolCalls {
		t.Errorf("stream tool calls = %d, want %d", len(ids), maxDevinToolCalls)
	}
	if len(stops) != maxDevinToolCalls {
		t.Errorf("stopped tool calls = %d, want %d", len(stops), maxDevinToolCalls)
	}
	if completed != 1 {
		t.Errorf("completion events = %d, want 1", completed)
	}
	for i := 0; i < maxDevinToolCalls; i++ {
		if want := fmt.Sprintf("call_%d", i); ids[i] != want {
			t.Errorf("step %d ID = %q, want %q", i, ids[i], want)
		}
		if stops[i] != 1 {
			t.Errorf("step %d stop events = %d, want 1", i, stops[i])
		}
		wantArgs := fmt.Sprintf(`{"accepted":%d}`, i)
		if i == 0 {
			wantArgs = `{"keep":true}`
		}
		if args[i] == nil || args[i].String() != wantArgs {
			t.Errorf("step %d arguments = %v, want %q", i, args[i], wantArgs)
		}
	}
}

func devinToolLimitFrames() []byte {
	var body bytes.Buffer
	appendCall := func(index int, id, arguments string) {
		var call []byte
		if id != "" {
			call = protowire.AppendTag(call, 1, protowire.BytesType)
			call = protowire.AppendString(call, id)
			call = protowire.AppendTag(call, 2, protowire.BytesType)
			call = protowire.AppendString(call, "tool")
		}
		call = protowire.AppendTag(call, 3, protowire.BytesType)
		call = protowire.AppendString(call, arguments)
		call = protowire.AppendTag(call, 4, protowire.VarintType)
		call = protowire.AppendVarint(call, uint64(index))
		frame := protowire.AppendTag(nil, 6, protowire.BytesType)
		frame = protowire.AppendBytes(frame, call)
		body.Write(helps.WrapConnectEnvelope(frame))
	}

	// Keep another accepted slot active while index zero exhausts the total limit.
	appendCall(1, "call_0", `{"keep":`)
	for i := 1; i < maxDevinToolCalls; i++ {
		appendCall(0, fmt.Sprintf("call_%d", i), fmt.Sprintf(`{"accepted":%d}`, i))
	}
	for i := maxDevinToolCalls; i < maxDevinToolCalls+3; i++ {
		appendCall(0, fmt.Sprintf("call_%d", i), `{"rejected":`)
		appendCall(0, "", `true}`)
	}
	// A rejected new slot must also ignore later unkeyed continuations.
	appendCall(2, "rejected_new_slot", `{"rejected":`)
	appendCall(2, "", `true}`)
	appendCall(1, "", `true}`)
	body.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))
	return body.Bytes()
}
