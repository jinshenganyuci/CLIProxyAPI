package executor

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	interactionsclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/interactions/claude"
)

func devinImagesUpgrade737Parse(t *testing.T, original string, dropUserImages bool) []helps.DevinPrompt {
	t.Helper()
	translated := interactionsclaude.ConvertClaudeRequestToInteractions("devin/swe-2", []byte(original), false)
	if dropUserImages {
		var request map[string]any
		if err := json.Unmarshal(translated, &request); err != nil {
			t.Fatal(err)
		}
		for _, raw := range request["input"].([]any) {
			step := raw.(map[string]any)
			if step["type"] != "user_input" {
				continue
			}
			var retained []any
			for _, rawPart := range step["content"].([]any) {
				part := rawPart.(map[string]any)
				if part["type"] != "image" {
					retained = append(retained, part)
				}
			}
			step["content"] = retained
		}
		var err error
		translated, err = json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
	}
	_, prompts, _, _, _, _, _, _, _ := parseInteractionsPayload(translated, []byte(original))
	return prompts
}

func devinImagesUpgrade737AssertCaption(t *testing.T, prompts []helps.DevinPrompt, caption string, wantImages ...string) {
	t.Helper()
	matched := 0
	for _, prompt := range prompts {
		if !strings.Contains(prompt.Content, caption) {
			continue
		}
		matched++
		if len(prompt.Images) != len(wantImages) {
			t.Fatalf("caption %q has %d images, want %d: %+v", caption, len(prompt.Images), len(wantImages), prompt)
		}
		for i, image := range prompt.Images {
			if image.Base64Data != wantImages[i] || image.MimeType != "image/png" {
				t.Fatalf("caption %q image %d = %+v, want PNG %q", caption, i, image, wantImages[i])
			}
		}
	}
	if matched != 1 {
		t.Fatalf("caption %q matched %d prompts, want 1: %+v", caption, matched, prompts)
	}
}

func TestDevinImagesUpgrade737ToolOnlyUserDoesNotShiftLaterImages(t *testing.T) {
	for _, withCall := range []bool{false, true} {
		name := "orphan"
		if withCall {
			name = "matched"
		}
		t.Run(name, func(t *testing.T) {
			assistant := ""
			if withCall {
				assistant = `{"role":"assistant","content":[{"type":"tool_use","id":"tool_a","name":"read","input":{}}]},`
			}
			original := `{"messages":[` + assistant + `
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool_a","content":"tool result"}]},
				{"role":"assistant","content":"next"},
				{"role":"user","content":[{"type":"text","text":"picture turn"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1hZ2U="}}]},
				{"role":"assistant","content":"received picture"},
				{"role":"user","content":"text only turn"}
			]}`
			for _, dropImages := range []bool{false, true} {
				prompts := devinImagesUpgrade737Parse(t, original, dropImages)
				devinImagesUpgrade737AssertCaption(t, prompts, "tool result")
				devinImagesUpgrade737AssertCaption(t, prompts, "picture turn", "aW1hZ2U=")
				devinImagesUpgrade737AssertCaption(t, prompts, "text only turn")
			}
		})
	}
}

func TestDevinImagesUpgrade737MixedUserSegmentsKeepTheirImages(t *testing.T) {
	original := `{"messages":[
		{"role":"user","content":[
			{"type":"text","text":"before first tool"},
			{"type":"tool_result","tool_use_id":"tool_a","content":"first result"},
			{"type":"text","text":"middle picture segment"},
			{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1hZ2U="}},
			{"type":"tool_result","tool_use_id":"tool_b","content":"second result"},
			{"type":"text","text":"after second tool"}
		]},
		{"role":"assistant","content":"acknowledged"},
		{"role":"user","content":"unrelated later user"}
	]}`
	for _, dropImages := range []bool{false, true} {
		prompts := devinImagesUpgrade737Parse(t, original, dropImages)
		devinImagesUpgrade737AssertCaption(t, prompts, "before first tool")
		devinImagesUpgrade737AssertCaption(t, prompts, "first result")
		devinImagesUpgrade737AssertCaption(t, prompts, "middle picture segment", "aW1hZ2U=")
		devinImagesUpgrade737AssertCaption(t, prompts, "second result")
		devinImagesUpgrade737AssertCaption(t, prompts, "after second tool")
		devinImagesUpgrade737AssertCaption(t, prompts, "unrelated later user")
	}
}

func TestDevinImagesUpgrade737SystemReminderDoesNotConsumeUserImage(t *testing.T) {
	original := `{"messages":[
		{"role":"user","content":"initial user"},
		{"role":"assistant","content":"initial answer"},
		{"role":"system","content":"mid-session policy reminder"},
		{"role":"user","content":[{"type":"text","text":"next picture turn"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1hZ2U="}}]},
		{"role":"assistant","content":"picture answer"},
		{"role":"user","content":"final plain turn"}
	]}`
	for _, dropImages := range []bool{false, true} {
		prompts := devinImagesUpgrade737Parse(t, original, dropImages)
		devinImagesUpgrade737AssertCaption(t, prompts, "initial user")
		devinImagesUpgrade737AssertCaption(t, prompts, "mid-session policy reminder")
		devinImagesUpgrade737AssertCaption(t, prompts, "next picture turn", "aW1hZ2U=")
		devinImagesUpgrade737AssertCaption(t, prompts, "final plain turn")
	}
}

func TestDevinImagesUpgrade737AmbiguousCaptionsDoNotCrossAttach(t *testing.T) {
	original := `{"messages":[
		{"role":"user","content":[{"type":"text","text":"same caption"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1hZ2U="}}]},
		{"role":"assistant","content":"first answer"},
		{"role":"user","content":[{"type":"text","text":"same caption"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"b3RoZXI="}}]},
		{"role":"assistant","content":"second answer"},
		{"role":"user","content":"plain followup"}
	]}`
	prompts := devinImagesUpgrade737Parse(t, original, true)
	for _, prompt := range prompts {
		if len(prompt.Images) != 0 {
			t.Fatalf("ambiguous captions must not guess an original image: %+v", prompt)
		}
	}
	retained := devinImagesUpgrade737Parse(t, original, false)
	var actual []string
	for _, prompt := range retained {
		for _, image := range prompt.Images {
			actual = append(actual, image.Base64Data)
		}
	}
	if len(actual) != 2 || actual[0] != "aW1hZ2U=" || actual[1] != "b3RoZXI=" {
		t.Fatalf("already translated images must remain unchanged: %v", actual)
	}
	devinImagesUpgrade737AssertCaption(t, retained, "plain followup")
}
