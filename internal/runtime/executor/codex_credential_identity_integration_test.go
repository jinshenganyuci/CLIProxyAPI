package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
	internalcodex "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

type codexCredentialIdentityCapture struct {
	prompt       string
	installation string
	session      string
	conversation string
	thread       string
	request      string
}

func TestCodexCredentialIdentityHTTPIntegrationAndConcurrency(t *testing.T) {
	var (
		capturesMu sync.Mutex
		captures   []codexCredentialIdentityCapture
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read upstream body: %v", errRead)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		capture := codexCredentialIdentityCapture{
			prompt:       gjson.GetBytes(body, "prompt_cache_key").String(),
			installation: gjson.GetBytes(body, "client_metadata.x-codex-installation-id").String(),
			session:      codexSessionHeaderValue(r.Header),
			conversation: headerValueCaseInsensitive(r.Header, "Conversation_id"),
			thread:       headerValueCaseInsensitive(r.Header, "Thread-Id"),
			request:      headerValueCaseInsensitive(r.Header, "X-Client-Request-Id"),
		}
		capturesMu.Lock()
		captures = append(captures, capture)
		capturesMu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Session-Id", capture.session)
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-5-codex\",\"output\":[],\"metadata\":{\"prompt_cache_key\":%q,\"x-codex-installation-id\":%q}}}\n\n", capture.prompt, capture.installation)
	}))
	defer server.Close()

	executor := NewCodexExecutor(codexCredentialIdentityTestConfig(false))
	authA := codexCredentialIdentityTestAuth("codex-a.json", "0d4c808d-8cf6-4cff-a0ca-c00639871678")
	authA.Attributes = map[string]string{"base_url": server.URL}
	authA.ProxyURL = "direct"
	authB := codexCredentialIdentityTestAuth("codex-b.json", "8ed0194b-6189-4378-a9ab-7cc1e835ec98")
	authB.Attributes = map[string]string{"base_url": server.URL}
	authB.ProxyURL = "direct"
	req := cliproxyexecutor.Request{
		Model: "gpt-5-codex",
		Payload: []byte(`{
          "model":"gpt-5-codex",
          "prompt_cache_key":"client-session",
          "input":"hello",
          "client_metadata":{"x-codex-installation-id":"client-install"}
        }`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Headers: http.Header{
			"Conversation_id":     {"client-session"},
			"Thread-Id":           {"client-thread"},
			"X-Client-Request-Id": {"client-request"},
		},
	}

	invoke := func(auth *cliproxyauth.Auth) cliproxyexecutor.Response {
		// The auth manager supplies an immutable clone to every execution attempt.
		response, errExecute := executor.Execute(context.Background(), auth.Clone(), req, opts)
		if errExecute != nil {
			t.Errorf("Execute(%s) error: %v", auth.ID, errExecute)
		}
		return response
	}
	firstResponse := invoke(authA)
	secondResponse := invoke(authA)
	otherResponse := invoke(authB)
	for _, response := range []cliproxyexecutor.Response{firstResponse, secondResponse, otherResponse} {
		if got := response.Headers.Get("Session-Id"); got != "client-session" {
			t.Fatalf("downstream Session-Id = %q, want original", got)
		}
		if got := gjson.GetBytes(response.Payload, "metadata.prompt_cache_key").String(); got != "client-session" {
			t.Fatalf("downstream payload prompt_cache_key = %q; payload=%s", got, response.Payload)
		}
		if got := gjson.GetBytes(response.Payload, "metadata.x-codex-installation-id").String(); got != "client-install" {
			t.Fatalf("downstream payload installation ID = %q; payload=%s", got, response.Payload)
		}
	}

	const concurrentRequests = 24
	var wait sync.WaitGroup
	wait.Add(concurrentRequests)
	for range concurrentRequests {
		go func() {
			defer wait.Done()
			_ = invoke(authA)
		}()
	}
	wait.Wait()

	capturesMu.Lock()
	snapshot := append([]codexCredentialIdentityCapture(nil), captures...)
	capturesMu.Unlock()
	if len(snapshot) != concurrentRequests+3 {
		t.Fatalf("upstream captures = %d, want %d", len(snapshot), concurrentRequests+3)
	}
	namespaceA, _, _ := internalcodex.ParseCredentialIdentity(authA.Metadata)
	wantSessionA := internalcodex.DeriveCredentialIdentity(namespaceA, "session", "client-session")
	wantInstallA := internalcodex.DeriveCredentialIdentity(namespaceA, "installation", "client-install")
	authACaptures := make([]codexCredentialIdentityCapture, 0, concurrentRequests+2)
	authACaptures = append(authACaptures, snapshot[:2]...)
	authACaptures = append(authACaptures, snapshot[3:]...)
	for index, capture := range authACaptures {
		if capture.prompt != wantSessionA || capture.session != wantSessionA || capture.conversation != wantSessionA {
			t.Fatalf("auth A capture %d session mapping = %+v, want %q", index, capture, wantSessionA)
		}
		if capture.installation != wantInstallA {
			t.Fatalf("auth A capture %d installation = %q, want %q", index, capture.installation, wantInstallA)
		}
		if capture.thread == "client-thread" || capture.request == "client-request" {
			t.Fatalf("auth A capture %d contains unmapped header: %+v", index, capture)
		}
	}
	if snapshot[2].prompt == wantSessionA || snapshot[2].installation == wantInstallA {
		t.Fatalf("different credential reused auth A identity: %+v", snapshot[2])
	}
}

func TestCodexCredentialIdentityCompactUsesSameSnapshot(t *testing.T) {
	var captured codexCredentialIdentityCapture
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses/compact" {
			t.Errorf("upstream path = %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		captured.prompt = gjson.GetBytes(body, "prompt_cache_key").String()
		captured.installation = gjson.GetBytes(body, "client_metadata.x-codex-installation-id").String()
		captured.session = codexSessionHeaderValue(r.Header)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Session-Id", captured.session)
		_, _ = fmt.Fprintf(w, `{"id":"resp_compact","object":"response.compaction","metadata":{"prompt_cache_key":%q,"x-codex-installation-id":%q}}`, captured.prompt, captured.installation)
	}))
	defer server.Close()

	auth := codexCredentialIdentityTestAuth("codex-compact.json", "6f3c61a1-f5bd-4265-93ed-a28612867322")
	auth.Attributes = map[string]string{"base_url": server.URL}
	auth.ProxyURL = "direct"
	response, errExecute := NewCodexExecutor(codexCredentialIdentityTestConfig(false)).Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model: "gpt-5-codex",
		Payload: []byte(`{
          "model":"gpt-5-codex",
          "prompt_cache_key":"compact-session",
          "client_metadata":{"x-codex-installation-id":"compact-install"},
          "input":[{"type":"message","role":"user","content":"compact"}]
        }`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Alt:          "responses/compact",
	})
	if errExecute != nil {
		t.Fatal(errExecute)
	}
	if captured.prompt == "compact-session" || captured.installation == "compact-install" {
		t.Fatalf("compact request was not mapped: %+v", captured)
	}
	if captured.prompt != captured.session {
		t.Fatalf("compact body/header snapshot diverged: %+v", captured)
	}
	if got := gjson.GetBytes(response.Payload, "metadata.prompt_cache_key").String(); got != "compact-session" {
		t.Fatalf("compact response prompt = %q; payload=%s", got, response.Payload)
	}
	if got := gjson.GetBytes(response.Payload, "metadata.x-codex-installation-id").String(); got != "compact-install" {
		t.Fatalf("compact response installation = %q; payload=%s", got, response.Payload)
	}
	if got := response.Headers.Get("Session-Id"); got != "compact-session" {
		t.Fatalf("compact response Session-Id = %q", got)
	}
}

func TestCodexCredentialIdentityHTTPStreamIntegration(t *testing.T) {
	captured := make(chan codexCredentialIdentityCapture, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read upstream body: %v", errRead)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		capture := codexCredentialIdentityCapture{
			prompt:       gjson.GetBytes(body, "prompt_cache_key").String(),
			installation: gjson.GetBytes(body, "client_metadata.x-codex-installation-id").String(),
			session:      codexSessionHeaderValue(r.Header),
		}
		captured <- capture
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Session-Id", capture.session)
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_stream\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-5-codex\",\"output\":[],\"metadata\":{\"prompt_cache_key\":%q,\"x-codex-installation-id\":%q}}}\n\n", capture.prompt, capture.installation)
	}))
	defer server.Close()

	auth := codexCredentialIdentityTestAuth("codex-stream.json", "e2676468-960a-4d2b-b4d0-6304d3f83535")
	auth.Attributes = map[string]string{"base_url": server.URL}
	auth.ProxyURL = "direct"
	result, errExecute := NewCodexExecutor(codexCredentialIdentityTestConfig(false)).ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"model":"gpt-5-codex","prompt_cache_key":"stream-session","input":"hello","client_metadata":{"x-codex-installation-id":"stream-install"}}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
	})
	if errExecute != nil {
		t.Fatal(errExecute)
	}
	if got := result.Headers.Get("Session-Id"); got != "stream-session" {
		t.Fatalf("downstream stream Session-Id = %q", got)
	}
	var downstream bytes.Buffer
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		downstream.Write(chunk.Payload)
	}
	if !bytes.Contains(downstream.Bytes(), []byte(`"prompt_cache_key":"stream-session"`)) ||
		!bytes.Contains(downstream.Bytes(), []byte(`"x-codex-installation-id":"stream-install"`)) {
		t.Fatalf("stream response did not restore client identities: %s", downstream.Bytes())
	}
	capture := <-captured
	if capture.prompt == "stream-session" || capture.installation == "stream-install" || capture.session == "stream-session" {
		t.Fatalf("stream request reached upstream without credential mapping: %+v", capture)
	}
	if capture.prompt != capture.session {
		t.Fatalf("stream body/header mapping diverged: %+v", capture)
	}
}

func TestCodexCredentialIdentityWebsocketPathsIntegration(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "execute"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			captured := make(chan codexCredentialIdentityCapture, 1)
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				responseHeaders := http.Header{"Session-Id": {codexSessionHeaderValue(r.Header)}}
				conn, errUpgrade := upgrader.Upgrade(w, r, responseHeaders)
				if errUpgrade != nil {
					t.Errorf("upgrade websocket: %v", errUpgrade)
					return
				}
				defer func() { _ = conn.Close() }()
				_, body, errRead := conn.ReadMessage()
				if errRead != nil {
					t.Errorf("read websocket request: %v", errRead)
					return
				}
				capture := codexCredentialIdentityCapture{
					prompt:       gjson.GetBytes(body, "prompt_cache_key").String(),
					installation: gjson.GetBytes(body, "client_metadata.x-codex-installation-id").String(),
					session:      codexSessionHeaderValue(r.Header),
				}
				captured <- capture
				completed := []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_ws","object":"response","status":"completed","model":"gpt-5-codex","output":[],"metadata":{"prompt_cache_key":%q,"x-codex-installation-id":%q},"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`, capture.prompt, capture.installation))
				if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
					t.Errorf("write websocket response: %v", errWrite)
				}
			}))
			defer server.Close()

			auth := codexCredentialIdentityTestAuth("codex-ws.json", "218e400e-10df-48a1-9dc3-7d86710a575e")
			auth.Attributes = map[string]string{"base_url": server.URL}
			auth.ProxyURL = "direct"
			req := cliproxyexecutor.Request{
				Model:   "gpt-5-codex",
				Payload: []byte(`{"model":"gpt-5-codex","prompt_cache_key":"ws-session","input":[{"role":"user","content":"hello"}],"client_metadata":{"x-codex-installation-id":"ws-install"}}`),
			}
			opts := cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatOpenAIResponse,
				ResponseFormat: sdktranslator.FormatOpenAIResponse,
			}
			executor := NewCodexWebsocketsExecutor(codexCredentialIdentityTestConfig(false))
			if stream {
				result, errExecute := executor.ExecuteStream(context.Background(), auth, req, opts)
				if errExecute != nil {
					t.Fatal(errExecute)
				}
				if got := result.Headers.Get("Session-Id"); got != "ws-session" {
					t.Fatalf("downstream websocket Session-Id = %q", got)
				}
				var downstream bytes.Buffer
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
					downstream.Write(chunk.Payload)
				}
				if !bytes.Contains(downstream.Bytes(), []byte(`"prompt_cache_key":"ws-session"`)) ||
					!bytes.Contains(downstream.Bytes(), []byte(`"x-codex-installation-id":"ws-install"`)) {
					t.Fatalf("websocket stream response did not restore client identities: %s", downstream.Bytes())
				}
			} else {
				response, errExecute := executor.Execute(context.Background(), auth, req, opts)
				if errExecute != nil {
					t.Fatal(errExecute)
				}
				if got := gjson.GetBytes(response.Payload, "metadata.prompt_cache_key").String(); got != "ws-session" {
					t.Fatalf("websocket response prompt = %q; payload=%s", got, response.Payload)
				}
				if got := gjson.GetBytes(response.Payload, "metadata.x-codex-installation-id").String(); got != "ws-install" {
					t.Fatalf("websocket response installation = %q; payload=%s", got, response.Payload)
				}
			}

			capture := <-captured
			if capture.prompt == "ws-session" || capture.installation == "ws-install" || capture.session == "ws-session" {
				t.Fatalf("websocket request reached upstream without credential mapping: %+v", capture)
			}
			if capture.prompt != capture.session {
				t.Fatalf("websocket body/header mapping diverged: %+v", capture)
			}
		})
	}
}

func TestCodexCredentialIdentityPrettyJSONCompact(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mapped := gjson.GetBytes(body, "prompt_cache_key").String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, "{\n  \"id\": \"resp_compact\",\n  \"object\": \"response.compaction\",\n  \"metadata\": {\n    \"prompt_cache_key\": %q\n  }\n}\n", mapped)
	}))
	defer server.Close()
	auth := codexCredentialIdentityTestAuth("audit.json", "0bdfb02c-eaf4-4bea-a449-c66074b475aa")
	auth.Attributes = map[string]string{"base_url": server.URL}
	auth.ProxyURL = "direct"
	res, err := NewCodexExecutor(codexCredentialIdentityTestConfig(false)).Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "gpt-5-codex", Payload: []byte(`{"model":"gpt-5-codex","prompt_cache_key":"client-session","input":[]}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Alt: "responses/compact"})
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(res.Payload, "metadata.prompt_cache_key").String(); got != "client-session" {
		t.Fatalf("pretty JSON leaked mapped identity=%s payload=%s", got, res.Payload)
	}
}

func TestCodexCredentialIdentityWebsocketHandshakeError(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUpgradeRequired} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("status=%d/stream=%v", status, stream), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(status)
					_, _ = fmt.Fprintf(w, `{"error":{"message":"test rejection","session_id":%q}}`, codexSessionHeaderValue(r.Header))
				}))
				defer server.Close()
				auth := codexCredentialIdentityTestAuth("audit.json", "0bdfb02c-eaf4-4bea-a449-c66074b475aa")
				auth.Attributes = map[string]string{"base_url": server.URL}
				auth.ProxyURL = "direct"
				executor := NewCodexWebsocketsExecutor(codexCredentialIdentityTestConfig(false))
				req := cliproxyexecutor.Request{Model: "gpt-5-codex", Payload: []byte(`{"model":"gpt-5-codex","prompt_cache_key":"client-session","input":[]}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
				ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
				var errExecute error
				if stream {
					_, errExecute = executor.ExecuteStream(ctx, auth, req, opts)
				} else {
					_, errExecute = executor.Execute(ctx, auth, req, opts)
				}
				if errExecute == nil {
					t.Fatal("expected handshake failure")
				}
				if got := gjson.Get(errExecute.Error(), "error.session_id").String(); got != "client-session" {
					t.Fatalf("handshake error identity=%s error=%v", got, errExecute)
				}
			})
		}
	}
}

func TestCodexCredentialIdentityHTTPTerminalError(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"type\":\"invalid_request_error\",\"message\":\"test rejection\",\"session_id\":%q}}}\n\n", codexSessionHeaderValue(r.Header))
			}))
			defer server.Close()
			auth := codexCredentialIdentityTestAuth("audit.json", "0bdfb02c-eaf4-4bea-a449-c66074b475aa")
			auth.Attributes = map[string]string{"base_url": server.URL}
			auth.ProxyURL = "direct"
			executor := NewCodexExecutor(codexCredentialIdentityTestConfig(false))
			req := cliproxyexecutor.Request{Model: "gpt-5-codex", Payload: []byte(`{"model":"gpt-5-codex","prompt_cache_key":"client-session","input":[]}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
			var errExecute error
			if stream {
				var result *cliproxyexecutor.StreamResult
				result, errExecute = executor.ExecuteStream(context.Background(), auth, req, opts)
				if errExecute == nil {
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							errExecute = chunk.Err
						}
					}
				}
			} else {
				_, errExecute = executor.Execute(context.Background(), auth, req, opts)
			}
			if errExecute == nil {
				t.Fatal("expected terminal failure")
			}
			if got := gjson.Get(errExecute.Error(), "error.session_id").String(); got != "client-session" {
				t.Fatalf("terminal error identity=%s error=%v", got, errExecute)
			}
		})
	}
}

func TestCodexCredentialIdentityDirectImageResponse(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				mapped := gjson.GetBytes(body, "client_metadata.x-codex-installation-id").String()
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: {\"type\":\"image_generation.completed\",\"padding\":%q,\"installation_id\":%q}\n\n", string(bytes.Repeat([]byte("a"), 40*1024)), mapped)
				} else {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprintf(w, `{"data":[],"installation_id":%q}`, mapped)
				}
			}))
			defer server.Close()
			auth := codexCredentialIdentityTestAuth("audit.json", "0bdfb02c-eaf4-4bea-a449-c66074b475aa")
			auth.Attributes = map[string]string{"base_url": server.URL}
			auth.ProxyURL = "direct"
			e := NewCodexExecutor(codexCredentialIdentityTestConfig(false))
			req := cliproxyexecutor.Request{Model: "gpt-image-1.5", Payload: []byte(`{"model":"gpt-image-1.5","prompt":"test","client_metadata":{"x-codex-installation-id":"client-install"}}`)}
			opts := codexOpenAIImageTestOptions(codexImagesGenerationsPath, stream)
			var result []byte
			if stream {
				res, err := e.ExecuteStream(context.Background(), auth, req, opts)
				if err != nil {
					t.Fatal(err)
				}
				for chunk := range res.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
					result = append(result, chunk.Payload...)
				}
			} else {
				res, err := e.Execute(context.Background(), auth, req, opts)
				if err != nil {
					t.Fatal(err)
				}
				result = res.Payload
			}
			if !bytes.Contains(result, []byte(`"installation_id":"client-install"`)) {
				t.Fatalf("image response identity not restored: %s", result)
			}
		})
	}
}
