package management_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/api/handlers/management"
	devinauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/devin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestRefreshAuthFiles_DevinQuotaObservationSurvivesManagerAndList(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	for _, activeCooldown := range []bool{false, true} {
		name := "available"
		if activeCooldown {
			name = "active_credential_quota"
		}
		t.Run(name, func(t *testing.T) {
			const token = "devin-session-token$quota-http-fixture"
			const targetURL = "http://127.0.0.1:1"
			var statusCalls atomic.Int32
			statusPayload := devinManagementQuotaStatusFixture()
			// This local server acts as the selected HTTP proxy. It never forwards
			// requests; bypassing it reaches only a closed loopback port.
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Method != http.MethodPost || req.URL.Path != devinauth.DevinGetUserStatusPath || req.URL.Host != "127.0.0.1:1" {
					t.Errorf("unexpected status proxy request: %s %s", req.Method, req.URL)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				body, errRead := io.ReadAll(req.Body)
				if errRead != nil || !strings.Contains(string(body), token) {
					t.Error("status request did not carry the fixture credential")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				statusCalls.Add(1)
				w.Header().Set("Content-Type", "application/proto")
				_, _ = w.Write(statusPayload)
			}))
			t.Cleanup(proxy.Close)

			cfg := &config.Config{AuthDir: t.TempDir(), SDKConfig: config.SDKConfig{ProxyURL: targetURL}}
			store := sdkauth.NewFileTokenStore()
			store.SetBaseDir(cfg.AuthDir)
			manager := coreauth.NewManager(store, nil, nil)
			manager.SetConfig(cfg)
			manager.RegisterExecutor(runtimeexecutor.NewDevinExecutor(cfg))
			path := filepath.Join(cfg.AuthDir, "devin-quota-http.json")
			oldObservedAt := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
			recoverAt := time.Now().Add(time.Hour)
			auth := &coreauth.Auth{
				ID: "devin-quota-http.json", FileName: "devin-quota-http.json", Provider: "devin",
				Status: coreauth.StatusActive, ProxyURL: proxy.URL, Prefix: "operator-prefix",
				Attributes: map[string]string{"path": path, "api_key": token, "base_url": targetURL, "weight": "3"},
				Metadata: map[string]any{
					"type": "devin", "api_key": token, "base_url": targetURL,
					"proxy_url": proxy.URL, "weight": 3, "request_retry": 2,
				},
				Quota: coreauth.QuotaState{
					ObservedAt: oldObservedAt,
					Signals:    map[string]string{"daily_quota_remaining_percent": "1%", "weekly_quota_remaining_percent": "2%"},
				},
			}
			if activeCooldown {
				auth.Status = coreauth.StatusError
				auth.StatusMessage = "credential quota cooling down"
				auth.Unavailable = true
				auth.NextRetryAfter = recoverAt
				auth.Quota.Exceeded = true
				auth.Quota.Reason = "credential_quota"
				auth.Quota.NextRecoverAt = recoverAt
				auth.Quota.BackoffLevel = 4
			}
			if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("register Devin credential: %v", errRegister)
			}
			handler := management.NewHandlerWithoutConfigFilePath(cfg, manager)
			engine := gin.New()
			engine.POST("/auth-files/refresh", handler.RefreshAuthFiles)
			engine.GET("/auth-files", handler.ListAuthFiles)

			refresh := httptest.NewRecorder()
			engine.ServeHTTP(refresh, httptest.NewRequest(http.MethodPost, "/auth-files/refresh?name="+auth.FileName, nil))
			if refresh.Code != http.StatusOK {
				t.Fatalf("refresh status = %d: %s", refresh.Code, refresh.Body.String())
			}
			var refreshed struct {
				OK   bool          `json:"ok"`
				Auth coreauth.Auth `json:"auth"`
			}
			if errDecode := json.Unmarshal(refresh.Body.Bytes(), &refreshed); errDecode != nil || !refreshed.OK {
				t.Fatalf("decode successful refresh: ok=%t err=%v", refreshed.OK, errDecode)
			}
			if statusCalls.Load() != 1 {
				t.Fatalf("status calls through credential proxy = %d, want 1", statusCalls.Load())
			}
			current, exists := manager.GetByID(auth.ID)
			if !exists {
				t.Fatal("refreshed credential missing from manager")
			}
			if !current.Quota.ObservedAt.After(oldObservedAt) || current.Quota.Signals["daily_quota_remaining_percent"] != "95%" || current.Quota.Signals["weekly_quota_remaining_percent"] != "45%" {
				t.Fatalf("manager lost refreshed quota observation: %+v", current.Quota)
			}
			if !refreshed.Auth.Quota.ObservedAt.Equal(current.Quota.ObservedAt) || !reflect.DeepEqual(refreshed.Auth.Quota.Signals, current.Quota.Signals) {
				t.Fatal("refresh HTTP response and manager quota differ")
			}
			if current.ProxyURL != proxy.URL || current.Metadata["proxy_url"] != proxy.URL || current.Prefix != auth.Prefix || current.Attributes["weight"] != "3" || current.Metadata["weight"] != 3 || current.Metadata["request_retry"] != 2 {
				t.Fatal("refresh changed configured proxy or credential controls")
			}
			if activeCooldown && (!current.Unavailable || current.Status != coreauth.StatusError || current.StatusMessage != auth.StatusMessage || !current.NextRetryAfter.Equal(recoverAt) || !current.Quota.Exceeded || current.Quota.Reason != "credential_quota" || !current.Quota.NextRecoverAt.Equal(recoverAt) || current.Quota.BackoffLevel != 4) {
				t.Fatalf("refresh changed active credential cooldown: status=%s unavailable=%t quota=%+v", current.Status, current.Unavailable, current.Quota)
			}

			list := httptest.NewRecorder()
			engine.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/auth-files?name="+auth.FileName, nil))
			if list.Code != http.StatusOK {
				t.Fatalf("list status = %d: %s", list.Code, list.Body.String())
			}
			var listed struct {
				Files []struct {
					ID    string              `json:"id"`
					Quota coreauth.QuotaState `json:"quota"`
				} `json:"files"`
			}
			if errDecode := json.Unmarshal(list.Body.Bytes(), &listed); errDecode != nil {
				t.Fatalf("decode auth list: %v", errDecode)
			}
			if len(listed.Files) != 1 || listed.Files[0].ID != auth.ID || !listed.Files[0].Quota.ObservedAt.Equal(current.Quota.ObservedAt) || !reflect.DeepEqual(listed.Files[0].Quota.Signals, current.Quota.Signals) {
				t.Fatalf("management list lost refreshed quota observation: %+v", listed.Files)
			}

			data, errRead := os.ReadFile(path)
			if errRead != nil {
				t.Fatalf("read persisted credential: %v", errRead)
			}
			var persisted map[string]any
			if errDecode := json.Unmarshal(data, &persisted); errDecode != nil {
				t.Fatalf("decode persisted credential: %v", errDecode)
			}
			if current.Metadata["plan"] != "Pro" || persisted["plan"] != "Pro" || persisted["proxy_url"] != proxy.URL || persisted["api_key"] != token {
				t.Fatal("refresh failed to persist durable provider metadata and configured proxy")
			}
			for _, key := range []string{"quota", "signals", "observed_at", "daily_quota_remaining_percent", "weekly_quota_remaining_percent", "daily_quota_reset_at", "weekly_quota_reset_at"} {
				if _, exists := current.Metadata[key]; exists {
					t.Errorf("transient quota field %q leaked into runtime token metadata", key)
				}
				if _, exists := persisted[key]; exists {
					t.Errorf("transient quota field %q leaked into credential file", key)
				}
			}
		})
	}
}

func devinManagementQuotaStatusFixture() []byte {
	appendBytes := func(dst []byte, field protowire.Number, value []byte) []byte {
		return protowire.AppendBytes(protowire.AppendTag(dst, field, protowire.BytesType), value)
	}
	planInfo := appendBytes(nil, 2, []byte("Pro"))
	planStatus := appendBytes(nil, 1, planInfo)
	for _, field := range []struct {
		number protowire.Number
		value  uint64
	}{{14, 95}, {15, 45}, {17, 1789200000}, {18, 1789286400}} {
		planStatus = protowire.AppendVarint(protowire.AppendTag(planStatus, field.number, protowire.VarintType), field.value)
	}
	userStatus := appendBytes(nil, 7, []byte("quota-fixture@example.test"))
	userStatus = appendBytes(userStatus, 13, planStatus)
	return appendBytes(nil, 1, userStatus)
}
