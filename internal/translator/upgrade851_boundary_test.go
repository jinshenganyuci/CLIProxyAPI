package translator

import (
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	geminiresponses "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/openai/responses"
	chatresponses "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/openai/openai/responses"
)

func TestUpgrade851DeveloperRolesSurviveCommentaryRestoration(t *testing.T) {
	input := []byte(`{"input":[{"type":"message","role":"user","content":"inspect"},{"type":"reasoning","encrypted_content":"cpa:commentary:v1","summary":[{"type":"summary_text","text":"Checking files."}]},{"type":"message","role":"developer","content":"Read only."},{"type":"message","role":"assistant","content":"Done."}]}`)
	for _, stream := range []bool{false, true} {
		out, err := chatresponses.ConvertOpenAIResponsesRequestToOpenAIChatCompletions("test-model", input, stream)
		if err != nil {
			t.Fatal(err)
		}
		messages := ojson.GetBytes(out, "messages").Array()
		wantRoles := []string{"user", "assistant", "developer", "assistant"}
		wantTexts := []string{"inspect", "Checking files.", "Read only.", "Done."}
		if len(messages) != len(wantRoles) || strings.Contains(string(out), "cpa:commentary:v1") {
			t.Fatalf("commentary carrier leaked or message order changed: %s", out)
		}
		for index, message := range messages {
			content := message.Get("content")
			if content.IsArray() {
				content = content.Get("0.text")
			}
			if message.Get("role").String() != wantRoles[index] || content.String() != wantTexts[index] {
				t.Fatalf("message %d lost its role or text: %s", index, out)
			}
		}
	}
}

func TestUpgrade851GeminiParallelRemoteMediaAcrossDeveloperNotice(t *testing.T) {
	// Remote media is a fork extension; the upstream batching fix must carry it
	// across notices without turning a URL into text or splitting tool results.
	input := []byte(`{"input":[{"type":"message","role":"user","content":"Inspect files."},{"type":"function_call","call_id":"first","name":"inspect","arguments":"{}"},{"type":"function_call","call_id":"second","name":"inspect","arguments":"{}"},{"type":"function_call_output","call_id":"second","output":[{"type":"input_text","text":"second-result"},{"type":"input_image","image_url":"https://example.invalid/second.png"}]},{"role":"developer","content":"Keep both images."},{"type":"function_call_output","call_id":"first","output":[{"type":"input_text","text":"first-result"},{"type":"input_image","image_url":"https://example.invalid/first.png"}]}]}`)
	out, err := geminiresponses.ConvertOpenAIResponsesRequestToGemini("gemini-test", input, false)
	if err != nil {
		t.Fatal(err)
	}
	if errValidate := signature.ValidateGeminiFunctionCallPairing(out); errValidate != nil {
		t.Fatalf("parallel remote media pairing failed: %v; output=%s", errValidate, out)
	}
	var resultTurns int
	for _, content := range ojson.GetBytes(out, "contents").Array() {
		parts := content.Get("parts").Array()
		if len(parts) == 0 || !parts[0].Get("functionResponse").Exists() {
			continue
		}
		resultTurns++
		if len(parts) != 2 {
			t.Fatalf("parallel results were split by the notice: %s", out)
		}
		for index, id := range []string{"first", "second"} {
			response := parts[index].Get("functionResponse")
			if response.Get("id").String() != id || response.Get("response.result").String() != id+"-result" || response.Get("parts.0.fileData.fileUri").String() != "https://example.invalid/"+id+".png" || response.Get("parts.0.fileData.mimeType").String() != "image/png" {
				t.Fatalf("ordered remote tool result %s changed: %s", id, out)
			}
		}
	}
	if resultTurns != 1 || strings.Count(string(out), "Keep both images.") != 1 {
		t.Fatalf("result batch or developer notice was lost or duplicated: %s", out)
	}
}
