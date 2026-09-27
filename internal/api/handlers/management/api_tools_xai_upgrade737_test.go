package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestAPICallXAIRefreshPreservesConcurrentControlChanges(t *testing.T) {
	for _, action := range []string{"edit", "delete", "replace"} {
		t.Run(action, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(started)
				<-release
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"access_token":"fresh-token","refresh_token":"fresh-refresh","expires_in":3600}`))
			}))
			defer tokenServer.Close()
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			var upstreamHits atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamHits.Add(1)
				if r.Header.Get("Authorization") != "Bearer fresh-token" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				_, _ = w.Write([]byte(`{"ok":true}`))
			}))
			defer upstream.Close()
			store := &memoryAuthStore{}
			manager := coreauth.NewManager(store, nil, nil)
			auth := upgrade737XAIAuth(t.Name(), tokenServer.URL)
			registered, errRegister := manager.Register(context.Background(), auth)
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			h := &Handler{cfg: &config.Config{}, authManager: manager, tokenStore: store}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- upgrade737APICall(h, registered.EnsureIndex(), upstream.URL) }()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("refresh did not start")
			}
			if action == "edit" {
				_, errEdit := manager.UpdateAndPersistMetadata(context.Background(), auth.ID, func(current *coreauth.Auth) error {
					current.ProxyURL = "http://new-route.invalid:8080"
					current.Metadata["proxy_url"] = current.ProxyURL
					current.Metadata["concurrency_limit"] = 3
					current.Metadata["note"] = "operator edit"
					return nil
				})
				if errEdit != nil {
					t.Fatal(errEdit)
				}
			} else {
				manager.Remove(context.Background(), auth.ID)
				if errDelete := store.Delete(context.Background(), auth.ID); errDelete != nil {
					t.Fatal(errDelete)
				}
				if action == "replace" {
					replacement := upgrade737XAIAuth(auth.ID, tokenServer.URL)
					replacement.Metadata["access_token"] = "replacement-token"
					replacement.Metadata["refresh_token"] = "replacement-refresh"
					if _, errRegister := manager.Register(context.Background(), replacement); errRegister != nil {
						t.Fatal(errRegister)
					}
				}
			}
			close(release)
			var recorder *httptest.ResponseRecorder
			select {
			case recorder = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("APICall did not finish")
			}
			live, _ := manager.GetByID(auth.ID)
			store.mu.Lock()
			saved := store.items[auth.ID]
			store.mu.Unlock()
			if action == "edit" {
				if recorder.Code != http.StatusOK || upstreamHits.Load() != 1 {
					t.Fatalf("original request route failed: status=%d hits=%d body=%s", recorder.Code, upstreamHits.Load(), recorder.Body.String())
				}
				for _, value := range []*coreauth.Auth{live, saved} {
					if value == nil || value.ProxyURL != "http://new-route.invalid:8080" || fmt.Sprint(value.Metadata["concurrency_limit"]) != "3" || value.Metadata["note"] != "operator edit" || value.Metadata["access_token"] != "fresh-token" {
						t.Fatalf("refresh lost concurrent controls or token: %+v", value)
					}
				}
			} else {
				if recorder.Code < 400 || upstreamHits.Load() != 0 {
					t.Fatalf("obsolete refresh reached upstream: status=%d hits=%d", recorder.Code, upstreamHits.Load())
				}
				if action == "delete" && (live != nil || saved != nil) {
					t.Fatal("refresh resurrected deleted credential")
				}
				if action == "replace" {
					for _, value := range []*coreauth.Auth{live, saved} {
						if value == nil || value.Metadata["access_token"] != "replacement-token" || value.Metadata["refresh_token"] != "replacement-refresh" {
							t.Fatal("obsolete refresh overwrote replacement credential")
						}
					}
				}
			}
		})
	}
}

type upgrade737ProxyCanary struct{ hits atomic.Int32 }

func (c *upgrade737ProxyCanary) RoundTrip(r *http.Request) (*http.Response, error) {
	c.hits.Add(1)
	if r.Body != nil {
		_ = r.Body.Close()
	}
	return nil, fmt.Errorf("unexpected inherited transport")
}

func TestAPICallXAIEmptyProxyPinsDirectRefresh(t *testing.T) {
	canary := &upgrade737ProxyCanary{}
	previous := http.DefaultTransport
	http.DefaultTransport = canary
	defer func() { http.DefaultTransport = previous }()
	var tokenHits atomic.Int32
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenHits.Add(1)
		_, _ = w.Write([]byte(`{"access_token":"fresh-token","expires_in":3600}`))
	}))
	defer tokenServer.Close()
	var apiHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiHits.Add(1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	manager := coreauth.NewManager(nil, nil, nil)
	auth := upgrade737XAIAuth(t.Name(), tokenServer.URL)
	auth.ProxyURL = ""
	registered, errRegister := manager.Register(context.Background(), auth)
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	h := &Handler{cfg: &config.Config{}, authManager: manager}
	recorder := upgrade737APICall(h, registered.EnsureIndex(), upstream.URL)
	if recorder.Code != http.StatusOK || tokenHits.Load() != 1 || apiHits.Load() != 1 || canary.hits.Load() != 0 {
		t.Fatalf("route changed during refresh: status=%d token=%d api=%d canary=%d", recorder.Code, tokenHits.Load(), apiHits.Load(), canary.hits.Load())
	}
}

func upgrade737XAIAuth(id, endpoint string) *coreauth.Auth {
	return &coreauth.Auth{ID: id, FileName: id, Provider: "xai", ProxyURL: "direct", Metadata: map[string]any{
		"type": "xai", "auth_kind": "oauth", "access_token": "stale-token", "refresh_token": id + "-refresh",
		"token_endpoint": endpoint, "expired": "2000-01-01T00:00:00Z", "concurrency_limit": 10,
	}}
}

func upgrade737APICall(h *Handler, index, target string) *httptest.ResponseRecorder {
	payload, _ := json.Marshal(map[string]any{"auth_index": index, "method": "GET", "url": target, "header": map[string]string{"Authorization": "Bearer $TOKEN$"}})
	router := gin.New()
	router.POST("/", h.APICall)
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)
	return recorder
}
