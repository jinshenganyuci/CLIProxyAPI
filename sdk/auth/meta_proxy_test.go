package auth

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	metaauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/meta"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

type metaLoginFixture struct{ beforeWait func() }

func (*metaLoginFixture) StartDeviceFlow(context.Context) (*metaauth.DeviceCodeResponse, error) {
	return &metaauth.DeviceCodeResponse{DeviceCode: "synthetic", UserCode: "LOCAL", VerificationURI: "https://example.test/device"}, nil
}

func (s *metaLoginFixture) WaitForAuthorization(context.Context, *metaauth.DeviceCodeResponse) (*metaauth.MetaAuthBundle, error) {
	if s.beforeWait != nil {
		s.beforeWait()
	}
	return &metaauth.MetaAuthBundle{TokenData: &metaauth.TokenData{AccessToken: "dca:local", TokenType: "Bearer"}, MintedKey: &metaauth.MintedKeyResponse{APIKey: "LLM|local", UserEmail: "sdk@example.test"}}, nil
}

func (*metaLoginFixture) CreateTokenStorage(bundle *metaauth.MetaAuthBundle) *metaauth.MetaTokenStorage {
	return metaauth.NewMetaAuth(nil).CreateTokenStorage(bundle)
}

func TestMetaOAuthSDKFreezesAndPersistsSelectedRoute(t *testing.T) {
	for _, test := range []struct {
		name, global, selected, want string
		explicit                     bool
	}{
		{name: "selected", global: "http://global.example:8118", selected: "socks5://selected.example:1080", explicit: true, want: "socks5://selected.example:1080"},
		{name: "direct", global: "http://global.example:8118", selected: "direct", explicit: true, want: "direct"},
		{name: "inherited", global: "http://global.example:8118", want: "http://global.example:8118"},
		{name: "empty global", want: "direct"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: test.global}}
			original := newMetaOAuthService
			newMetaOAuthService = func(snapshot *config.Config) metaOAuthService {
				if snapshot == cfg || snapshot.ProxyURL != test.want {
					t.Fatal("SDK did not construct a frozen route")
				}
				return &metaLoginFixture{beforeWait: func() { cfg.ProxyURL = "http://changed.example:8118" }}
			}
			t.Cleanup(func() { newMetaOAuthService = original })
			opts := &LoginOptions{NoBrowser: true, Metadata: map[string]string{}}
			if test.explicit {
				opts.Metadata["proxy_url"] = test.selected
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			record, errLogin := (MetaAuthenticator{}).Login(ctx, cfg, opts)
			if errLogin != nil {
				t.Fatal(errLogin)
			}
			if record.ProxyURL != test.want || record.Metadata["proxy_url"] != test.want {
				t.Fatal("SDK credential changed route")
			}
			path := filepath.Join(t.TempDir(), record.FileName)
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
			if saved["proxy_url"] != test.want || saved["api_key"] != "LLM|local" || saved["dca_token"] != "dca:local" {
				t.Fatal("SDK storage lost acquired route or native tokens")
			}
			cancel()
			if errSave := record.Storage.SaveTokenToFile(filepath.Join(t.TempDir(), record.FileName)); !errors.Is(errSave, context.Canceled) {
				t.Fatal("cancelled login can still create credentials")
			}
		})
	}
}

func TestMetaOAuthSDKRejectsInvalidProxyBeforeDeviceFlow(t *testing.T) {
	original := newMetaOAuthService
	newMetaOAuthService = func(*config.Config) metaOAuthService { t.Fatal("invalid proxy constructed auth service"); return nil }
	t.Cleanup(func() { newMetaOAuthService = original })
	for _, selected := range []string{"", "invalid://user:secret@proxy.example"} {
		_, errLogin := (MetaAuthenticator{}).Login(context.Background(), &config.Config{}, &LoginOptions{NoBrowser: true, Metadata: map[string]string{"proxy_url": selected}})
		if errLogin == nil || strings.Contains(errLogin.Error(), "secret") {
			t.Fatal("invalid SDK route accepted or exposed")
		}
	}
}
