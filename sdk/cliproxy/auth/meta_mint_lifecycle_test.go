package auth

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestMetaMintRejectsConcurrentCredentialReplacement(t *testing.T) {
	for _, mode := range []string{"prepare", "refresh"} {
		for _, mutation := range []string{"metadata_dca", "attribute_dca", "metadata_api_key", "attribute_api_key", "access_token", "removed_dca", "provider", "storage_dca", "storage_api_key", "storage_access_token", "storage_removed"} {
			t.Run(mode+"/"+mutation, func(t *testing.T) {
				store := &requestPrepareStore{}
				manager := NewManager(store, nil, nil)
				initial := &Auth{
					ID: "meta-concurrent-mint", Provider: "meta", Status: StatusActive,
					Metadata:   map[string]any{"dca_token": "dca:account-a", "api_key": "old-key", "access_token": "old-key"},
					Attributes: map[string]string{"api_key": "old-key", "dca_token": "dca:account-a"},
				}
				if strings.HasPrefix(mutation, "storage_") {
					initial.Metadata = map[string]any{}
					initial.Attributes = nil
					initial.Storage = &metaMintTestStorage{dca: "dca:account-a", apiKey: "old-key", accessToken: "old-key"}
				}
				base, err := manager.Register(WithSkipPersist(context.Background()), initial)
				if err != nil {
					t.Fatal(err)
				}
				replacement := base.Clone()
				switch mutation {
				case "metadata_dca":
					replacement.Metadata["dca_token"] = "dca:account-b"
				case "attribute_dca":
					replacement.Attributes["dca_token"] = "dca:account-b"
				case "metadata_api_key":
					replacement.Metadata["api_key"] = "account-b-key"
				case "attribute_api_key":
					replacement.Attributes["api_key"] = "account-b-key"
				case "access_token":
					replacement.Metadata["access_token"] = "account-b-key"
				case "removed_dca":
					delete(replacement.Metadata, "dca_token")
				case "provider":
					replacement.Provider = "codex"
				case "storage_dca", "storage_api_key", "storage_access_token":
					storage := *base.Storage.(*metaMintTestStorage)
					if mutation == "storage_dca" {
						storage.dca = "dca:account-b"
					} else if mutation == "storage_api_key" {
						storage.apiKey = "account-b-key"
					} else {
						storage.accessToken = "account-b-key"
					}
					replacement.Storage = &storage
				case "storage_removed":
					replacement.Storage = nil
				}
				current, err := manager.Update(context.Background(), replacement)
				if err != nil {
					t.Fatal(err)
				}
				candidate := base.Clone()
				candidate.Metadata["api_key"] = "late-account-a-key"
				candidate.Metadata["access_token"] = "late-account-a-key"
				if candidate.Attributes == nil {
					candidate.Attributes = make(map[string]string)
				}
				candidate.Attributes["api_key"] = "late-account-a-key"
				update := manager.UpdatePreparedAuth
				if mode == "refresh" {
					update = manager.UpdateRefreshedAuth
				}
				result, err := update(context.Background(), base, candidate)
				if result != nil || err == nil || !strings.Contains(err.Error(), "credentials changed during mint") {
					t.Fatalf("late mint accepted: result present = %v, error = %v", result != nil, err)
				}
				live, _ := manager.GetByID(base.ID)
				if !reflect.DeepEqual(live, current) || !reflect.DeepEqual(store.lastAuth(), current) {
					t.Fatal("late mint changed the replacement runtime or persisted credential")
				}
				if got := store.saveCount.Load(); got != 1 {
					t.Fatalf("store saves = %d, want only the credential replacement", got)
				}
			})
		}
	}
}

type metaMintTestStorage struct {
	dca         string
	apiKey      string
	accessToken string
}

func (s *metaMintTestStorage) SaveTokenToFile(string) error { return nil }

func (s *metaMintTestStorage) MetaCredentialTokens() (string, string, string) {
	return s.dca, s.apiKey, s.accessToken
}

type metaMintFailureStore struct {
	requestPrepareStore
	err error
}

func (s *metaMintFailureStore) Save(context.Context, *Auth) (string, error) {
	return "", s.err
}

func TestMetaMintPreservesConcurrentControlsAndRequiresPersistence(t *testing.T) {
	for _, mode := range []string{"prepare", "refresh"} {
		t.Run(mode, func(t *testing.T) {
			store := &requestPrepareStore{}
			manager := NewManager(store, nil, nil)
			base, err := manager.Register(WithSkipPersist(context.Background()), &Auth{
				ID: "meta-controls", Provider: "meta", Status: StatusActive, ProxyURL: "http://old-proxy.invalid:8080",
				Metadata: map[string]any{"dca_token": "dca:account-a", "proxy_url": "http://old-proxy.invalid:8080"},
				Storage:  &metaMintTestStorage{dca: "dca:account-a"},
			})
			if err != nil {
				t.Fatal(err)
			}
			control := base.Clone()
			control.ProxyURL = "direct"
			control.Metadata["proxy_url"] = "direct"
			control.Metadata["notes"] = "concurrent note"
			control.Metadata["weight"] = float64(3)
			control.Prefix = "edited"
			control.Storage = &metaMintTestStorage{dca: "dca:account-a"}
			if _, err := manager.Update(context.Background(), control); err != nil {
				t.Fatal(err)
			}
			candidate := base.Clone()
			candidate.Metadata["api_key"] = "minted-key"
			candidate.Metadata["access_token"] = "minted-key"
			candidate.Storage = &metaMintTestStorage{dca: "dca:account-a", apiKey: "minted-key", accessToken: "minted-key"}
			update := manager.UpdatePreparedAuth
			if mode == "refresh" {
				update = manager.UpdateRefreshedAuth
			}
			result, err := update(context.Background(), base, candidate)
			if err != nil {
				t.Fatal(err)
			}
			for _, record := range []*Auth{result, store.lastAuth()} {
				if record.ProxyURL != "direct" || record.Metadata["proxy_url"] != "direct" || record.Prefix != "edited" || record.Metadata["weight"] != float64(3) || record.Metadata["notes"] != "concurrent note" || record.Metadata["api_key"] != "minted-key" || record.Metadata["access_token"] != "minted-key" || record.Metadata["dca_token"] != "dca:account-a" {
					t.Fatal("mint lost concurrent controls or credential consistency")
				}
			}
		})
		t.Run(mode+"_store_failure", func(t *testing.T) {
			saveErr := errors.New("synthetic store failure")
			manager := NewManager(&metaMintFailureStore{err: saveErr}, nil, nil)
			base, err := manager.Register(WithSkipPersist(context.Background()), &Auth{
				ID: "meta-persist-failure", Provider: "meta", Status: StatusActive,
				Metadata: map[string]any{"dca_token": "dca:account-a"},
			})
			if err != nil {
				t.Fatal(err)
			}
			candidate := base.Clone()
			candidate.Metadata["api_key"] = "unsaved-key"
			candidate.Metadata["access_token"] = "unsaved-key"
			update := manager.UpdatePreparedAuth
			if mode == "refresh" {
				update = manager.UpdateRefreshedAuth
			}
			result, err := update(context.Background(), base, candidate)
			if result != nil || !errors.Is(err, saveErr) {
				t.Fatalf("failed store returned usable mint: result present = %v, error = %v", result != nil, err)
			}
			live, _ := manager.GetByID(base.ID)
			if !reflect.DeepEqual(live, base) {
				t.Fatal("failed store installed the minted credential")
			}
		})
	}
}
