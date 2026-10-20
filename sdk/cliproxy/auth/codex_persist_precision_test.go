package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
)

const precisionCredentialJSON = `{"type":"codex","access_token":"outer-original","refresh_token":"outer-refresh","account_id":"account","proxy_url":"direct","concurrency_limit":3,"codex_identity_version":1,"codex_identity_namespace":"681df939-6c93-4408-9bc1-b20e8ac3be57","siwc":{"access_token":"siwc-original","refresh_token":"siwc-refresh","client_id":"client"},"use_siwc":true,"unknown":{"large":9007199254740993,"edit":"old","remove":"old","array":[9007199254740993,{"large":9007199254740993,"remove":true}]},"delete_me":{"large":9007199254740993}}`

func decodePrecisionCredential(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var out map[string]any
	if err = decoder.Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func precisionCredentialManager(t *testing.T) (*Manager, *Auth, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "precision.json")
	if err := os.WriteFile(path, []byte(precisionCredentialJSON), 0600); err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(precisionCredentialJSON), &metadata); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(&bindingFileStore{path: path}, nil, nil)
	auth, err := manager.Register(WithSkipPersist(context.Background()), &Auth{ID: "precision.json", Provider: "codex", Status: StatusActive, ProxyURL: "direct", Attributes: map[string]string{"path": path}, Metadata: metadata})
	if err != nil {
		t.Fatal(err)
	}
	return manager, auth, path
}

func TestCodexPersistencePrecisionAfterSuccessfulRequest(t *testing.T) {
	manager, auth, path := precisionCredentialManager(t)
	before := decodePrecisionCredential(t, path)
	manager.MarkResult(context.Background(), Result{AuthID: auth.ID, Provider: "codex", Model: "precision-model", Success: true})
	after := decodePrecisionCredential(t, path)
	if after["disabled"] != false {
		t.Fatal("normal disabled=false normalization was lost")
	}
	delete(after, "disabled")
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("successful request changed unrelated JSON: before=%v after=%v", before, after)
	}
	rawBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	infoBefore, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	manager.MarkResult(context.Background(), Result{AuthID: auth.ID, Provider: "codex", Model: "precision-model", Success: true})
	rawAfter, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	infoAfter, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rawBefore, rawAfter) || !os.SameFile(infoBefore, infoAfter) || !infoBefore.ModTime().Equal(infoAfter.ModTime()) {
		t.Fatal("unchanged success rewrote credential file")
	}
}

func TestCodexPersistencePrecisionPreservesEditsAndDeletions(t *testing.T) {
	manager, auth, path := precisionCredentialManager(t)
	_, committed, err := manager.Modify(context.Background(), auth.ID, auth.RegistrationEpoch, func(current *Auth) error {
		unknown := current.Metadata["unknown"].(map[string]any)
		unknown["edit"] = "new"
		delete(unknown, "remove")
		array := unknown["array"].([]any)
		delete(array[1].(map[string]any), "remove")
		unknown["array"] = append(array, nil)
		delete(current.Metadata, "delete_me")
		current.Metadata["added_null"] = nil
		current.Metadata["proxy_url"] = "http://selected.invalid:8123"
		current.ProxyURL = "http://selected.invalid:8123"
		current.Metadata["concurrency_limit"] = 2
		return nil
	})
	if err != nil || !committed {
		t.Fatalf("metadata edit failed: %t %v", committed, err)
	}
	saved := decodePrecisionCredential(t, path)
	unknown := saved["unknown"].(map[string]any)
	if unknown["large"] != json.Number("9007199254740993") || unknown["edit"] != "new" {
		t.Fatalf("nested sibling changed: %v", unknown)
	}
	if _, ok := unknown["remove"]; ok {
		t.Fatal("nested deleted field restored")
	}
	if _, ok := saved["delete_me"]; ok {
		t.Fatal("top-level deleted field restored")
	}
	if value, ok := saved["added_null"]; !ok || value != nil {
		t.Fatal("explicit null lost")
	}
	array := unknown["array"].([]any)
	if len(array) != 3 || array[0] != json.Number("9007199254740993") || array[1].(map[string]any)["large"] != json.Number("9007199254740993") || array[2] != nil {
		t.Fatalf("array values changed: %v", array)
	}
	if _, ok := array[1].(map[string]any)["remove"]; ok {
		t.Fatal("array child deletion restored")
	}
	if saved["proxy_url"] != "http://selected.invalid:8123" || saved["concurrency_limit"] != json.Number("2") {
		t.Fatal("explicit controls lost")
	}
}

func TestCodexPersistencePrecisionHonorsExplicitNumericValues(t *testing.T) {
	for name, value := range map[string]any{"number": json.Number("9007199254740992"), "integer": int64(9007199254740992), "raw": json.RawMessage(`9007199254740992`)} {
		t.Run(name, func(t *testing.T) {
			manager, auth, path := precisionCredentialManager(t)
			manager.MarkResult(context.Background(), Result{AuthID: auth.ID, Provider: "codex", Success: true})
			_, committed, err := manager.Modify(context.Background(), auth.ID, auth.RegistrationEpoch, func(current *Auth) error { current.Metadata["unknown"].(map[string]any)["large"] = value; return nil })
			if err != nil || !committed {
				t.Fatal(err)
			}
			saved := decodePrecisionCredential(t, path)["unknown"].(map[string]any)
			if saved["large"] != json.Number("9007199254740992") {
				t.Fatalf("exact numeric edit was ignored: %v", saved["large"])
			}
			if saved["array"].([]any)[0] != json.Number("9007199254740993") {
				t.Fatal("exact numeric edit changed sibling value")
			}
		})
	}
}

func TestCodexPersistencePrecisionAcrossTokenStorageAndRefresh(t *testing.T) {
	for _, mode := range []string{"token_storage", "outer_refresh", "siwc_refresh"} {
		t.Run(mode, func(t *testing.T) {
			manager, base, path := precisionCredentialManager(t)
			before := decodePrecisionCredential(t, path)
			updated := base.Clone()
			if mode == "token_storage" {
				updated.Storage = &codex.CodexTokenStorage{AccessToken: "stale-storage", RefreshToken: "stale-refresh", AccountID: "account"}
				updated.Metadata["notes"] = "metadata edit"
				if _, err := manager.Update(context.Background(), updated); err != nil {
					t.Fatal(err)
				}
			} else {
				outer := mode == "outer_refresh"
				if outer {
					updated.Metadata["access_token"] = "outer-new"
				} else {
					updated.Metadata["siwc"].(map[string]any)["access_token"] = "siwc-new"
				}
				updated.CodexRefreshPatch = codex.NewCredentialRefreshPatch(base.Metadata, updated.Metadata, outer, !outer)
				if _, err := manager.UpdateRefreshedAuth(context.Background(), base, updated); err != nil {
					t.Fatal(err)
				}
			}
			saved := decodePrecisionCredential(t, path)
			if !reflect.DeepEqual(saved["unknown"], before["unknown"]) || saved["codex_identity_namespace"] != before["codex_identity_namespace"] || saved["proxy_url"] != "direct" || saved["concurrency_limit"] != json.Number("3") {
				t.Fatalf("persistence path changed unrelated values: %v", saved)
			}
			wantOuter, wantSIWC := "outer-original", "siwc-original"
			if mode == "outer_refresh" {
				wantOuter = "outer-new"
			}
			if mode == "siwc_refresh" {
				wantSIWC = "siwc-new"
			}
			if saved["access_token"] != wantOuter || saved["siwc"].(map[string]any)["access_token"] != wantSIWC {
				t.Fatal("credential partitions changed unexpectedly")
			}
		})
	}
}
