package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestKimiAISelectedProxyCoversRuntimeProtocols(t *testing.T) {
	var globalHits, contextHits, selectedHits atomic.Int32
	global := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { globalHits.Add(1); w.WriteHeader(502) }))
	defer global.Close()
	selected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		selectedHits.Add(1)
		if r.URL.Host != "api.kimi.ai" || r.Header.Get("Authorization") != "Bearer selected-token" {
			t.Errorf("unexpected upstream host or credentials: %s", r.URL)
		}
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Error(errRead)
		}
		stream := strings.Contains(string(body), `"stream":true`)
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		switch r.URL.Path {
		case "/coding/v1/chat/completions":
			if stream {
				_, _ = io.WriteString(w, "data: {\"id\":\"chat\",\"model\":\"k3\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
			} else {
				_, _ = io.WriteString(w, `{"id":"chat","model":"k3","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
			}
		case "/coding/v1/responses":
			items := gjson.GetBytes(body, "input").Array()
			if len(items) != 5 || items[2].Get("call_id").String() != "first" || items[2].Get("output").String() != "one" || items[3].Get("call_id").String() != "second" || items[3].Get("output").String() != "two" || items[4].Get("role").String() != "developer" {
				t.Errorf("parallel tool outputs were not normalized on the selected proxy: %s", body)
			}
			if stream {
				_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp\",\"object\":\"response\",\"model\":\"k3\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
			} else {
				_, _ = io.WriteString(w, `{"id":"resp","object":"response","model":"k3","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)
			}
		case "/coding/v1/messages":
			if stream {
				_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg\",\"model\":\"k3\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			} else {
				_, _ = io.WriteString(w, `{"id":"msg","model":"k3","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
			}
		case "/coding/v1/messages/count_tokens":
			_, _ = io.WriteString(w, `{"input_tokens":1}`)
		case "/generic":
			_, _ = io.WriteString(w, `{}`)
		default:
			t.Errorf("unexpected upstream path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer selected.Close()
	exec := NewKimiExecutor(&config.Config{SDKConfig: config.SDKConfig{ProxyURL: global.URL}})
	auth := &cliproxyauth.Auth{ID: "kimi-ai-route", Provider: "kimi-ai", ProxyURL: selected.URL, Attributes: map[string]string{"domain": "kimi.ai", "base_url": "http://api.kimi.ai/coding/v1"}, Metadata: map[string]any{"access_token": "selected-token", "concurrency_limit": 3}}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", kimiRoundTripperFunc(func(*http.Request) (*http.Response, error) { contextHits.Add(1); return nil, io.ErrUnexpectedEOF }))
	for _, source := range []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude} {
		for _, stream := range []bool{false, true} {
			t.Run(source.String()+map[bool]string{false: "", true: "_stream"}[stream], func(t *testing.T) {
				payload := []byte(`{"model":"kimi-k3","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`)
				if source == sdktranslator.FormatOpenAIResponse {
					payload = []byte(`{"model":"kimi-k3","input":[{"type":"function_call","call_id":"first","name":"lookup","arguments":"{}"},{"type":"function_call","call_id":"second","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"first","output":"one"},{"type":"message","role":"developer","content":[{"type":"input_text","text":"keep instructions"}]},{"type":"function_call_output","call_id":"second","output":"two"}]}`)
				}
				req := cliproxyexecutor.Request{Model: "kimi-k3", Payload: payload}
				opts := cliproxyexecutor.Options{SourceFormat: source, Stream: stream}
				if stream {
					result, errExecute := exec.ExecuteStream(ctx, auth, req, opts)
					if errExecute != nil {
						t.Fatal(errExecute)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else {
					if _, errExecute := exec.Execute(ctx, auth, req, opts); errExecute != nil {
						t.Fatal(errExecute)
					}
				}
			})
		}
	}
	if _, errCount := exec.CountTokens(ctx, auth, cliproxyexecutor.Request{Model: "kimi-k3", Payload: []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":"hi"}]}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}); errCount != nil {
		t.Fatal(errCount)
	}
	req, _ := http.NewRequest(http.MethodGet, "http://api.kimi.ai/generic", nil)
	resp, errHTTP := exec.HttpRequest(ctx, auth, req)
	if errHTTP != nil {
		t.Fatal(errHTTP)
	}
	_ = resp.Body.Close()
	if selectedHits.Load() != 8 || globalHits.Load() != 0 || contextHits.Load() != 0 {
		t.Fatalf("runtime route counts selected=%d global=%d context=%d", selectedHits.Load(), globalHits.Load(), contextHits.Load())
	}
	if auth.Attributes["base_url"] != "http://api.kimi.ai/coding/v1" || auth.Metadata["concurrency_limit"] != 3 {
		t.Fatal("runtime mutated shared credential controls")
	}
}

func TestKimiAIInvalidProxyNeverUsesRuntimeFallback(t *testing.T) {
	var hits atomic.Int32
	canary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); _, _ = io.WriteString(w, `{}`) }))
	defer canary.Close()
	exec := NewKimiExecutor(&config.Config{SDKConfig: config.SDKConfig{ProxyURL: canary.URL}})
	auth := &cliproxyauth.Auth{Provider: "kimi-ai", ProxyURL: "invalid://user:secret@proxy.invalid", Attributes: map[string]string{"base_url": canary.URL}, Metadata: map[string]any{"access_token": "token", "refresh_token": "refresh"}}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", kimiRoundTripperFunc(func(*http.Request) (*http.Response, error) { hits.Add(1); return nil, io.ErrUnexpectedEOF }))
	for _, source := range []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude} {
		req := cliproxyexecutor.Request{Model: "kimi-k3", Payload: []byte(`{"model":"kimi-k3","max_tokens":32,"input":"hello","messages":[{"role":"user","content":"hello"}]}`)}
		opts := cliproxyexecutor.Options{SourceFormat: source}
		if _, errExecute := exec.Execute(ctx, auth, req, opts); errExecute == nil {
			t.Fatal("invalid proxy accepted Execute")
		}
		if _, errExecute := exec.ExecuteStream(ctx, auth, req, opts); errExecute == nil {
			t.Fatal("invalid proxy accepted ExecuteStream")
		}
	}
	if _, errRefresh := exec.Refresh(ctx, auth); errRefresh == nil {
		t.Fatal("invalid proxy accepted refresh")
	}
	if hits.Load() != 0 {
		t.Fatalf("runtime fallback reached canary %d times", hits.Load())
	}
}

func TestKimiConcurrentClaudeRequestsPreserveCredentialAttributes(t *testing.T) {
	exec := NewKimiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Provider: "kimi-ai", ProxyURL: "invalid://proxy", Attributes: map[string]string{"base_url": "https://api.kimi.ai/coding/v1"}, Metadata: map[string]any{"access_token": "token"}}
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = exec.CountTokens(context.Background(), auth, cliproxyexecutor.Request{Model: "kimi-k3", Payload: []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":"hi"}]}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
		}()
	}
	wg.Wait()
	if auth.Attributes["base_url"] != "https://api.kimi.ai/coding/v1" {
		t.Fatal("Claude adaptation overwrote configured base_url")
	}
}
