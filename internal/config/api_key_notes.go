package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// MaxAPIKeyNoteLength limits notes by Unicode character count after trimming.
const MaxAPIKeyNoteLength = 120

// APIKeyNoteID keeps display notes attached to a key independently of list order.
// This identifier is separate from the salted traffic identity.
func APIKeyNoteID(key string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(key)))
	return hex.EncodeToString(digest[:])
}

// APIKeyNote returns the display note without changing authentication or key identity.
func (cfg *Config) APIKeyNote(key string) string {
	if cfg == nil || strings.TrimSpace(key) == "" {
		return ""
	}
	return strings.TrimSpace(cfg.APIKeyNotes[APIKeyNoteID(key)])
}

// Validate the YAML nodes before decoding: YAML otherwise coerces numeric and
// boolean scalar values into strings. Errors never echo keys or note contents.
func validateAPIKeyNotesNode(root *yaml.Node) error {
	notes := yamlPath(root, "api-key-notes")
	if notes == nil || notes.ShortTag() == "!!null" {
		return nil
	}
	if notes.Kind != yaml.MappingNode {
		return fmt.Errorf("access.api-key-notes must be a mapping of key hashes to text")
	}
	for i := 0; i+1 < len(notes.Content); i += 2 {
		id, note := notes.Content[i], notes.Content[i+1]
		if id.Kind != yaml.ScalarNode || id.ShortTag() != "!!str" || len(id.Value) != 64 {
			return fmt.Errorf("access.api-key-notes keys must be lowercase SHA-256 hashes")
		}
		for _, ch := range id.Value {
			if !(ch >= '0' && ch <= '9') && !(ch >= 'a' && ch <= 'f') {
				return fmt.Errorf("access.api-key-notes keys must be lowercase SHA-256 hashes")
			}
		}
		if note.Kind != yaml.ScalarNode || note.ShortTag() != "!!str" {
			return fmt.Errorf("access.api-key-notes values must be text")
		}
		if utf8.RuneCountInString(strings.TrimSpace(note.Value)) > MaxAPIKeyNoteLength {
			return fmt.Errorf("access.api-key-notes values must be at most %d characters", MaxAPIKeyNoteLength)
		}
	}
	return nil
}
