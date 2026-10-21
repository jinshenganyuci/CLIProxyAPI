package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestV837LateUnauthorizedCannotRejectReplacementRegistration(t *testing.T) {
	for _, mode := range []string{"execute", "count", "stream-bootstrap"} {
		t.Run(mode, func(t *testing.T) {
			m := NewManager(nil, &FillFirstSelector{}, nil)
			m.SetRetryConfig(0, 0, 0)
			initial, err := m.Register(t.Context(), &Auth{ID: t.Name(), Provider: "codex", Status: StatusActive, Metadata: map[string]any{
				"type": "codex", "access_token": "outer", "use_siwc": true,
				"siwc": map[string]any{"access_token": "same-siwc", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)},
			}})
			if err != nil {
				t.Fatal(err)
			}
			model := "v837-registration-model"
			registry.GetGlobalRegistry().RegisterClient(initial.ID, "codex", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(initial.ID) })
			var replacement *Auth
			execute := func(_ context.Context, selected *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				if replacement != nil {
					t.Fatal("stale request retried on replacement registration")
				}
				requireCredentialConcurrencySnapshot(t, m, initial.ID, 1, 10)
				next := selected.Clone()
				next.ProxyURL = "direct"
				next.Metadata["proxy_url"] = "direct"
				next.Metadata["codex_identity_namespace"] = "new-registration-namespace"
				next.Metadata["concurrency_limit"] = 2
				m.Remove(t.Context(), initial.ID)
				replacement, err = m.Register(t.Context(), next)
				if err != nil {
					t.Fatal(err)
				}
				if replacement.RegistrationEpoch == initial.RegistrationEpoch {
					t.Fatal("registration epoch did not advance")
				}
				return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusUnauthorized, Message: "old request rejected"}
			}
			e := &credentialConcurrencyExecutor{schedulerTestExecutor: schedulerTestExecutor{provider: "codex"}, execute: execute}
			e.stream = func(ctx context.Context, a *Auth, r cliproxyexecutor.Request, o cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				_, err := execute(ctx, a, r, o)
				return nil, err
			}
			m.RegisterExecutor(e)
			req := cliproxyexecutor.Request{Model: model}
			switch mode {
			case "execute":
				_, err = m.Execute(t.Context(), []string{"codex"}, req, cliproxyexecutor.Options{})
			case "count":
				_, err = m.ExecuteCount(t.Context(), []string{"codex"}, req, cliproxyexecutor.Options{})
			case "stream-bootstrap":
				_, err = m.ExecuteStream(t.Context(), []string{"codex"}, req, cliproxyexecutor.Options{})
			}
			if err == nil || replacement == nil {
				t.Fatal("expected an executed stale request failure")
			}
			got, _ := m.GetByID(initial.ID)
			if got.RejectedAccessToken != "" || got.RejectedSIWCAccessToken != "" || got.Generation != replacement.Generation || got.Unavailable || got.CodexSIWCStatus(time.Now()) != SIWCStatusValid {
				t.Fatalf("stale 401 mutated replacement: rejected=%q siwc=%q generation=%d want=%d", got.RejectedAccessToken, got.RejectedSIWCAccessToken, got.Generation, replacement.Generation)
			}
			if got.ProxyURL != "direct" || got.Metadata["codex_identity_namespace"] != "new-registration-namespace" {
				t.Fatal("replacement controls changed")
			}
			requireCredentialConcurrencySnapshot(t, m, initial.ID, 0, 2)
		})
	}
}

func TestV837PersistedMetadataPreservesSIWCRejectionPartition(t *testing.T) {
	for _, change := range []string{"controls", "outer-token", "same-siwc", "new-siwc"} {
		t.Run(change, func(t *testing.T) {
			m, _, base := newMetadataControlsFixture(t, nil)
			base.Metadata["use_siwc"] = true
			base.Metadata["siwc"] = map[string]any{"access_token": "siwc-rejected", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)}
			base, err := m.Update(t.Context(), base)
			if err != nil {
				t.Fatal(err)
			}
			m.MarkResult(t.Context(), Result{AuthID: base.ID, Provider: "codex", AccessToken: "siwc-rejected", RegistrationEpoch: base.RegistrationEpoch, CredentialVersion: base.CredentialVersion, Error: &Error{HTTPStatus: 401}})
			release, err := m.AcquireCredentialConcurrency(t.Context(), base)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			got, err := m.UpdateAndPersistMetadata(t.Context(), base.ID, func(a *Auth) error {
				a.Metadata["concurrency_limit"] = 3
				a.Metadata["codex_identity_namespace"] = "preserved-namespace"
				a.ProxyURL = "direct"
				a.RejectedAccessToken = ""
				a.RejectedSIWCAccessToken = ""
				switch change {
				case "outer-token":
					a.Metadata["access_token"] = "new-outer"
				case "same-siwc":
					a.Metadata["siwc"].(map[string]any)["access_token"] = "siwc-rejected"
				case "new-siwc":
					a.Metadata["siwc"].(map[string]any)["access_token"] = "fresh-siwc"
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if change == "new-siwc" {
				if got.RejectedAccessToken != "" || got.RejectedSIWCAccessToken != "" || got.CodexSIWCStatus(time.Now()) != SIWCStatusValid {
					t.Fatal("new SIWC token retained obsolete rejection")
				}
			} else if got.RejectedAccessToken != "siwc-rejected" || got.RejectedSIWCAccessToken != "siwc-rejected" || got.CodexSIWCStatus(time.Now()) != SIWCStatusInvalid || !got.Unavailable {
				t.Fatalf("metadata edit cleared unchanged rejected SIWC: active=%q nested=%q unavailable=%t", got.RejectedAccessToken, got.RejectedSIWCAccessToken, got.Unavailable)
			}
			if got.ProxyURL != "direct" || got.Metadata["codex_identity_namespace"] != "preserved-namespace" {
				t.Fatal("accepted controls changed")
			}
			requireCredentialConcurrencySnapshot(t, m, base.ID, 1, 3)
			release()
			requireCredentialConcurrencySnapshot(t, m, base.ID, 0, 3)
		})
	}
}
