package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	internalcodex "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type codexCredentialIdentitySnapshot struct {
	AuthID    string
	Namespace uuid.UUID
	Version   int
	ProxyURL  string
}

func applyCodexIdentityBody(cfg *config.Config, auth *cliproxyauth.Auth, userPayload []byte, rawJSON []byte) ([]byte, codexIdentityConfuseState, error) {
	if cfg != nil && cfg.Codex.CredentialIdentity.Enabled {
		if cfg.Codex.IdentityConfuse {
			return nil, codexIdentityConfuseState{}, fmt.Errorf("codex credential identity: legacy identity-confuse is also enabled")
		}
		return applyCodexCredentialIdentityBody(cfg, auth, rawJSON)
	}
	body, state := applyCodexIdentityConfuseBody(cfg, auth, userPayload, rawJSON)
	return body, state, nil
}

func applyCodexCredentialIdentityBody(cfg *config.Config, auth *cliproxyauth.Auth, rawJSON []byte) ([]byte, codexIdentityConfuseState, error) {
	if auth != nil && codexAuthUsesAPIKey(auth) {
		return rawJSON, codexIdentityConfuseState{}, nil
	}
	if auth == nil {
		return nil, codexIdentityConfuseState{}, fmt.Errorf("codex credential identity: selected OAuth credential is missing")
	}
	if strings.EqualFold(strings.TrimSpace(auth.Attributes[internalcodex.CredentialIdentityConflictAttribute]), "true") {
		return nil, codexIdentityConfuseState{}, fmt.Errorf("codex credential identity for auth %q conflicts with another credential namespace; resolve it from /management.html", auth.ID)
	}
	namespace, version, errIdentity := internalcodex.ParseCredentialIdentity(auth.Metadata)
	if errIdentity != nil {
		return nil, codexIdentityConfuseState{}, fmt.Errorf("codex credential identity for auth %q: %w; initialize it from /management.html", auth.ID, errIdentity)
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

	if len(rawJSON) == 0 {
		return rawJSON, state, nil
	}
	if promptCacheKey := strings.TrimSpace(gjson.GetBytes(rawJSON, "prompt_cache_key").String()); promptCacheKey != "" {
		state.originalPromptCacheKey = promptCacheKey
		state.promptCacheKey = state.mapCredentialIdentity("session", promptCacheKey)
		rawJSON = helps.SetStringIfDifferent(rawJSON, "prompt_cache_key", state.promptCacheKey)
	}

	installationPath := "client_metadata.x-codex-installation-id"
	installationID := strings.TrimSpace(gjson.GetBytes(rawJSON, installationPath).String())
	if installationID == "" && cfg != nil && cfg.Codex.CredentialIdentity.SynthesizeMissingInstallationID {
		seed := state.originalPromptCacheKey
		if seed == "" {
			seed = "credential-default"
		}
		installationID = "synthetic:" + seed
	}
	if installationID != "" {
		rawJSON, _ = sjson.SetBytes(rawJSON, installationPath, state.mapCredentialIdentity("installation", installationID))
	}

	windowPath := "client_metadata.x-codex-window-id"
	if windowID := strings.TrimSpace(gjson.GetBytes(rawJSON, windowPath).String()); windowID != "" {
		rawJSON, _ = sjson.SetBytes(rawJSON, windowPath, state.mapCredentialIdentity("window", windowID))
	}

	turnMetadataPath := "client_metadata.x-codex-turn-metadata"
	turnMetadata := gjson.GetBytes(rawJSON, turnMetadataPath)
	if turnMetadata.Exists() {
		switch turnMetadata.Type {
		case gjson.String:
			updated := rewriteCodexTurnMetadata(turnMetadata.String(), &state, true)
			rawJSON, _ = sjson.SetBytes(rawJSON, turnMetadataPath, updated)
		case gjson.JSON:
			updated := rewriteCodexTurnMetadata(turnMetadata.Raw, &state, true)
			rawJSON, _ = sjson.SetRawBytes(rawJSON, turnMetadataPath, []byte(updated))
		}
	}
	return rawJSON, state, nil
}

func applyCodexIdentityHeaders(headers http.Header, state *codexIdentityConfuseState) {
	if state == nil || !state.credentialIdentity {
		applyCodexIdentityConfuseHeaders(headers, state)
		return
	}
	if headers == nil || !state.enabled {
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
	if raw == "" || state == nil || !state.enabled {
		return raw
	}
	var metadata map[string]any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if errDecode := decoder.Decode(&metadata); errDecode != nil || metadata == nil {
		return raw
	}
	fields := []struct {
		key  string
		kind string
	}{
		{key: "prompt_cache_key", kind: "session"},
		{key: "turn_id", kind: "turn"},
		{key: "window_id", kind: "window"},
	}
	for _, field := range fields {
		value, ok := metadata[field.key].(string)
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		if forward {
			metadata[field.key] = state.mapCredentialIdentity(field.kind, value)
		} else if original, exists := state.reverseIdentities[strings.TrimSpace(value)]; exists {
			metadata[field.key] = original
		}
	}
	updated, errMarshal := json.Marshal(metadata)
	if errMarshal != nil {
		return raw
	}
	return string(updated)
}

func rewriteCodexIdentityPayload(payload []byte, replacements map[string]string, forward bool) []byte {
	if len(payload) == 0 || len(replacements) == 0 {
		return payload
	}
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') && json.Valid(trimmed) {
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

	var value any
	decoder := json.NewDecoder(bytes.NewReader(jsonPayload))
	decoder.UseNumber()
	if errDecode := decoder.Decode(&value); errDecode != nil {
		return line
	}
	if !rewriteCodexIdentityJSONValue(value, replacements, forward) {
		return line
	}
	updated, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return line
	}
	out := make([]byte, 0, len(prefix)+len(updated)+len(lineEnding))
	out = append(out, prefix...)
	out = append(out, updated...)
	out = append(out, lineEnding...)
	return out
}

func rewriteCodexIdentityJSONValue(value any, replacements map[string]string, forward bool) bool {
	changed := false
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			kind := codexIdentityJSONKind(key)
			if stringValue, ok := child.(string); ok && kind != "" {
				if normalizeCodexIdentityKey(key) == "x_codex_turn_metadata" {
					if updated, didChange := rewriteCodexIdentityJSONString(stringValue, replacements, forward); didChange {
						typed[key] = updated
						changed = true
					}
					continue
				}
				replacementKey := strings.TrimSpace(stringValue)
				if forward {
					replacementKey = codexIdentityMapKey(kind, replacementKey)
				}
				if replacement, exists := replacements[replacementKey]; exists {
					typed[key] = replacement
					changed = true
				}
				continue
			}
			if rewriteCodexIdentityJSONValue(child, replacements, forward) {
				changed = true
			}
		}
	case []any:
		for _, child := range typed {
			if rewriteCodexIdentityJSONValue(child, replacements, forward) {
				changed = true
			}
		}
	}
	return changed
}

func rewriteCodexIdentityJSONString(raw string, replacements map[string]string, forward bool) (string, bool) {
	var value any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if errDecode := decoder.Decode(&value); errDecode != nil {
		return raw, false
	}
	if !rewriteCodexIdentityJSONValue(value, replacements, forward) {
		return raw, false
	}
	updated, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return raw, false
	}
	return string(updated), true
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
	if clientError, ok := parseCodexWebsocketError(exposed); ok {
		return clientError
	}
	return upstream
}
