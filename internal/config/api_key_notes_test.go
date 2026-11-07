package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAPIKeyNotesV8PersistenceAndKeyIdentity(t *testing.T) {
	const first, second, removed = "synthetic-client-a", "synthetic-client-b", "synthetic-removed-client"
	digest := sha256.Sum256([]byte(first))
	if APIKeyNoteID(" \t"+first+"\n") != hex.EncodeToString(digest[:]) {
		t.Fatal("note ID does not match SHA-256 of the trimmed key")
	}
	raw := fmt.Sprintf("config-version: 8\nserver: {port: 8317}\naccess:\n  api-keys: [%s, %s]\n  api-key-notes:\n    %s: '  Phone 手机  '\n    %s: Editor\n    %s: Historical note\n", first, second, APIKeyNoteID(first), APIKeyNoteID(second), APIKeyNoteID(removed))
	if err := ValidateV8Config([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseConfigBytes([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKeyNote(first) != "Phone 手机" || cfg.APIKeyNote(second) != "Editor" || cfg.APIKeyNote(removed) != "Historical note" {
		t.Fatal("notes were not loaded independently of the active key list")
	}
	clone := cfg.CloneForRuntime()
	clone.APIKeyNotes[APIKeyNoteID(first)] = "Clone only"
	if cfg.APIKeyNote(first) != "Phone 手机" {
		t.Fatal("runtime clone shares the note map")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err = os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.Port = 8319
	cfg.APIKeys = []string{second, first}
	if err = SaveConfigPreserveComments(path, cfg); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Port != 8319 || !reflect.DeepEqual(reloaded.APIKeys, cfg.APIKeys) || !reflect.DeepEqual(reloaded.APIKeyNotes, cfg.APIKeyNotes) {
		t.Fatal("unrelated save or key reorder changed notes or authentication keys")
	}
	reloaded.APIKeys = []string{second}
	if err = SaveConfigPreserveComments(path, reloaded); err != nil {
		t.Fatal(err)
	}
	reloaded, err = LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.APIKeyNote(first) != "Phone 手机" || reloaded.APIKeyNote(removed) != "Historical note" {
		t.Fatal("a normal save pruned notes for absent keys")
	}
	delete(reloaded.APIKeyNotes, APIKeyNoteID(first))
	if err = SaveConfigPreserveComments(path, reloaded); err != nil {
		t.Fatal(err)
	}
	reloaded, err = LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.APIKeyNote(first) != "" || reloaded.APIKeyNote(second) != "Editor" || reloaded.APIKeyNote(removed) != "Historical note" {
		t.Fatal("explicit note removal was lost or affected another key")
	}
}

func TestAPIKeyNotesRejectInvalidValuesWithoutEchoingContent(t *testing.T) {
	hash := APIKeyNoteID("synthetic-key")
	for _, test := range []struct{ name, notes string }{
		{"not a map", "private-note-marker"},
		{"raw key", "{private-key-marker: note}"},
		{"uppercase hash", fmt.Sprintf("{'%s': note}", strings.ToUpper(hash))},
		{"number", fmt.Sprintf("{'%s': 17}", hash)},
		{"boolean", fmt.Sprintf("{'%s': true}", hash)},
		{"null note", fmt.Sprintf("{'%s': null}", hash)},
		{"object", fmt.Sprintf("{'%s': {private-note-marker: value}}", hash)},
		{"too long", fmt.Sprintf("{'%s': '%s'}", hash, strings.Repeat("秘", 121))},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := []byte("config-version: 8\naccess:\n  api-keys: [synthetic-key]\n  api-key-notes: " + test.notes + "\n")
			_, parseErr := ParseConfigBytes(raw)
			validationErr := ValidateV8Config(raw)
			for _, err := range []error{parseErr, validationErr} {
				if err == nil {
					t.Fatal("invalid note configuration accepted")
				}
				if strings.Contains(err.Error(), "private-") || strings.Contains(err.Error(), "秘") {
					t.Fatal("validation error exposed note or key contents")
				}
			}
		})
	}
	valid := fmt.Sprintf("config-version: 8\naccess:\n  api-key-notes: {'%s': '  %s  '}\n", hash, strings.Repeat("😀", 120))
	cfg, err := ParseConfigBytes([]byte(valid))
	if err != nil {
		t.Fatal("120 Unicode characters must remain valid:", err)
	}
	if cfg.APIKeyNote("synthetic-key") != strings.Repeat("😀", 120) {
		t.Fatal("note length or whitespace handling changed Unicode text")
	}
}
