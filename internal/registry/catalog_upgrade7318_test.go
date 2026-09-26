package registry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
)

// catalogReadStartTransport7318 gates a real network body after response headers.
// The request context must remain live when the first body read starts.
type catalogReadStartTransport7318 struct {
	base   http.RoundTripper
	onRead func()
}

func (r catalogReadStartTransport7318) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, errRoundTrip := r.base.RoundTrip(req)
	if errRoundTrip != nil {
		return nil, errRoundTrip
	}
	resp.Body = &catalogReadStartBody7318{ReadCloser: resp.Body, ctx: req.Context(), onRead: r.onRead}
	return resp, nil
}

type catalogReadStartBody7318 struct {
	io.ReadCloser
	ctx    context.Context
	onRead func()
	once   sync.Once
}

func (b *catalogReadStartBody7318) Read(p []byte) (int, error) {
	b.once.Do(b.onRead)
	if errContext := b.ctx.Err(); errContext != nil {
		return 0, errContext
	}
	return b.ReadCloser.Read(p)
}

func TestDevinCatalogFallbackPreservesCustomModels7318(t *testing.T) {
	devinCatalogStore.mu.RLock()
	originalModels := cloneModelInfos(devinCatalogStore.models)
	originalRaw := append([]byte(nil), devinCatalogStore.rawJSON...)
	originalRevision := devinCatalogStore.revision
	devinCatalogStore.mu.RUnlock()
	originalURLs := devinModelsURLs
	t.Cleanup(func() {
		devinModelsURLs = originalURLs
		devinCatalogStore.mu.Lock()
		devinCatalogStore.models, devinCatalogStore.rawJSON, devinCatalogStore.revision = originalModels, originalRaw, originalRevision
		devinCatalogStore.mu.Unlock()
	})

	customPayload := []byte(`{"devin":[{"id":"custom-private","display_name":"Private custom model","owned_by":"private","context_length":65536,"support_configuration_update":true,"thinking":{"levels":["low","high"]}}]}`)
	if _, errLoad := loadDevinModelsFromBytes(customPayload, "test-custom"); errLoad != nil {
		t.Fatal(errLoad)
	}
	baseline := GetDevinModels()
	baselineRaw, baselineRevision := GetDevinModelsSnapshot()

	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "HTTP failure", status: http.StatusBadGateway, body: "upstream unavailable"},
		{name: "malformed JSON", status: http.StatusOK, body: `{"devin":`},
		{name: "empty catalog", status: http.StatusOK, body: `{"devin":[]}`},
		{name: "duplicate IDs", status: http.StatusOK, body: `{"devin":[{"id":"custom-private"},{"id":"DEVIN/CUSTOM-PRIVATE"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer ts.Close()
			devinModelsURLs = []string{ts.URL}
			tryRefreshDevinModels(context.Background(), "test rejected custom refresh")
			if got := GetDevinModels(); !reflect.DeepEqual(got, baseline) {
				t.Fatalf("rejected refresh modified custom models: got %#v, want %#v", got, baseline)
			}
			raw, revision := GetDevinModelsSnapshot()
			if string(raw) != string(baselineRaw) || revision != baselineRevision {
				t.Fatalf("rejected refresh changed raw catalog or revision: revision=%d, want %d", revision, baselineRevision)
			}
		})
	}

	custom := LookupDevinModel(" CUSTOM-PRIVATE ")
	if custom == nil || custom.ID != "devin/custom-private" || !custom.SupportConfigurationUpdate || custom.ContextLength != 65536 {
		t.Fatalf("custom metadata was not retained: %#v", custom)
	}
	custom.Thinking.Levels[0] = "mutated"
	if got := LookupDevinModel("devin/custom-private"); got.Thinking.Levels[0] != "low" {
		t.Fatal("lookup returned mutable catalog metadata")
	}
	if builtin := LookupDevinModel("SWE-1-6-SLOW"); builtin == nil || builtin.ContextLength != 200000 || builtin.MaxCompletionTokens != 64000 {
		t.Fatalf("builtin missing from custom catalog: %#v", builtin)
	}
}

func TestDevinCatalogRemoteSourceFallback7318(t *testing.T) {
	originalURLs := devinModelsURLs
	t.Cleanup(func() { devinModelsURLs = originalURLs })
	var failedRequests, fallbackRequests atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		failedRequests.Add(1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer first.Close()
	const expected = `{"devin":[{"id":"custom-fallback","owned_by":"private"}]}`
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackRequests.Add(1)
		_, _ = io.WriteString(w, expected)
	}))
	defer second.Close()
	devinModelsURLs = []string{first.URL, second.URL}
	body, source := fetchDevinModelsFromRemote(context.Background())
	if string(body) != expected || source != second.URL || failedRequests.Load() != 1 || fallbackRequests.Load() != 1 {
		t.Fatalf("source fallback failed: body=%s, source=%s, requests=%d/%d", body, source, failedRequests.Load(), fallbackRequests.Load())
	}
}

func TestDevinCatalogCanceledBody7318(t *testing.T) {
	originalURLs, originalTransport := devinModelsURLs, http.DefaultTransport
	t.Cleanup(func() {
		devinModelsURLs, http.DefaultTransport = originalURLs, originalTransport
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer ts.Close()
	http.DefaultTransport = catalogReadStartTransport7318{base: originalTransport, onRead: cancel}
	devinModelsURLs = []string{ts.URL}
	if body, source := fetchDevinModelsFromRemote(ctx); body != nil || source != "" {
		t.Fatalf("canceled body accepted: body=%s, source=%s", body, source)
	}
}

func TestCodexBuiltinCatalogSurvivesRemoteReplacement7318(t *testing.T) {
	original := getModels()
	originalURLs := modelsURLs
	t.Cleanup(func() {
		modelsURLs = originalURLs
		modelsCatalogStore.mu.Lock()
		modelsCatalogStore.data = original
		modelsCatalogStore.mu.Unlock()
	})
	payload := []byte(`{"codex-pro":[{"id":"private-codex","owned_by":"private","support_configuration_update":true}]}`)
	// A provider refresh cannot remove the fork's built-in image definitions.
	if errLoad := loadModelsFromBytes(payload, "test-private-catalog"); errLoad != nil {
		t.Fatal(errLoad)
	}
	models := GetCodexProModels()
	found := make(map[string]*ModelInfo, len(models))
	for _, model := range models {
		found[model.ID] = model
	}
	if custom := found["private-codex"]; custom == nil || !custom.SupportConfigurationUpdate || custom.OwnedBy != "private" {
		t.Fatalf("custom model missing or altered: %#v", custom)
	}
	for _, id := range []string{codexBuiltinImage15ModelID, codexBuiltinImageModelID, codexBuiltinImage25FlareModelID, codexBuiltinImage25SunburstModelID, codexBuiltinImage25ModelID} {
		if found[id] == nil {
			t.Errorf("fork image model %s lost after catalog replacement", id)
		}
	}
	publicJSON, errMarshal := json.Marshal(found["private-codex"])
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	var public map[string]any
	if errUnmarshal := json.Unmarshal(publicJSON, &public); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if _, exposed := public["support_configuration_update"]; exposed {
		t.Fatal("internal configuration-update capability leaked through catalog serialization")
	}
}
