package translator

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	antigravity "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/antigravity/openai/responses"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	gemini "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/openai/responses"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestUpgrade820DirectAndPipelineAgreeOnUnsupportedUserTurns(t *testing.T) {
	var networkHits atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { networkHits.Add(1); w.WriteHeader(http.StatusTeapot) }))
	defer origin.Close()
	for _, target := range []struct {
		name, path string
		format     sdktranslator.Format
		convert    sdktranslator.RequestTransform
	}{
		{"gemini", "contents", sdktranslator.FormatGemini, gemini.ConvertOpenAIResponsesRequestToGemini},
		{"antigravity", "request.contents", sdktranslator.FormatAntigravity, antigravity.ConvertOpenAIResponsesRequestToAntigravity},
	} {
		t.Run(target.name, func(t *testing.T) {
			for _, test := range []struct {
				name, raw string
				wantError bool
			}{
				{"file ID only", `{"input":[{"role":"user","content":[{"type":"input_file","file_id":"file-fixture"}]}]}`, true},
				{"instructions and later text cannot rescue", `{"instructions":"instruction","input":[{"role":"developer","content":"instruction"},{"role":"user","content":[{"type":"input_file","file_id":"file-fixture"}]},{"role":"assistant","content":"history"},{"role":"user","content":"later text"}]}`, true},
				{"whitespace cannot rescue", `{"input":[{"role":"user","content":[{"type":"input_file","file_id":"file-fixture"},{"type":"input_text","text":" \n\t "}]}]}`, true},
				{"same turn keeps sendable text", `{"input":[{"role":"user","content":[{"type":"input_file","file_id":"file-fixture"},{"type":"input_text","text":"keep"}]}]}`, false},
				{"file URL represented without fetching", fmt.Sprintf(`{"input":[{"role":"user","content":[{"type":"input_file","file_url":%q}]}]}`, origin.URL+"/document.pdf"), false},
			} {
				for _, stream := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/stream_%t", test.name, stream), func(t *testing.T) {
						raw := []byte(test.raw)
						before := bytes.Clone(raw)
						body, directErr := target.convert("gemini-test", raw, stream)
						r := sdktranslator.NewRegistry()
						r.Register(sdktranslator.FormatOpenAIResponse, target.format, target.convert, sdktranslator.ResponseTransform{})
						env, err := sdktranslator.NewPipeline(r).TranslateRequest(t.Context(), sdktranslator.FormatOpenAIResponse, target.format, sdktranslator.RequestEnvelope{Model: "gemini-test", Stream: stream, Body: raw})
						for name, got := range map[string]error{"direct": directErr, "pipeline": err, "envelope": env.Err} {
							if !test.wantError {
								if got != nil {
									t.Fatalf("%s rejected a representable turn: %v", name, got)
								}
								continue
							}
							var unsupported *translatorcommon.UnsupportedPartError
							if !errors.As(got, &unsupported) || unsupported.Type != "input_file" || unsupported.StatusCode() != 400 || !unsupported.IsRequestScoped() {
								t.Errorf("%s lost the request-scoped refusal: %v", name, got)
							}
						}
						if !gjson.ValidBytes(body) || !gjson.ValidBytes(env.Body) || !bytes.Equal(raw, before) {
							t.Fatal("translation changed input or lost diagnostic JSON")
						}
						if !test.wantError {
							parts := gjson.GetBytes(env.Body, target.path+".0.parts").Array()
							if len(parts) != 1 {
								t.Fatalf("sendable turn contents changed: %s", env.Body)
							}
							if test.name == "same turn keeps sendable text" && parts[0].Get("text").String() != "keep" {
								t.Fatal("sendable text was removed")
							}
							if test.name == "file URL represented without fetching" && !bytes.Contains(env.Body, []byte(origin.URL+"/document.pdf")) {
								t.Fatal("representable attachment was removed")
							}
						}
						if networkHits.Load() != 0 {
							t.Fatal("translator fetched a remote document")
						}
					})
				}
			}
		})
	}
}
