package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestKimiApplyPatchCountTokensPreservesSelectedExitAndSharedAuth(t *testing.T) {
	var selectedHits, fallbackHits atomic.Int32
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackHits.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer fallback.Close()
	selected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		selectedHits.Add(1)
		if r.URL.Host != "api.kimi.ai" || r.URL.Path != "/coding/v1/messages/count_tokens" || r.Header.Get("Authorization") != "Bearer synthetic-selected-token" {
			t.Errorf("incorrect selected credential request: host=%q path=%q", r.URL.Host, r.URL.Path)
		}
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Error(errRead)
		}
		if gjson.GetBytes(body, "model").String() != "k3" || gjson.GetBytes(body, "tools.0.name").String() != "apply_patch" || gjson.GetBytes(body, "tools.0.input_schema.properties.input.type").String() != "string" {
			t.Errorf("native patch declaration was not adapted for token counting: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"input_tokens":42}`)
	}))
	defer selected.Close()
	exec := NewKimiExecutor(&config.Config{SDKConfig: config.SDKConfig{ProxyURL: fallback.URL}})
	auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "kimi-ai", ProxyURL: selected.URL,
		Attributes: map[string]string{"domain": "kimi.ai", "base_url": "http://api.kimi.ai/coding/v1"},
		Metadata:   map[string]any{"access_token": "synthetic-selected-token", "concurrency_limit": 3}}
	before := auth.Clone()
	payload := applyPatchTestRequest()
	original := bytes.Clone(payload)
	ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", kimiRoundTripperFunc(func(*http.Request) (*http.Response, error) {
		fallbackHits.Add(1)
		return nil, io.ErrUnexpectedEOF
	}))
	const requests = 8
	var wg sync.WaitGroup
	for range requests {
		wg.Go(func() {
			_, errCount := exec.CountTokens(ctx, auth, cliproxyexecutor.Request{Model: "kimi-k3", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
			if errCount != nil {
				t.Errorf("CountTokens() = %v", errCount)
			}
		})
	}
	wg.Wait()
	if selectedHits.Load() != requests || fallbackHits.Load() != 0 {
		t.Fatalf("selected exit requests = %d, fallback requests = %d", selectedHits.Load(), fallbackHits.Load())
	}
	if !reflect.DeepEqual(auth, before) || !bytes.Equal(payload, original) {
		t.Fatal("concurrent token counting mutated shared auth or original patch declarations")
	}
	auth.ProxyURL = "invalid://synthetic-user:synthetic-secret@proxy.invalid"
	_, errCount := exec.CountTokens(ctx, auth, cliproxyexecutor.Request{Model: "kimi-k3", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if errCount == nil || selectedHits.Load() != requests || fallbackHits.Load() != 0 {
		t.Fatalf("invalid selected proxy did not fail closed: err=%v selected=%d fallback=%d", errCount, selectedHits.Load(), fallbackHits.Load())
	}
}
