package pluginhost

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestV844OAuthCallbackProxyDoesNotReplaceQuotaCredentialClient(t *testing.T) {
	origin, originCalls := nativeQuotaRouteServer(t, "origin")
	session, sessionCalls := nativeQuotaRouteServer(t, "session")
	selected, selectedCalls := nativeQuotaRouteServer(t, "selected")
	host := New()
	host.runtimeConfig = &config.Config{Codex: config.CodexConfig{CredentialProxyPolicy: "require"}}
	ctx := WithOAuthProxyURL(withHostCallbackPluginID(context.Background(), "v844"), session.URL)
	request, _ := json.Marshal(pluginapi.HTTPRequest{URL: origin.URL})
	response, err := host.callHostHTTPDo(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeRPCEnvelope[pluginapi.HTTPResponse](response)
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded.Body) != "session" || sessionCalls.Load() != 1 || originCalls.Load() != 0 {
		t.Fatal("OAuth callback bypassed session proxy")
	}
	bound := host.newHTTPClient(&coreauth.Auth{Provider: "codex", ProxyURL: selected.URL})
	ctx = context.WithValue(ctx, quotaCallbackHTTPClientKey{}, bound)
	response, err = host.callHostHTTPDo(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err = decodeRPCEnvelope[pluginapi.HTTPResponse](response)
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded.Body) != "selected" || selectedCalls.Load() != 1 || sessionCalls.Load() != 1 || originCalls.Load() != 0 {
		t.Fatal("session override replaced credential-bound quota client")
	}
}

func TestV844Schema7BodyOptOutPreservesCallerScopeAndReplaySkip(t *testing.T) {
	calls := map[string]int{}
	makeRecord := func(id string, schema uint32) capabilityRecord {
		return capabilityRecord{id: id, plugin: pluginapi.Plugin{SchemaVersion: schema, Capabilities: pluginapi.Capabilities{
			RequestInterceptorOmitBodyBefore: true, RequestInterceptorOmitBodyAfter: true,
			RequestInterceptor: requestInterceptorFunc(func(_ context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
				calls[id]++
				if req.CallerScope != "caller-isolated" || req.Metadata[pluginapi.CallerScopeMetadataKey] != "caller-isolated" {
					t.Error("caller scope lost")
				}
				if schema >= 7 && len(req.Body) != 0 || schema < 7 && string(req.Body) != "original" {
					t.Error("body schema compatibility lost")
				}
				req.Metadata[pluginapi.CallerScopeMetadataKey] = "must-not-escape"
				return pluginapi.RequestInterceptResponse{}, nil
			}),
		}}}
	}
	host := newHostWithRecords(makeRecord("modern", pluginabi.SchemaVersionRequestInterceptorOmitBody), makeRecord("legacy", 6), makeRecord("replay-origin", 7))
	req := pluginapi.RequestInterceptRequest{Body: []byte("original"), Metadata: map[string]any{pluginapi.CallerScopeMetadataKey: "caller-isolated"}}
	host.InterceptRequestBeforeAuthExcept(context.Background(), req, "replay-origin")
	host.InterceptRequestAfterAuthExcept(context.Background(), req, "replay-origin")
	if calls["modern"] != 2 || calls["legacy"] != 2 || calls["replay-origin"] != 0 || req.Metadata[pluginapi.CallerScopeMetadataKey] != "caller-isolated" || string(req.Body) != "original" {
		t.Fatal("opt-out corrupted replay isolation or caller request")
	}
}
