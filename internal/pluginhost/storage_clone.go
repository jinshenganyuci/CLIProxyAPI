package pluginhost

import (
	"bytes"
	"encoding/json"
	"fmt"

	baseauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth"
)

// CloneForMetadataUpdate retains provider data without sharing mutable metadata.
func (s *pluginTokenStorage) CloneForMetadataUpdate() (baseauth.TokenStorage, error) {
	if s == nil {
		return nil, fmt.Errorf("plugin token storage is nil")
	}
	metadata, errClone := baseauth.CloneMetadataForUpdate(s.meta)
	if errClone != nil {
		return nil, errClone
	}
	rawJSON := bytes.Clone(s.rawJSON)
	if len(bytes.TrimSpace(rawJSON)) > 0 {
		var fields map[string]json.RawMessage
		if errDecode := json.Unmarshal(rawJSON, &fields); errDecode != nil {
			return nil, fmt.Errorf("decode plugin storage for metadata update: %w", errDecode)
		}
		// Current metadata owns this control, including deliberate removal.
		// Retaining the old raw field would restore it during the storage merge.
		delete(fields, "concurrency_limit")
		delete(fields, "concurrency-limit")
		var errEncode error
		rawJSON, errEncode = json.Marshal(fields)
		if errEncode != nil {
			return nil, fmt.Errorf("encode plugin storage for metadata update: %w", errEncode)
		}
	}
	return &pluginTokenStorage{provider: s.provider, rawJSON: rawJSON, meta: metadata}, nil
}
