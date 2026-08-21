package executor

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	internalcodex "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestCodexCredentialIdentityMapsEverySupportedBodyAndHeaderField(t *testing.T) {
	cfg := codexCredentialIdentityTestConfig(false)
	auth := codexCredentialIdentityTestAuth("auth-a.json", "0bdfb02c-eaf4-4bea-a449-c66074b475aa")
	body := []byte(`{
  "prompt_cache_key":"session-original",
  "client_metadata":{
    "x-codex-installation-id":"install-original",
    "x-codex-window-id":"window-original",
    "x-codex-turn-metadata":"{\"prompt_cache_key\":\"session-original\",\"turn_id\":\"turn-original\",\"window_id\":\"window-original\",\"untouched\":\"session-original\"}"
  }
}`)

	upstreamBody, state, errIdentity := applyCodexIdentityBody(cfg, auth, body, body)
	if errIdentity != nil {
		t.Fatal(errIdentity)
	}
	if !state.credentialIdentity || state.credentialSnapshot.AuthID != auth.ID || state.credentialSnapshot.ProxyURL != auth.ProxyURL {
		t.Fatalf("snapshot = %+v", state.credentialSnapshot)
	}

	mappedSession := state.forwardIdentities["session-original"]
	mappedInstall := state.forwardIdentities["install-original"]
	mappedWindow := state.forwardIdentities["window-original"]
	mappedTurn := state.forwardIdentities["turn-original"]
	for label, value := range map[string]string{
		"session": mappedSession,
		"install": mappedInstall,
		"window":  mappedWindow,
		"turn":    mappedTurn,
	} {
		if _, errParse := uuid.Parse(value); errParse != nil {
			t.Fatalf("%s mapped value %q is not UUID: %v", label, value, errParse)
		}
	}
	if got := gjson.GetBytes(upstreamBody, "prompt_cache_key").String(); got != mappedSession {
		t.Fatalf("body prompt_cache_key = %q, want %q", got, mappedSession)
	}
	if got := gjson.GetBytes(upstreamBody, "client_metadata.x-codex-installation-id").String(); got != mappedInstall {
		t.Fatalf("body installation = %q, want %q", got, mappedInstall)
	}
	if got := gjson.GetBytes(upstreamBody, "client_metadata.x-codex-window-id").String(); got != mappedWindow {
		t.Fatalf("body window = %q, want %q", got, mappedWindow)
	}
	turnMetadata := gjson.GetBytes(upstreamBody, "client_metadata.x-codex-turn-metadata").String()
	if got := gjson.Get(turnMetadata, "prompt_cache_key").String(); got != mappedSession {
		t.Fatalf("turn metadata prompt = %q, want %q", got, mappedSession)
	}
	if got := gjson.Get(turnMetadata, "turn_id").String(); got != mappedTurn {
		t.Fatalf("turn metadata turn = %q, want %q", got, mappedTurn)
	}
	if got := gjson.Get(turnMetadata, "window_id").String(); got != mappedWindow {
		t.Fatalf("turn metadata window = %q, want %q", got, mappedWindow)
	}
	if got := gjson.Get(turnMetadata, "untouched").String(); got != "session-original" {
		t.Fatalf("unknown turn metadata field changed to %q", got)
	}

	headers := http.Header{
		"Session-Id":            {"session-original"},
		"Conversation_id":       {"session-original"},
		"Thread-Id":             {"thread-original"},
		"X-Client-Request-Id":   {"request-original"},
		"X-Codex-Window-Id":     {"window-original"},
		"X-Codex-Turn-Metadata": {`{"prompt_cache_key":"session-original","turn_id":"turn-original","window_id":"window-original"}`},
	}
	applyCodexIdentityHeaders(headers, &state)
	if got := codexSessionHeaderValue(headers); got != mappedSession {
		t.Fatalf("Session-Id = %q, want %q", got, mappedSession)
	}
	if got := headerValueCaseInsensitive(headers, "Conversation_id"); got != mappedSession {
		t.Fatalf("Conversation_id = %q, want %q", got, mappedSession)
	}
	if got := headerValueCaseInsensitive(headers, "Thread-Id"); got != state.forwardIdentities["thread-original"] {
		t.Fatalf("Thread-Id = %q", got)
	}
	if got := headerValueCaseInsensitive(headers, "X-Client-Request-Id"); got != state.forwardIdentities["request-original"] {
		t.Fatalf("X-Client-Request-Id = %q", got)
	}
	if got := headerValueCaseInsensitive(headers, "X-Codex-Window-Id"); got != mappedWindow {
		t.Fatalf("X-Codex-Window-Id = %q, want %q", got, mappedWindow)
	}
}

func TestCodexCredentialIdentityInvariantsAcrossCredentialClientRestartAndRename(t *testing.T) {
	cfg := codexCredentialIdentityTestConfig(false)
	namespaceA := "52e6831f-bfd3-45f5-8948-c0f8a0f2d3e7"
	authA := codexCredentialIdentityTestAuth("codex-a.json", namespaceA)
	authARenamed := codexCredentialIdentityTestAuth("renamed-codex-a.json", namespaceA)
	authB := codexCredentialIdentityTestAuth("codex-b.json", "d5f47e30-8cf9-4b45-b240-146224e0b2cf")

	mapInstall := func(auth *cliproxyauth.Auth, original string) string {
		body := []byte(`{"client_metadata":{"x-codex-installation-id":"` + original + `"}}`)
		upstream, _, errIdentity := applyCodexIdentityBody(cfg, auth, body, body)
		if errIdentity != nil {
			t.Fatal(errIdentity)
		}
		return gjson.GetBytes(upstream, "client_metadata.x-codex-installation-id").String()
	}

	aClient1First := mapInstall(authA, "client-1")
	aClient1Restart := mapInstall(authA, "client-1")
	aClient1Renamed := mapInstall(authARenamed, "client-1")
	aClient2 := mapInstall(authA, "client-2")
	bClient1 := mapInstall(authB, "client-1")
	if aClient1First != aClient1Restart || aClient1First != aClient1Renamed {
		t.Fatalf("same credential/client was not stable: %q %q %q", aClient1First, aClient1Restart, aClient1Renamed)
	}
	if aClient1First == aClient2 {
		t.Fatal("different original client IDs mapped to one identity")
	}
	if aClient1First == bClient1 {
		t.Fatal("different credential namespaces mapped one client to the same identity")
	}
}

func TestCodexCredentialIdentitySameCredentialIgnoresDownstreamKeyAsNamespace(t *testing.T) {
	cfg := codexCredentialIdentityTestConfig(false)
	auth := codexCredentialIdentityTestAuth("codex-a.json", "69dbda5e-93f7-4a0d-b450-4cc0ccf30206")
	body := []byte(`{"prompt_cache_key":"client-session","client_metadata":{"x-codex-installation-id":"client-install"}}`)

	firstBody, firstState, firstErr := applyCodexIdentityBody(cfg, auth, body, body)
	secondBody, secondState, secondErr := applyCodexIdentityBody(cfg, auth, body, body)
	if firstErr != nil || secondErr != nil {
		t.Fatalf("identity errors = %v, %v", firstErr, secondErr)
	}
	// The downstream CPA API key is deliberately not an input to this function.
	// Four keys bound to this auth therefore share this credential namespace while
	// the original client installation/session IDs still remain distinct inputs.
	if !bytes.Equal(firstBody, secondBody) {
		t.Fatalf("same credential/client body changed across keys: %s vs %s", firstBody, secondBody)
	}
	if firstState.credentialSnapshot.Namespace != secondState.credentialSnapshot.Namespace {
		t.Fatal("same credential produced different namespace snapshots")
	}
}

func TestCodexCredentialIdentityPreservesCrossFieldEquality(t *testing.T) {
	cfg := codexCredentialIdentityTestConfig(false)
	auth := codexCredentialIdentityTestAuth("codex-a.json", "3cb59d69-5897-49ef-a82b-a4a47115a51e")
	body := []byte(`{"prompt_cache_key":"shared","client_metadata":{"x-codex-installation-id":"shared","x-codex-window-id":"shared","x-codex-turn-metadata":"{\"prompt_cache_key\":\"shared\",\"turn_id\":\"shared\",\"window_id\":\"shared\"}"}}`)
	upstream, _, errIdentity := applyCodexIdentityBody(cfg, auth, body, body)
	if errIdentity != nil {
		t.Fatal(errIdentity)
	}
	values := []string{
		gjson.GetBytes(upstream, "prompt_cache_key").String(),
		gjson.GetBytes(upstream, "client_metadata.x-codex-installation-id").String(),
		gjson.GetBytes(upstream, "client_metadata.x-codex-window-id").String(),
	}
	metadata := gjson.GetBytes(upstream, "client_metadata.x-codex-turn-metadata").String()
	values = append(values,
		gjson.Get(metadata, "prompt_cache_key").String(),
		gjson.Get(metadata, "turn_id").String(),
		gjson.Get(metadata, "window_id").String(),
	)
	for _, value := range values[1:] {
		if value != values[0] {
			t.Fatalf("equal original values did not remain equal after mapping: %v", values)
		}
	}
}

func TestCodexCredentialIdentityFailClosedAndSynthesisPolicy(t *testing.T) {
	cfg := codexCredentialIdentityTestConfig(false)
	missing := &cliproxyauth.Auth{ID: "missing.json", Provider: "codex", Metadata: map[string]any{"access_token": "secret"}}
	if _, _, errIdentity := applyCodexIdentityBody(cfg, missing, nil, []byte(`{}`)); errIdentity == nil || !strings.Contains(errIdentity.Error(), "initialize it") {
		t.Fatalf("missing namespace error = %v", errIdentity)
	}
	invalid := codexCredentialIdentityTestAuth("invalid.json", "not-a-uuid")
	if _, _, errIdentity := applyCodexIdentityBody(cfg, invalid, nil, []byte(`{}`)); errIdentity == nil {
		t.Fatal("invalid namespace did not fail closed")
	}
	conflict := codexCredentialIdentityTestAuth("conflict.json", "6af0cb17-177f-4328-ac47-06e368893b65")
	conflict.Attributes = map[string]string{internalcodex.CredentialIdentityConflictAttribute: "true"}
	if _, _, errIdentity := applyCodexIdentityBody(cfg, conflict, nil, []byte(`{}`)); errIdentity == nil || !strings.Contains(errIdentity.Error(), "conflicts") {
		t.Fatalf("conflicting namespace error = %v", errIdentity)
	}

	valid := codexCredentialIdentityTestAuth("valid.json", "9a351905-b916-4c33-ac32-fb510e9eb5ca")
	withoutSynthesis, _, errIdentity := applyCodexIdentityBody(cfg, valid, nil, []byte(`{"prompt_cache_key":"session"}`))
	if errIdentity != nil {
		t.Fatal(errIdentity)
	}
	if gjson.GetBytes(withoutSynthesis, "client_metadata.x-codex-installation-id").Exists() {
		t.Fatal("installation ID was synthesized while policy was false")
	}
	cfg.Codex.CredentialIdentity.SynthesizeMissingInstallationID = true
	withSynthesis, _, errIdentity := applyCodexIdentityBody(cfg, valid, nil, []byte(`{"prompt_cache_key":"session"}`))
	if errIdentity != nil {
		t.Fatal(errIdentity)
	}
	if got := gjson.GetBytes(withSynthesis, "client_metadata.x-codex-installation-id").String(); got == "" {
		t.Fatal("installation ID was not synthesized while policy was true")
	}
}

func TestCodexCredentialIdentityResponseReverseMappingIsStructured(t *testing.T) {
	cfg := codexCredentialIdentityTestConfig(false)
	auth := codexCredentialIdentityTestAuth("codex-a.json", "97174329-7101-4217-becd-6b7b4f5c1088")
	body := []byte(`{"prompt_cache_key":"session-original","client_metadata":{"x-codex-installation-id":"install-original"}}`)
	_, state, errIdentity := applyCodexIdentityBody(cfg, auth, body, body)
	if errIdentity != nil {
		t.Fatal(errIdentity)
	}
	mappedSession := state.forwardIdentities["session-original"]
	mappedInstall := state.forwardIdentities["install-original"]
	upstream := []byte(`data: {"type":"response.completed","response":{"metadata":{"prompt_cache_key":"` + mappedSession + `","x-codex-installation-id":"` + mappedInstall + `","note":"` + mappedSession + `"}}}` + "\n\n")
	client := applyCodexIdentityExposeResponsePayload(upstream, state)
	if !bytes.Contains(client, []byte(`"prompt_cache_key":"session-original"`)) {
		t.Fatalf("prompt cache key was not reversed: %s", client)
	}
	if !bytes.Contains(client, []byte(`"x-codex-installation-id":"install-original"`)) {
		t.Fatalf("installation ID was not reversed: %s", client)
	}
	if !bytes.Contains(client, []byte(`"note":"`+mappedSession+`"`)) {
		t.Fatalf("unrelated response string was globally replaced: %s", client)
	}

	headers := http.Header{"Session-Id": {mappedSession}, "X-Unrelated": {mappedSession}}
	exposed := exposeCodexIdentityHeaders(headers, state)
	if got := exposed.Get("Session-Id"); got != "session-original" {
		t.Fatalf("response Session-Id = %q", got)
	}
	if got := exposed.Get("X-Unrelated"); got != mappedSession {
		t.Fatalf("unrelated response header changed to %q", got)
	}
}

func codexCredentialIdentityTestConfig(synthesize bool) *config.Config {
	return &config.Config{Codex: config.CodexConfig{CredentialIdentity: config.CodexCredentialIdentityConfig{
		Enabled:                         true,
		SynthesizeMissingInstallationID: synthesize,
	}}}
}

func codexCredentialIdentityTestAuth(id string, namespace string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       id,
		Provider: "codex",
		ProxyURL: "socks5://127.0.0.1:1080",
		Metadata: map[string]any{
			"access_token": "secret",
			internalcodex.CredentialIdentityVersionMetadataKey:   internalcodex.CredentialIdentityCurrentVersion,
			internalcodex.CredentialIdentityNamespaceMetadataKey: namespace,
		},
	}
}
