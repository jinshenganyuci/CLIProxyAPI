package pluginhost

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestV842PluginThinkingPayloadRedirectPreservesCredentialLease(t *testing.T) {
	const provider = "v842-plugin"
	applier := &recordingThinkingApplier{provider: provider}
	record := normalizeTestCapabilityRecord(capabilityRecord{id: "v842-boundary", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{ThinkingApplier: applier}}})
	host := newHostWithRecords(record)
	host.refreshThinkingProviders([]capabilityRecord{record})
	t.Cleanup(thinking.ClearPluginProviders)
	host.modelRegistrations[record.id] = pluginModelRegistration{pluginID: record.id, provider: provider, models: []*registry.ModelInfo{{ID: "v842-model", Thinking: &registry.ThinkingSupport{Levels: []string{"low", "high"}}}}, hasExecutor: true}
	type observation struct {
		headers http.Header
		body    []byte
	}
	firstSeen, lastSeen := make(chan observation, 2), make(chan observation, 2)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		lastSeen <- observation{r.Header.Clone(), b}
		_, _ = w.Write([]byte(`{"usage":{"prompt_tokens":11}}`))
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		firstSeen <- observation{r.Header.Clone(), b}
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	host.runtimeConfig = &config.Config{SDKConfig: config.SDKConfig{ProxyURL: "http://global-canary.invalid:8080"}, Payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "v842-model", Protocol: "openai"}}, Params: map[string]any{"thinking_level": "payload-wins", "max_tokens": 64}}}, Filter: []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "v842-model", Protocol: "openai"}}, Params: []string{"applied_by_plugin"}}}}}
	manager := coreauth.NewManager(nil, nil, nil)
	auth, err := manager.Register(coreauth.WithSkipPersist(context.Background()), &coreauth.Auth{ID: "v842-auth", Provider: provider, ProxyURL: "direct", Metadata: map[string]any{"concurrency_limit": 3, "codex_identity_namespace": "981bd5bd-1ad8-4eef-88f8-5f0ec7cb1df7", "access_token": "synthetic"}})
	if err != nil {
		t.Fatal(err)
	}
	release, err := manager.AcquireCredentialConcurrency(context.Background(), auth)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	execute := func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
		resp, err := req.HTTPClient.Do(ctx, pluginapi.HTTPRequest{Method: http.MethodPost, URL: origin.URL, Headers: http.Header{"Authorization": {"Bearer synthetic"}, "X-API-Key": {"synthetic-key"}, "X-Trace": {"keep"}}, Body: req.Payload})
		return pluginapi.ExecutorResponse{Payload: resp.Body, Headers: resp.Headers}, err
	}
	adapter := newExecutorAdapterForRecordForTest(host, record, &fakeExecutor{identifier: provider, execute: execute, countTokens: execute}, []sdktranslator.Format{sdktranslator.FormatOpenAI}, []sdktranslator.Format{sdktranslator.FormatOpenAI})
	adapter.provider = provider
	for _, mode := range []string{"execute", "count"} {
		t.Run(mode, func(t *testing.T) {
			req := coreexecutor.Request{Model: "v842-model(high)", Format: sdktranslator.FormatOpenAI, Payload: []byte(`{"model":"v842-model(high)","reasoning_effort":"low","messages":[{"role":"user","content":"hi"}]}`)}
			opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, ResponseFormat: sdktranslator.FormatOpenAI}
			var err error
			if mode == "count" {
				opts.ResponseFormat = sdktranslator.FormatClaude
				var result coreexecutor.Response
				result, err = adapter.CountTokens(context.Background(), auth, req, opts)
				if err == nil && string(result.Payload) != `{"input_tokens":11}` {
					t.Fatalf("count response was wrapped as a completion: %s", result.Payload)
				}
			} else {
				_, err = adapter.Execute(context.Background(), auth, req, opts)
			}
			if err != nil {
				t.Fatal(err)
			}
			first, last := <-firstSeen, <-lastSeen
			if first.headers.Get("Authorization") == "" || last.headers.Get("Authorization") != "" || last.headers.Get("X-API-Key") != "" || last.headers.Get("X-Trace") != "keep" {
				t.Fatal("cross-origin redirect credential policy changed")
			}
			if !reflect.DeepEqual(first.body, last.body) || gjson.GetBytes(last.body, "thinking_level").String() != "payload-wins" || gjson.GetBytes(last.body, "applied_by_plugin").Exists() || gjson.GetBytes(last.body, "max_tokens").Int() != 64 {
				t.Fatalf("final payload barrier did not survive plugin HTTP: %s", last.body)
			}
			if count, limit := manager.CredentialConcurrencySnapshot(auth.ID); count != 1 || limit != 3 {
				t.Fatal("plugin execution changed an existing lease")
			}
		})
	}
	if len(applier.calls) != 2 || applier.calls[0].Config.Level != "high" || applier.calls[1].Config.Level != "high" {
		t.Fatal("canonical suffix precedence did not precede payload rules")
	}
	current, _ := manager.GetByID(auth.ID)
	if current.ProxyURL != "direct" || !reflect.DeepEqual(current.Metadata, auth.Metadata) {
		t.Fatal("plugin request changed credential controls")
	}
	release()
	if count, _ := manager.CredentialConcurrencySnapshot(auth.ID); count != 0 {
		t.Fatal("preexisting lease did not release")
	}
}

func TestV842PluginRPCErrorKeepsMachineCodeAndRetryAfter(t *testing.T) {
	_, err := decodeEnvelopeResult[rpcEmptyResponse](pluginabi.Envelope{OK: false, Error: &pluginabi.Error{Code: "credential_limit_exceeded", Message: "synthetic", HTTPStatus: 429, RetryAfterSeconds: 7}})
	if err == nil {
		t.Fatal("missing plugin error")
	}
	code, ok := err.(interface{ ErrorCode() string })
	if !ok || code.ErrorCode() != "credential_limit_exceeded" {
		t.Fatal("native retry metadata removed fork machine code")
	}
	wait, ok := err.(interface{ RetryAfter() *time.Duration })
	if !ok || wait.RetryAfter() == nil || *wait.RetryAfter() != 7*time.Second || coreauth.SafeResponseHeaders(err).Get("Retry-After") != "7" {
		t.Fatal("fork machine code removed native retry metadata")
	}
}
