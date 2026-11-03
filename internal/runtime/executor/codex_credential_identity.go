package executor

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/google/uuid"
	internalcodex "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type codexCredentialIdentitySnapshot struct {
	AuthID    string
	Namespace uuid.UUID
	Version   int
	ProxyURL  string
}

func applyCodexIdentityBody(cfg *config.Config, auth *cliproxyauth.Auth, userPayload []byte, rawJSON []byte) ([]byte, codexIdentityConfuseState, error) {
	doc := ojson.NewDoc(rawJSON)
	state, errIdentity := applyCodexIdentityBodyDoc(cfg, auth, userPayload, doc)
	if errIdentity != nil {
		return nil, state, errIdentity
	}
	return doc.Bytes(), state, nil
}

func applyCodexIdentityBodyDoc(cfg *config.Config, auth *cliproxyauth.Auth, userPayload []byte, doc *ojson.Doc) (codexIdentityConfuseState, error) {
	if cfg != nil && cfg.Codex.CredentialIdentity.Enabled {
		if cfg.Codex.IdentityConfuse {
			return codexIdentityConfuseState{}, fmt.Errorf("codex credential identity: legacy identity-confuse is also enabled")
		}
		return applyCodexCredentialIdentityBodyDoc(cfg, auth, doc)
	}
	return applyCodexIdentityConfuseBodyDoc(cfg, auth, userPayload, doc), nil
}

func applyCodexCredentialIdentityBodyDoc(cfg *config.Config, auth *cliproxyauth.Auth, doc *ojson.Doc) (codexIdentityConfuseState, error) {
	if auth != nil && codexAuthUsesAPIKey(auth) {
		return codexIdentityConfuseState{}, nil
	}
	if auth == nil {
		return codexIdentityConfuseState{}, fmt.Errorf("codex credential identity: selected OAuth credential is missing")
	}
	if strings.EqualFold(strings.TrimSpace(auth.Attributes[internalcodex.CredentialIdentityConflictAttribute]), "true") {
		return codexIdentityConfuseState{}, fmt.Errorf("codex credential identity for auth %q conflicts with another credential namespace; resolve it from /management.html", auth.ID)
	}
	namespace, version, errIdentity := internalcodex.ParseCredentialIdentity(auth.Metadata)
	if errIdentity != nil {
		return codexIdentityConfuseState{}, fmt.Errorf("codex credential identity for auth %q: %w; initialize it from /management.html", auth.ID, errIdentity)
	}
	state := codexIdentityConfuseState{
		enabled:            true,
		credentialIdentity: true,
		credentialSnapshot: codexCredentialIdentitySnapshot{
			AuthID:    strings.TrimSpace(auth.ID),
			Namespace: namespace,
			Version:   version,
			ProxyURL:  strings.TrimSpace(auth.ProxyURL),
		},
		forwardIdentities: make(map[string]string),
		reverseIdentities: make(map[string]string),
	}

	if doc == nil || doc.Len() == 0 {
		return state, nil
	}
	if promptCacheKey := strings.TrimSpace(doc.Get("prompt_cache_key").String()); promptCacheKey != "" {
		state.originalPromptCacheKey = promptCacheKey
		state.promptCacheKey = state.mapCredentialIdentity("session", promptCacheKey)
		helps.SetStringIfDifferentDoc(doc, "prompt_cache_key", state.promptCacheKey)
	}

	installationPath := "client_metadata.x-codex-installation-id"
	installationID := strings.TrimSpace(doc.Get(installationPath).String())
	if installationID == "" && cfg != nil && cfg.Codex.CredentialIdentity.SynthesizeMissingInstallationID {
		seed := state.originalPromptCacheKey
		if seed == "" {
			seed = "credential-default"
		}
		installationID = "synthetic:" + seed
	}
	if installationID != "" {
		_ = doc.Set(installationPath, state.mapCredentialIdentity("installation", installationID))
	}

	windowPath := "client_metadata.x-codex-window-id"
	if windowID := strings.TrimSpace(doc.Get(windowPath).String()); windowID != "" {
		_ = doc.Set(windowPath, state.mapCredentialIdentity("window", windowID))
	}

	turnMetadataPath := "client_metadata.x-codex-turn-metadata"
	turnMetadata := doc.Get(turnMetadataPath)
	if turnMetadata.Exists() {
		switch turnMetadata.Type {
		case ojson.String:
			updated := rewriteCodexTurnMetadata(turnMetadata.String(), &state, true)
			_ = doc.Set(turnMetadataPath, updated)
		case ojson.JSON:
			updated := rewriteCodexTurnMetadata(turnMetadata.Raw, &state, true)
			_ = doc.SetRawBytes(turnMetadataPath, []byte(updated))
		}
	}
	return state, nil
}

func applyCodexIdentityHeaders(headers http.Header, state *codexIdentityConfuseState, preserveNativeHeaders ...bool) {
	if state == nil || !state.credentialIdentity {
		applyCodexIdentityConfuseHeaders(headers, state)
		return
	}
	if headers == nil || !state.enabled {
		return
	}
	if len(preserveNativeHeaders) > 0 && preserveNativeHeaders[0] {
		state.preserveNativeHeaders = true
		rewriteCodexNativeIdentityHeaders(headers, state, true)
		return
	}

	if value := codexSessionHeaderValue(headers); value != "" {
		setCodexSessionHeaderCasePreserved(headers, "Session-Id", state.mapCredentialIdentity("session", value))
	}
	mapCodexIdentityHeader(headers, "Conversation_id", "session", state)
	mapCodexIdentityHeader(headers, "Thread-Id", "session", state)
	mapCodexIdentityHeader(headers, "X-Client-Request-Id", "request", state)
	mapCodexIdentityHeader(headers, "X-Codex-Window-Id", "window", state)
	if rawTurnMetadata := strings.TrimSpace(headerValueCaseInsensitive(headers, "X-Codex-Turn-Metadata")); rawTurnMetadata != "" {
		setHeaderCasePreserved(headers, "X-Codex-Turn-Metadata", rewriteCodexTurnMetadata(rawTurnMetadata, state, true))
	}
}

func rewriteCodexNativeIdentityHeaders(headers http.Header, state *codexIdentityConfuseState, forward bool) {
	for header, values := range headers {
		switch normalizeCodexIdentityKey(header) {
		case "session_id", "conversation_id", "thread_id", "x_client_request_id", "x_codex_window_id":
			for index, value := range values {
				if strings.TrimSpace(value) == "" {
					continue
				}
				if forward {
					values[index] = state.mapCredentialIdentity(codexIdentityJSONKind(header), value)
				} else if original, exists := state.reverseIdentities[strings.TrimSpace(value)]; exists {
					values[index] = original
				}
			}
		case "x_codex_turn_metadata":
			for index, value := range values {
				if forward {
					values[index] = rewriteCodexTurnMetadata(value, state, true)
				} else if updated, changed := rewriteCodexIdentityJSONString(value, state.reverseIdentities, false); changed {
					values[index] = updated
				}
			}
		}
	}
}

func mapCodexIdentityHeader(headers http.Header, header string, kind string, state *codexIdentityConfuseState) {
	value := strings.TrimSpace(headerValueCaseInsensitive(headers, header))
	if value == "" {
		return
	}
	setHeaderCasePreserved(headers, header, state.mapCredentialIdentity(kind, value))
}

func (state *codexIdentityConfuseState) mapCredentialIdentity(kind string, value string) string {
	value = strings.TrimSpace(value)
	if state == nil || !state.credentialIdentity || !state.enabled || value == "" {
		return value
	}
	key := codexIdentityMapKey(kind, value)
	if mapped, ok := state.forwardIdentities[key]; ok {
		return mapped
	}
	if original, alreadyMapped := state.reverseIdentities[value]; alreadyMapped && state.forwardIdentities[codexIdentityMapKey(kind, original)] == value {
		return value
	}
	mapped := internalcodex.DeriveCredentialIdentity(state.credentialSnapshot.Namespace, kind, value)
	state.forwardIdentities[key] = mapped
	state.reverseIdentities[mapped] = value
	return mapped
}

func codexIdentityMapKey(kind string, value string) string {
	return kind + "\x00" + value
}

func rewriteCodexTurnMetadata(raw string, state *codexIdentityConfuseState, forward bool) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || state == nil || !state.enabled || !ojson.Valid(raw) || !ojson.Parse(raw).IsObject() {
		return raw
	}
	doc := ojson.NewDocString(raw)
	fields := []struct {
		key  string
		kind string
	}{
		{key: "prompt_cache_key", kind: "session"},
		{key: "turn_id", kind: "turn"},
		{key: "window_id", kind: "window"},
	}
	for _, field := range fields {
		value := doc.Get(field.key)
		if value.Type != ojson.String || strings.TrimSpace(value.String()) == "" {
			continue
		}
		if forward {
			_ = doc.Set(field.key, state.mapCredentialIdentity(field.kind, value.String()))
		} else if original, exists := state.reverseIdentities[strings.TrimSpace(value.String())]; exists {
			_ = doc.Set(field.key, original)
		}
	}
	return doc.String()
}

func rewriteCodexIdentityPayload(payload []byte, replacements map[string]string, forward bool) []byte {
	if len(payload) == 0 || len(replacements) == 0 {
		return payload
	}
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') && ojson.ValidBytes(trimmed) {
		return rewriteCodexIdentityPayloadLine(payload, replacements, forward)
	}
	if bytes.Contains(payload, []byte("\n")) {
		lines := bytes.SplitAfter(payload, []byte("\n"))
		changed := false
		for index, line := range lines {
			updated := rewriteCodexIdentityPayloadLine(line, replacements, forward)
			if !bytes.Equal(updated, line) {
				lines[index] = updated
				changed = true
			}
		}
		if changed {
			return bytes.Join(lines, nil)
		}
		return payload
	}
	return rewriteCodexIdentityPayloadLine(payload, replacements, forward)
}

func rewriteCodexIdentityPayloadLine(line []byte, replacements map[string]string, forward bool) []byte {
	lineEnding := []byte(nil)
	content := line
	if bytes.HasSuffix(content, []byte("\n")) {
		lineEnding = []byte("\n")
		content = content[:len(content)-1]
	}

	prefix := []byte(nil)
	jsonPayload := bytes.TrimSpace(content)
	if bytes.HasPrefix(jsonPayload, dataTag) {
		dataIndex := bytes.Index(content, dataTag)
		if dataIndex < 0 {
			return line
		}
		payloadStart := dataIndex + len(dataTag)
		for payloadStart < len(content) && (content[payloadStart] == ' ' || content[payloadStart] == '\t') {
			payloadStart++
		}
		prefix = content[:payloadStart]
		jsonPayload = bytes.TrimSpace(content[payloadStart:])
	}
	if len(jsonPayload) == 0 || jsonPayload[0] != '{' && jsonPayload[0] != '[' {
		return line
	}

	if !ojson.ValidBytes(jsonPayload) {
		return line
	}
	doc := ojson.NewDoc(jsonPayload)
	if !rewriteCodexIdentityJSONDoc(doc, ojson.ParseBytes(jsonPayload), nil, replacements, forward) {
		return line
	}
	updated := doc.Bytes()
	out := make([]byte, 0, len(prefix)+len(updated)+len(lineEnding))
	out = append(out, prefix...)
	out = append(out, updated...)
	out = append(out, lineEnding...)
	return out
}

// Walk the original token views while mutating one document. Numeric and unknown
// payload fields remain raw, including large integers and JSON key punctuation.
func rewriteCodexIdentityJSONDoc(doc *ojson.Doc, value ojson.Result, path []string, replacements map[string]string, forward bool) bool {
	changed := false
	if !value.IsObject() && !value.IsArray() {
		return false
	}
	object := value.IsObject()
	value.ForEach(func(key, child ojson.Result) bool {
		component := key.String()
		if object {
			// Doc paths use a leading colon to force a literal object key.
			component = ":" + ojson.Escape(component)
		}
		childPath := append(append([]string(nil), path...), component)
		kind := ""
		if object {
			kind = codexIdentityJSONKind(key.String())
		}
		if child.Type == ojson.String && kind != "" {
			var replacement string
			var exists bool
			if kind == "turn_metadata" {
				replacement, exists = rewriteCodexIdentityJSONString(child.String(), replacements, forward)
			} else {
				replacementKey := strings.TrimSpace(child.String())
				if forward {
					replacementKey = codexIdentityMapKey(kind, replacementKey)
				}
				replacement, exists = replacements[replacementKey]
			}
			if exists && doc.Set(strings.Join(childPath, "."), replacement) == nil {
				changed = true
			}
			return true
		}
		changed = rewriteCodexIdentityJSONDoc(doc, child, childPath, replacements, forward) || changed
		return true
	})
	return changed
}

func rewriteCodexIdentityJSONString(raw string, replacements map[string]string, forward bool) (string, bool) {
	if !ojson.Valid(raw) {
		return raw, false
	}
	doc := ojson.NewDocString(raw)
	if !rewriteCodexIdentityJSONDoc(doc, ojson.Parse(raw), nil, replacements, forward) {
		return raw, false
	}
	return doc.String(), true
}

func codexIdentityJSONKind(key string) string {
	switch normalizeCodexIdentityKey(key) {
	case "prompt_cache_key", "session_id", "conversation_id", "thread_id":
		return "session"
	case "x_client_request_id":
		return "request"
	case "x_codex_window_id", "window_id":
		return "window"
	case "turn_id":
		return "turn"
	case "x_codex_installation_id", "installation_id":
		return "installation"
	case "x_codex_turn_metadata":
		return "turn_metadata"
	default:
		return ""
	}
}

func normalizeCodexIdentityKey(key string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(key)), "-", "_")
}

func exposeCodexIdentityHeaders(headers http.Header, state codexIdentityConfuseState) http.Header {
	if headers == nil {
		return nil
	}
	exposed := headers.Clone()
	if !state.credentialIdentity || len(state.reverseIdentities) == 0 {
		return exposed
	}
	if state.preserveNativeHeaders {
		rewriteCodexNativeIdentityHeaders(exposed, &state, false)
		return exposed
	}
	for _, header := range []string{
		"Session-Id", "Session_id", "Conversation_id", "Thread-Id",
		"X-Client-Request-Id", "X-Codex-Window-Id",
	} {
		value := strings.TrimSpace(headerValueCaseInsensitive(exposed, header))
		if original, exists := state.reverseIdentities[value]; exists {
			setHeaderCasePreserved(exposed, header, original)
		}
	}
	if raw := strings.TrimSpace(headerValueCaseInsensitive(exposed, "X-Codex-Turn-Metadata")); raw != "" {
		if updated, changed := rewriteCodexIdentityJSONString(raw, state.reverseIdentities, false); changed {
			setHeaderCasePreserved(exposed, "X-Codex-Turn-Metadata", updated)
		}
	}
	return exposed
}

func exposeCodexIdentityStatusError(upstream statusErr, state codexIdentityConfuseState) statusErr {
	body := []byte(upstream.msg)
	exposed := applyCodexIdentityExposeResponsePayload(body, state)
	if len(exposed) == 0 || bytes.Equal(exposed, body) {
		return upstream
	}
	clientError := upstream
	clientError.msg = string(exposed)
	return clientError
}

func exposeCodexIdentityWebsocketError(payload []byte, state codexIdentityConfuseState, upstream error) error {
	exposed := applyCodexIdentityExposeResponsePayload(payload, state)
	if bytes.Equal(exposed, payload) {
		return upstream
	}
	// Preserve the parsed quota scope and retry reset while exposing identities.
	switch parsed := upstream.(type) {
	case statusErrWithHeaders:
		clientError := parsed
		clientError.statusErr = exposeCodexIdentityStatusError(parsed.statusErr, state)
		clientError.headers = exposeCodexIdentityHeaders(parsed.headers, state)
		return clientError
	case *statusErrWithHeaders:
		if parsed != nil {
			clientError := *parsed
			clientError.statusErr = exposeCodexIdentityStatusError(parsed.statusErr, state)
			clientError.headers = exposeCodexIdentityHeaders(parsed.headers, state)
			return &clientError
		}
	}
	return upstream
}
