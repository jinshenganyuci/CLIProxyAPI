package multiagentv2

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/tidwall/gjson"
)

func TestUpgrade802CompatMetadataStrippingDoesNotRequireOptimization(t *testing.T) {
	payload := []byte(`{"metadata":{"author":"business owner"},"input":[{"type":"agent_message","author":"/root","recipient":"/root/task","internal_chat_message_metadata_passthrough":{"turn_id":"private"},"content":[{"type":"input_text","text":"task","author":"quoted source"}]},{"type":"function_call","call_id":"call-1","name":"deliver","arguments":"{\"author\":\"business owner\",\"recipient\":\"customer\"}"},{"type":"function_call_output","call_id":"call-1","output":"{\"recipient\":\"customer\"}"},{"type":"configuration_update","reasoning":{"effort":"high"}}]}`)
	for _, cfg := range []*config.Config{nil, {}} {
		out := RewriteCodexMultiAgentV2Input(context.Background(), http.Header{"User-Agent": {"custom-sdk"}}, payload, cfg, true)
		for _, field := range []string{"author", "recipient", "internal_chat_message_metadata_passthrough"} {
			if gjson.GetBytes(out, "input.0."+field).Exists() {
				t.Fatalf("compat metadata %s survived without optimization: %s", field, out)
			}
		}
		// Issue #6233 requires portable message/user input in compatibility mode,
		// even when multi-agent optimization is disabled or the config is absent.
		if gjson.GetBytes(out, "input.0.type").String() != "message" {
			t.Fatalf("compatibility mode did not convert the agent message: %s", out)
		}
		if gjson.GetBytes(out, "input.0.role").String() != "user" {
			t.Fatalf("converted compatibility message did not retain the user role: %s", out)
		}
		for _, path := range []string{"metadata", "input.0.content", "input.1", "input.2", "input.3"} {
			if gjson.GetBytes(out, path).Raw != gjson.GetBytes(payload, path).Raw {
				t.Fatalf("compat stripping changed business data or configuration intent at %s: %s", path, out)
			}
		}
		if ordinary := RewriteCodexMultiAgentV2Input(context.Background(), nil, payload, cfg, false); !bytes.Equal(ordinary, payload) {
			t.Fatal("non-compat request changed with optimization disabled")
		}
	}
}
