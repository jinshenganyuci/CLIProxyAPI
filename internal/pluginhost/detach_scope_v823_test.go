package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type detachStreamClientV823 struct {
	host                 *Host
	id                   string
	instance             *hostCallbackInstance
	streamID, callbackID string
}

func (c *detachStreamClientV823) Call(ctx context.Context, method string, raw []byte) ([]byte, error) {
	if method != pluginabi.MethodExecutorExecuteStream {
		return nil, fmt.Errorf("unexpected method %s", method)
	}
	var req rpcExecutorRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	c.streamID, c.callbackID = req.StreamID, req.HostCallbackID
	emit, _ := json.Marshal(rpcStreamEmitRequest{StreamID: req.StreamID, Payload: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"started\"}}]}\n\n")})
	if _, err := c.host.callFromPlugin(withHostCallbackIdentity(ctx, c.id, c.instance), pluginabi.MethodHostStreamEmit, emit); err != nil {
		return nil, err
	}
	return marshalRPCResult(rpcExecutorStreamResponse{Headers: http.Header{"Content-Type": {"text/event-stream"}}})
}
func (*detachStreamClientV823) Shutdown() {}

func newDetachHostV823(t *testing.T) (*Host, *config.Config) {
	t.Helper()
	loader := newTestSymbolLoader()
	for _, id := range []string{"alpha", "bravo"} {
		loader.lookups[id] = newTestSymbolLookup(&testPlugin{registerResult: validTestPlugin(id), reconfigureResult: validTestPlugin(id)})
	}
	host := NewForTest(loader)
	cfg := &config.Config{Plugins: config.PluginsConfig{Enabled: true, Dir: makePluginDir(t, "alpha", "bravo"), Configs: enabledPluginConfigs("alpha", "bravo")}}
	host.ApplyConfig(context.Background(), cfg)
	t.Cleanup(host.ShutdownAll)
	return host, cfg
}

func TestV823DisableClosesOwnedStreamsAndKeepsSiblingAdmission(t *testing.T) {
	host, cfg := newDetachHostV823(t)
	manager := coreauth.NewManager(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clients := make(map[string]*detachStreamClientV823)
	streams := make(map[string]*coreexecutor.StreamResult)
	operations := make(map[string]*hostHTTPOperation)
	nestedContexts := make(map[string]context.Context)
	host.SetModelExecutor(&fakeHostModelExecutor{executeModelStream: func(ctx context.Context, req handlers.ModelExecutionRequest) (handlers.ModelExecutionStream, *interfaces.ErrorMessage) {
		nestedContexts[req.Model] = ctx
		return handlers.ModelExecutionStream{StatusCode: 200, Chunks: make(chan handlers.ModelExecutionChunk)}, nil
	}})
	for _, id := range []string{"alpha", "bravo"} {
		host.mu.Lock()
		lp := host.loaded[id]
		host.mu.Unlock()
		client := &detachStreamClientV823{host: host, id: id, instance: lp.callbackInstance}
		clients[id] = client
		rpc := &rpcPluginAdapter{host: host, id: id, instance: lp.callbackInstance, client: client}
		var record capabilityRecord
		for _, r := range host.Snapshot().records {
			if r.id == id {
				record = r
			}
		}
		adapter := &executorAdapter{host: host, pluginID: id, provider: id, path: record.path, version: record.version, executor: &rpcProviderExecutor{rpcPluginAdapter: rpc}, inputFormats: []sdktranslator.Format{sdktranslator.FormatOpenAI}, outputFormats: []sdktranslator.Format{sdktranslator.FormatOpenAI}}
		manager.RegisterExecutor(adapter)
		auth := &coreauth.Auth{ID: id, Provider: id, Status: coreauth.StatusActive, ProxyURL: "direct", Metadata: map[string]any{"concurrency_limit": 3, "codex_identity_namespace": "namespace-" + id}}
		if _, err := manager.Register(ctx, auth); err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(id, id, []*registry.ModelInfo{{ID: "detach-model"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		stream, err := manager.ExecuteStream(ctx, []string{id}, coreexecutor.Request{Model: "detach-model", Payload: []byte(`{"messages":[{"role":"user","content":"hello"}]}`)}, coreexecutor.Options{Stream: true, SourceFormat: sdktranslator.FormatOpenAI, Metadata: map[string]any{coreexecutor.PinnedAuthMetadataKey: id}})
		if err != nil {
			t.Fatal(err)
		}
		streams[id] = stream
		select {
		case chunk := <-stream.Chunks:
			if chunk.Err != nil {
				t.Fatal(chunk.Err)
			}
		case <-time.After(time.Second):
			t.Fatal("initial plugin frame missing")
		}
		if n, limit := manager.CredentialConcurrencySnapshot(id); n != 1 || limit != 3 {
			t.Fatalf("initial %s admission=%d/%d", id, n, limit)
		}
		_, op, ok := host.httpOperations.open(id, lp.callbackInstance, client.callbackID, ctx)
		if !ok {
			t.Fatal("HTTP operation did not open")
		}
		operations[id] = op
		raw, _ := json.Marshal(rpcHostModelExecutionRequest{HostCallbackID: client.callbackID, HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{Model: id, Stream: true, EntryProtocol: "openai", ExitProtocol: "openai", Body: []byte(`{"stream":true}`)}})
		if _, err := host.callFromPlugin(withHostCallbackIdentity(ctx, id, lp.callbackInstance), pluginabi.MethodHostModelExecuteStream, raw); err != nil {
			t.Fatal(err)
		}
	}
	disabled := false
	changed := *cfg
	changed.Plugins = cfg.Plugins
	changed.Plugins.Configs = enabledPluginConfigs("bravo")
	changed.Plugins.Configs["alpha"] = config.PluginInstanceConfig{Enabled: &disabled}
	host.ApplyConfig(ctx, &changed)
	if host.PluginLoaded("alpha") || !host.PluginRegistered("bravo") {
		t.Fatal("disable detached the wrong plugin")
	}
	if callbackContextExists(host, clients["alpha"].callbackID) {
		t.Error("disabled plugin callback scope retained")
	}
	if !callbackContextExists(host, clients["bravo"].callbackID) {
		t.Error("sibling callback scope closed")
	}
	if operations["alpha"].ctx.Err() != context.Canceled || operations["bravo"].ctx.Err() != nil {
		t.Error("HTTP operation ownership lost")
	}
	if nestedContexts["alpha"].Err() != context.Canceled || nestedContexts["bravo"].Err() != nil {
		t.Error("nested model stream ownership cleanup failed")
	}
	done := make(chan error, 1)
	go func() {
		var failure error
		for chunk := range streams["alpha"].Chunks {
			if chunk.Err != nil {
				failure = chunk.Err
			}
		}
		done <- failure
	}()
	select {
	case failure := <-done:
		if failure == nil {
			t.Error("plugin detach silently reported successful stream completion")
		}
	case <-time.After(time.Second):
		t.Fatal("disabled plugin stream and credential lease never completed")
	}
	if n, _ := manager.CredentialConcurrencySnapshot("alpha"); n != 0 {
		t.Errorf("disabled plugin retained lease %d", n)
	}
	if n, _ := manager.CredentialConcurrencySnapshot("bravo"); n != 1 {
		t.Errorf("sibling lease prematurely released %d", n)
	}
	closeRaw, _ := json.Marshal(rpcStreamCloseRequest{StreamID: clients["bravo"].streamID})
	if _, err := host.callFromPlugin(withHostCallbackIdentity(ctx, "bravo", clients["bravo"].instance), pluginabi.MethodHostStreamClose, closeRaw); err != nil {
		t.Fatal(err)
	}
	for chunk := range streams["bravo"].Chunks {
		if chunk.Err != nil {
			t.Error(chunk.Err)
		}
	}
	if n, _ := manager.CredentialConcurrencySnapshot("bravo"); n != 0 {
		t.Errorf("sibling completion retained lease %d", n)
	}
	for _, id := range []string{"alpha", "bravo"} {
		a, _ := manager.GetByID(id)
		if a.ProxyURL != "direct" || a.Metadata["codex_identity_namespace"] != "namespace-"+id {
			t.Error("detach changed credential routing metadata")
		}
	}
}

func TestV823DisableStillDetachesWhenDirectoryScanFails(t *testing.T) {
	host, cfg := newDetachHostV823(t)
	badDir := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(badDir, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	changed := *cfg
	changed.Plugins = cfg.Plugins
	changed.Plugins.Dir = badDir
	changed.Plugins.Configs = enabledPluginConfigs("bravo")
	host.ApplyConfig(context.Background(), &changed)
	if host.PluginLoaded("alpha") {
		t.Error("config-removed plugin survived failed directory scan")
	}
	if !host.PluginLoaded("bravo") {
		t.Error("enabled sibling library was detached on scan failure")
	}
}

func TestV823LateInstanceCleanupPreservesReplacementCallback(t *testing.T) {
	host := New()
	oldInstance, newInstance, siblingInstance := &hostCallbackInstance{}, &hostCallbackInstance{}, &hostCallbackInstance{}
	oldID, closeOld := host.openCallbackContextForPluginInstance(context.Background(), "alpha", oldInstance)
	siblingID, closeSibling := host.openCallbackContextForPluginInstance(context.Background(), "bravo", siblingInstance)
	defer closeSibling()
	cleaned := 0
	host.addCallbackCleanup(oldID, func() {
		cleaned++
		if _, _, _, exists := host.lookupCallbackContext(oldID); exists {
			t.Error("cleanup ran before scope removal")
		}
	})
	host.closeHostHTTPCallbackInstance("alpha", oldInstance)
	if cleaned != 1 || callbackContextExists(host, oldID) {
		t.Fatal("old scope cleanup did not complete exactly once")
	}
	replacementID, closeReplacement := host.openCallbackContextForPluginInstance(context.Background(), "alpha", newInstance)
	defer closeReplacement()
	host.closeHostHTTPCallbackInstance("alpha", oldInstance)
	closeOld()
	if cleaned != 1 || !callbackContextExists(host, replacementID) || !callbackContextExists(host, siblingID) {
		t.Fatal("late cleanup affected a replacement or sibling")
	}
	if lateID, closeLate := host.openCallbackContextForPluginInstance(context.Background(), "alpha", oldInstance); lateID != "" {
		closeLate()
		t.Fatal("closed instance created a new callback scope")
	}
}
