package management

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	metaauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/meta"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type metaSubscriptionUpgradeService struct {
	fakeMetaOAuthService
	bundle *metaauth.MetaAuthBundle
}

func (s *metaSubscriptionUpgradeService) WaitForAuthorization(context.Context, *metaauth.DeviceCodeResponse) (*metaauth.MetaAuthBundle, error) {
	return s.bundle, nil
}

func TestMetaOAuthSubscriptionReLoginUsesSelectedRouteUpgrade7318(t *testing.T) {
	h := newDevinProxyTestHandler(t, "socks5://selected.invalid:1080")
	var saved *coreauth.Auth
	h.postAuthPersistHook = func(_ context.Context, record *coreauth.Auth) error {
		saved = record.Clone()
		return nil
	}
	for index, active := range []bool{true, false} {
		state := fmt.Sprintf("meta-subscription-upgrade-%d", index)
		RegisterOAuthSession(state, "meta")
		t.Cleanup(func() { CancelOAuthSession(state) })
		key := &metaauth.MintedKeyResponse{
			APIKey: "LLM|synthetic", UserEmail: "same-account@example.test",
			IsSubsActive: active, HasPaymentMethod: active,
		}
		if active {
			key.SubsTierID, key.SubsTierName = "paid", "Paid Tier"
		}
		service := &metaSubscriptionUpgradeService{bundle: &metaauth.MetaAuthBundle{
			TokenData: &metaauth.TokenData{AccessToken: "dca:synthetic", ExpiresAt: 2000000000}, MintedKey: key,
		}}
		h.completeMetaOAuth(context.Background(), state, true, service, &metaauth.DeviceCodeResponse{})
		if saved == nil {
			t.Fatal("device authorization did not persist")
		}
		raw, errRead := os.ReadFile(filepath.Join(h.cfg.AuthDir, saved.FileName))
		if errRead != nil {
			t.Fatal(errRead)
		}
		var persisted map[string]any
		if errDecode := json.Unmarshal(raw, &persisted); errDecode != nil {
			t.Fatal(errDecode)
		}
		for _, metadata := range []map[string]any{saved.Metadata, persisted} {
			if metadata["is_subs_active"] != active || metadata["has_payment_method"] != active || metadata["subs_tier_id"] != key.SubsTierID || metadata["subs_tier_name"] != key.SubsTierName {
				t.Fatalf("re-login %d did not replace subscription metadata before proxy binding", index)
			}
			if metadata["proxy_url"] != h.cfg.ProxyURL || metadata["api_key"] != key.APIKey || metadata["dca_token"] != "dca:synthetic" {
				t.Fatal("subscription merge lost route or acquisition credentials")
			}
		}
		saved = nil
	}
}
