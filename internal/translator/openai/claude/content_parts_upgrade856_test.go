package claude

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestV856TypedThinkingPartsRemainSequentialAroundOpenTool(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"chatcmpl-v856","model":"fixture","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_precise","type":"function","function":{"name":"lookup","arguments":"{\"n\":"}}]},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{"content":[{"type":"thinking","thinking":[{"type":"text","text":"first <&>"}]},{"type":"text","text":"answer"},{"type":"reasoning","reasoning_content":[{"text":"second"}]}]},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"9007199254740993,\"s\":\"<tag>&\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`,
	)
	assertSequentialContentBlocks(t, events)
	var starts, thoughts, texts, arguments []string
	for _, event := range events {
		switch event.Type {
		case "content_block_start":
			starts = append(starts, ojson.Get(event.Payload, "content_block.type").String())
		case "content_block_delta":
			switch ojson.Get(event.Payload, "delta.type").String() {
			case "thinking_delta":
				thoughts = append(thoughts, ojson.Get(event.Payload, "delta.thinking").String())
			case "text_delta":
				texts = append(texts, ojson.Get(event.Payload, "delta.text").String())
			case "input_json_delta":
				arguments = append(arguments, ojson.Get(event.Payload, "delta.partial_json").String())
			}
		}
	}
	if got := strings.Join(starts, ","); got != "tool_use,thinking,text,thinking" {
		t.Fatalf("block order = %q", got)
	}
	if strings.Join(thoughts, "|") != "first <&>|second" || strings.Join(texts, "") != "answer" {
		t.Fatalf("typed content changed: thoughts=%q texts=%q", thoughts, texts)
	}
	args := strings.Join(arguments, "")
	if ojson.Get(args, "n").Raw != "9007199254740993" || ojson.Get(args, "s").String() != "<tag>&" {
		t.Fatalf("tool arguments lost precision or text: %s", args)
	}
	if countByType(events, "message_stop") != 1 || lastStopReason(events) != "tool_use" {
		t.Fatalf("wrong terminal state: %+v", events)
	}
}

func TestV856NonStreamTypedThinkingAndInlineToolsPreserveInput(t *testing.T) {
	body := []byte(`{"id":"chatcmpl-v856","model":"fixture","choices":[{"index":0,"message":{"role":"assistant","reasoning_content":"prefix","content":[{"type":"thinking","thinking":[{"text":"nested <&>"}]},{"type":"text","text":"before"},{"type":"tool_calls","tool_calls":[{"id":"call_precise","type":"function","function":{"name":"lookup","arguments":"{\"n\":9007199254740993,\"s\":\"<tag>&\"}"}}]},{"type":"reasoning","text":"after"}]},"finish_reason":"tool_calls"}]}`)
	before := bytes.Clone(body)
	out := ConvertOpenAIResponseToClaudeNonStream(context.Background(), "", nil, nil, body, nil)
	if !bytes.Equal(body, before) {
		t.Fatal("translator changed the caller response buffer")
	}
	if got := ojson.GetBytes(out, "content.#.type").Raw; got != `["thinking","thinking","text","tool_use","thinking"]` {
		t.Fatalf("block order = %s; response=%s", got, out)
	}
	if ojson.GetBytes(out, "content.1.thinking").String() != "nested <&>" || ojson.GetBytes(out, "content.3.input.n").Raw != "9007199254740993" || ojson.GetBytes(out, "content.3.input.s").String() != "<tag>&" {
		t.Fatalf("typed content or exact tool input changed: %s", out)
	}
	if ojson.GetBytes(out, "stop_reason").String() != "tool_use" {
		t.Fatalf("stop reason = %s", out)
	}
}
