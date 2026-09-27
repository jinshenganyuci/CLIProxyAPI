package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestMetaNotFoundDeadlineKeepsHealthySiblingAvailableUpgrade7318(t *testing.T) {
	previous := coreauth.QuotaCooldownDisabledForAuth(nil)
	coreauth.SetQuotaCooldownDisabled(false)
	t.Cleanup(func() { coreauth.SetQuotaCooldownDisabled(previous) })
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprintf("explicit-reset-%t", explicit), func(t *testing.T) {
			resetAt := time.Now().Add(2 * time.Minute).Truncate(time.Second)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload struct {
					Model string `json:"model"`
				}
				if errDecode := json.NewDecoder(r.Body).Decode(&payload); errDecode != nil {
					t.Error(errDecode)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if r.Header.Get("X-Client-Id") != "tbh:tui" {
					t.Error("native Meta client header missing")
				}
				if payload.Model == "meta-healthy-upgrade" {
					writeMetaResponsesOK(w, "healthy")
					return
				}
				body := map[string]any{"message": "model not found", "code": "model_not_found"}
				if explicit {
					body["resets_at"] = resetAt.Unix()
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": body})
			}))
			defer server.Close()
			manager := coreauth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(NewMetaExecutor(&config.Config{}))
			id := fmt.Sprintf("meta-short-cooldown-upgrade-%t", explicit)
			auth, errRegister := manager.Register(context.Background(), &coreauth.Auth{
				ID: id, Provider: "meta", ProxyURL: "direct",
				Metadata:   map[string]any{"api_key": "LLM|synthetic", "auth_kind": "oauth"},
				Attributes: map[string]string{"base_url": server.URL},
			})
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			registry.GetGlobalRegistry().RegisterClient(id, "meta", []*registry.ModelInfo{{ID: "meta-missing-upgrade"}, {ID: "meta-healthy-upgrade"}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
			request := func(model string) error {
				_, errExecute := manager.Execute(context.Background(), []string{"meta"}, cliproxyexecutor.Request{
					Model: model, Payload: []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}]}`, model)),
				}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
				return errExecute
			}
			before := time.Now()
			if request("meta-missing-upgrade") == nil {
				t.Fatal("missing model was accepted")
			}
			failed, _ := manager.GetByID(id)
			state := failed.ModelStates["meta-missing-upgrade"]
			if state == nil {
				t.Fatal("missing model cooldown was not retained")
			}
			if explicit {
				if delta := state.NextRetryAfter.Sub(resetAt); delta < -time.Second || delta > time.Second {
					t.Fatalf("upstream reset ignored: delta %v", delta)
				}
			} else if state.NextRetryAfter.Before(before.Add(5*time.Minute)) || state.NextRetryAfter.After(time.Now().Add(5*time.Minute)) {
				t.Fatal("native five-minute fallback changed")
			}
			if failed.Quota.Exceeded {
				t.Fatal("model 404 became a credential quota")
			}
			if errHealthy := request("meta-healthy-upgrade"); errHealthy != nil {
				t.Fatalf("healthy sibling blocked: %v", errHealthy)
			}
			if inflight, limit := manager.CredentialConcurrencySnapshot(auth.ID); inflight != 0 || limit != 10 {
				t.Fatalf("requests leaked a lease or changed default concurrency: %d/%d", inflight, limit)
			}
		})
	}
}
