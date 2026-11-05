package auth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestFetchAntigravityAvailableModelsStrictProxyAndPayload(t *testing.T) {
	var directHits, proxyHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		directHits.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer upstream.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil || ojson.GetBytes(body, "project").String() != "fixture-project" {
			t.Errorf("project payload was not preserved: %v", errRead)
		}
		if r.URL.String() != upstream.URL+AntigravityModelsPath || r.Header.Get("User-Agent") != "fixture-agent" || r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("selected route or request identity changed")
		}
		_, _ = w.Write([]byte(`{"models":{"gemini-test":{}},"webSearchModelIds":["gemini-test"]}`))
	}))
	defer proxy.Close()
	req := AntigravityModelProbeRequest{BaseURLs: []string{upstream.URL}, ProxyURL: proxy.URL, AccessToken: "fixture-token", ProjectID: " fixture-project ", UserAgent: "fixture-agent"}
	hints, status := FetchAntigravityAvailableModels(context.Background(), req)
	if status != AntigravityProbeStatusSuccess || len(hints.ModelIDs) != 1 || proxyHits.Load() != 1 || directHits.Load() != 0 {
		t.Fatalf("explicit proxy not used: status=%v proxy=%d direct=%d", status, proxyHits.Load(), directHits.Load())
	}
	req.ProxyURL = "unsupported://127.0.0.1:1"
	if _, status = FetchAntigravityAvailableModels(context.Background(), req); status != AntigravityProbeStatusTransientError {
		t.Fatalf("invalid proxy status = %v", status)
	}
	if directHits.Load() != 0 || proxyHits.Load() != 1 {
		t.Fatal("invalid explicit proxy fell back to another route")
	}
}
