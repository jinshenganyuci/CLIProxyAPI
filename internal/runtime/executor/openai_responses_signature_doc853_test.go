package executor

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
)

func TestResponsesSignatureDocRestoresCommentaryWithoutOtherEdits(t *testing.T) {
	raw := []byte(`{"input":[{"type":"reasoning","encrypted_content":"cpa:commentary:v1","summary":[{"type":"summary_text","text":"Inspecting files."}]}],"sequence":9007199254740993}`)
	original := bytes.Clone(raw)
	doc := ojson.NewDoc(raw)
	if errSet := doc.Set("prompt_cache_key", "pending-session"); errSet != nil {
		t.Fatal(errSet)
	}
	if !sanitizeOpenAIResponsesReasoningEncryptedContentDoc(context.Background(), "test", doc) {
		t.Fatal("commentary restoration was not reported as a document change")
	}
	if got := doc.Get("input.0.phase").String(); got != "commentary" {
		t.Fatalf("commentary phase = %q", got)
	}
	if got := doc.Get("input.0.content.0.text").String(); got != "Inspecting files." {
		t.Fatalf("commentary text = %q", got)
	}
	if sanitizeOpenAIResponsesReasoningEncryptedContentDoc(context.Background(), "test", doc) {
		t.Fatal("restored commentary is not idempotent")
	}
	if errSet := doc.Set("service_tier", "priority"); errSet != nil {
		t.Fatal(errSet)
	}
	out := doc.Bytes()
	if ojson.GetBytes(out, "sequence").Raw != "9007199254740993" || ojson.GetBytes(out, "prompt_cache_key").String() != "pending-session" || ojson.GetBytes(out, "service_tier").String() != "priority" {
		t.Fatalf("document stages lost unrelated fields: %s", out)
	}
	if !bytes.Equal(raw, original) {
		t.Fatal("document mutation changed the caller's payload")
	}
}

func TestResponsesSignatureDocRestoresCommentaryAndKeepsOpaqueRoutingPolicy(t *testing.T) {
	suffix := "." + strings.Repeat("a", 67)
	ciphertext := make([]byte, 96)
	for i := range ciphertext {
		ciphertext[i] = byte(i*37 + 11)
	}
	opaque := base64.RawStdEncoding.EncodeToString(ciphertext)
	if _, errInspect := signature.InspectGrokEncryptedContent(opaque + suffix); errInspect != nil {
		t.Fatal(errInspect)
	}
	if signature.DetectSignatureProvider(opaque+suffix) != signature.SignatureProviderUnknown {
		t.Fatal("fixture is not an opaque signature")
	}
	for _, target := range []signature.SignatureProvider{signature.SignatureProviderGPT, signature.SignatureProviderClaude} {
		for _, routed := range []bool{false, true} {
			doc := ojson.NewDocString(`{"input":[{"type":"reasoning","encrypted_content":"cpa:commentary:v1","summary":[{"type":"summary_text","text":"Checking."}]},{"type":"reasoning","id":"rs_opaque","encrypted_content":"` + opaque + suffix + `","summary":[]}]}`)
			if !sanitizeOpenAIResponsesReasoningEncryptedContentWithOptionsDoc(context.Background(), "test", doc, true, true, metaSignatureForeign, target, routed) {
				t.Fatalf("target %s routed %t did not restore commentary", target, routed)
			}
			want := opaque
			if routed {
				want += suffix
			}
			if got := doc.Get("input.1.encrypted_content").String(); got != want {
				t.Fatalf("target %s routed %t signature = %q, want %q", target, routed, got, want)
			}
			if doc.Get("input.0.type").String() != "message" || doc.Get("input.1.id").String() != "rs_opaque" {
				t.Fatal("combined commentary and signature mutation lost replay semantics")
			}
		}
	}
}
