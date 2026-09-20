package responses_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	antigravity "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/antigravity/openai/responses"
	gemini "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/gemini/openai/responses"
	"github.com/tidwall/gjson"
)

func TestMultimodalUpgrade739RemoteMediaHasNoLocalFetch(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.WriteHeader(http.StatusTeapot) }))
	defer server.Close()
	for _, target := range []struct {
		name, contents string
		convert        func(string, []byte, bool) []byte
	}{
		{"gemini", "contents", gemini.ConvertOpenAIResponsesRequestToGemini},
		{"antigravity", "request.contents", antigravity.ConvertOpenAIResponsesRequestToAntigravity},
	} {
		t.Run(target.name, func(t *testing.T) {
			for _, shape := range []string{"message", "tool-array", "tool-object"} {
				t.Run(shape, func(t *testing.T) {
					toolResult := shape != "message"
					media := fmt.Sprintf(`[{"type":"input_audio","audio_url":%q},{"type":"input_video","video_url":%q},{"type":"input_file","file_url":%q}]`, server.URL+"/audio.mp3", server.URL+"/video.mp4", server.URL+"/report.pdf")
					wanted := map[string]string{"/audio.mp3": "audio/mpeg", "/video.mp4": "video/mp4", "/report.pdf": "application/pdf"}
					if shape == "tool-object" {
						media = fmt.Sprintf(`{"type":"input_file","file_url":%q}`, server.URL+"/report.pdf")
						wanted = map[string]string{"/report.pdf": "application/pdf"}
					}
					input := `{"input":[{"role":"user","content":` + media + `}]}`
					if toolResult {
						input = `{"input":[{"type":"function_call","call_id":"call_media","name":"read_media","arguments":"{}"},{"type":"function_call_output","call_id":"call_media","output":` + media + `}],"tools":[{"type":"function","name":"read_media","parameters":{"type":"object"}}]}`
					}
					out := target.convert("gemini-3-flash", []byte(input), false)
					observed := make(map[string]string)
					collect := func(part gjson.Result) {
						file := part.Get("fileData")
						if !file.Exists() {
							file = part.Get("file_data")
						}
						if !file.Exists() {
							return
						}
						uri := file.Get("fileUri").String()
						if uri == "" {
							uri = file.Get("file_uri").String()
						}
						mime := file.Get("mimeType").String()
						if mime == "" {
							mime = file.Get("mime_type").String()
						}
						observed[uri] = mime
					}
					for _, content := range gjson.GetBytes(out, target.contents).Array() {
						for _, part := range content.Get("parts").Array() {
							if toolResult {
								for _, mediaPart := range part.Get("functionResponse.parts").Array() {
									collect(mediaPart)
								}
							} else {
								collect(part)
							}
						}
					}
					for path, mime := range wanted {
						if observed[server.URL+path] != mime {
							t.Errorf("missing media %s MIME %s: %s", path, mime, out)
						}
					}
					if hits.Load() != 0 {
						t.Fatalf("translator fetched a remote media URI %d times", hits.Load())
					}
				})
			}
		})
	}
}

func TestMultimodalUpgrade739UnknownBlocksDoNotBecomeFiles(t *testing.T) {
	input := []byte(`{"input":[{"role":"user","content":[{"type":"input_text","text":"keep"},{"type":"vendor_private","url":"https://example.test/private.pdf"}]}]}`)
	for _, convert := range []func(string, []byte, bool) []byte{gemini.ConvertOpenAIResponsesRequestToGemini, antigravity.ConvertOpenAIResponsesRequestToAntigravity} {
		out := convert("gemini-3-flash", input, false)
		if gjson.GetBytes(out, "contents.0.parts.#").Int() > 1 || gjson.GetBytes(out, "request.contents.0.parts.#").Int() > 1 {
			t.Fatalf("unknown content was reinterpreted as a document: %s", out)
		}
	}
}
