package auth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	metaauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/meta"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

type metaSubscriptionUpgradeFixture struct{ metaLoginFixture }

func (*metaSubscriptionUpgradeFixture) WaitForAuthorization(context.Context, *metaauth.DeviceCodeResponse) (*metaauth.MetaAuthBundle, error) {
	return &metaauth.MetaAuthBundle{
		TokenData: &metaauth.TokenData{AccessToken: "dca:renewed", TokenType: "Bearer"},
		MintedKey: &metaauth.MintedKeyResponse{APIKey: "LLM|renewed", UserEmail: "subscription@example.test", SubsTierName: "Pro", SubsTierID: "pro", IsSubsActive: true, HasPaymentMethod: false},
	}, nil
}

func TestMetaMintSubscriptionReloginPreservesControlsUpgrade(t *testing.T) {
	original := newMetaOAuthService
	newMetaOAuthService = func(cfg *config.Config) metaOAuthService {
		if cfg.ProxyURL != "socks5://selected.example:1080" {
			t.Fatal("selected acquisition proxy changed")
		}
		return &metaSubscriptionUpgradeFixture{}
	}
	t.Cleanup(func() { newMetaOAuthService = original })
	record, errLogin := (MetaAuthenticator{}).Login(context.Background(), &config.Config{}, &LoginOptions{NoBrowser: true, Metadata: map[string]string{"proxy_url": "socks5://selected.example:1080"}})
	if errLogin != nil {
		t.Fatal(errLogin)
	}
	path := filepath.Join(t.TempDir(), record.FileName)
	previous := []byte(`{"type":"meta","proxy_url":"socks5://selected.example:1080","api_key":"LLM|old","dca_token":"dca:old","priority":-2,"concurrency_limit":3,"subs_tier_name":"Free","subs_tier_id":"free","is_subs_active":false,"has_payment_method":true}`)
	if errWrite := os.WriteFile(path, previous, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	if errSave := record.Storage.SaveTokenToFile(path); errSave != nil {
		t.Fatal(errSave)
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	var saved map[string]any
	if errDecode := json.Unmarshal(raw, &saved); errDecode != nil {
		t.Fatal(errDecode)
	}
	for key, want := range map[string]any{"priority": float64(-2), "concurrency_limit": float64(3), "proxy_url": record.ProxyURL, "api_key": "LLM|renewed", "dca_token": "dca:renewed", "subs_tier_name": "Pro", "subs_tier_id": "pro", "is_subs_active": true, "has_payment_method": false} {
		if got := saved[key]; got != want {
			t.Errorf("saved %s=%v, want%v", key, got, want)
		}
	}
}
