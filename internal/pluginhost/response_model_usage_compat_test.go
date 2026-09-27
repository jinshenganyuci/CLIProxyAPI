package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type responseModelRPCClient struct {
	methods  []string
	requests [][]byte
}

func (c *responseModelRPCClient) Call(_ context.Context, method string, request []byte) ([]byte, error) {
	c.methods = append(c.methods, method)
	c.requests = append(c.requests, bytes.Clone(request))
	return json.Marshal(pluginabi.Envelope{OK: true, Result: json.RawMessage(`{}`)})
}

func (*responseModelRPCClient) Shutdown() {}

func TestResponseModelUsageKeepsLegacyPluginAccountingAndRPCPayload(t *testing.T) {
	for _, transport := range []string{"native", "rpc"} {
		t.Run(transport, func(t *testing.T) {
			var records []pluginapi.UsageRecord
			client := &responseModelRPCClient{}
			var plugin pluginapi.UsagePlugin = usagePluginFunc(func(_ context.Context, record pluginapi.UsageRecord) {
				records = append(records, record)
			})
			if transport == "rpc" {
				plugin = &rpcPluginAdapter{id: "response-model-compat", client: client}
			}
			host := newHostWithRecords(capabilityRecord{
				id:     "response-model-compat",
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{UsagePlugin: plugin}},
			})
			adapter := &usageAdapter{host: host, pluginID: "response-model-compat"}
			record := coreusage.Record{
				Provider: "codex", Model: "requested-model", Alias: "client-alias",
				AuthID: "credential-a", APIKey: "synthetic-key", ServiceTier: "requested-tier",
				Generate: coreusage.GenerateFlag(false), Failed: true,
				Detail:          coreusage.Detail{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
				ResponseHeaders: http.Header{"X-Request-Id": {"synthetic-request"}},
			}
			adapter.HandleUsage(context.Background(), record)
			record.ResponseModel, record.ResponseServiceTier, record.Stream = "served-model", "reported-tier", true
			adapter.HandleUsage(context.Background(), record)
			record.ResponseModel, record.ResponseServiceTier, record.Stream = "", "", false
			adapter.HandleUsage(context.Background(), record)
			if transport == "rpc" {
				if len(client.requests) != 3 {
					t.Fatalf("RPC calls = %d, want exactly one per usage record", len(client.requests))
				}
				for i, payload := range client.requests {
					if client.methods[i] != pluginabi.MethodUsageHandle {
						t.Fatalf("RPC method = %q", client.methods[i])
					}
					var decoded pluginapi.UsageRecord
					if err := json.Unmarshal(payload, &decoded); err != nil {
						t.Fatal(err)
					}
					records = append(records, decoded)
				}
			}
			if len(records) != 3 {
				t.Fatalf("plugin deliveries = %d, want exactly one per usage record", len(records))
			}
			var legacyBaseline []byte
			for i, got := range records {
				wantModel, wantTier, wantStream := "", "", false
				if i == 1 {
					wantModel, wantTier, wantStream = "served-model", "reported-tier", true
				}
				if got.ResponseModel != wantModel || got.ResponseServiceTier != wantTier || got.Stream != wantStream {
					t.Fatalf("delivery %d response metadata = %q/%q/%v", i, got.ResponseModel, got.ResponseServiceTier, got.Stream)
				}
				if got.Model != "requested-model" || got.Alias != "client-alias" || got.AuthID != "credential-a" || got.APIKey != "synthetic-key" || got.ServiceTier != "requested-tier" || got.Detail.TotalTokens != 15 || got.Generate || !got.Failed {
					t.Fatalf("legacy accounting identity changed: %+v", got)
				}
				encoded, err := json.Marshal(got)
				if err != nil {
					t.Fatal(err)
				}
				// Released RPC consumers decode known fields and ignore additive metadata.
				var legacy struct {
					Model, Alias, AuthID, APIKey, ServiceTier string
					Generate, Failed                          bool
					Detail                                    struct{ InputTokens, OutputTokens, TotalTokens int64 }
				}
				if err := json.Unmarshal(encoded, &legacy); err != nil {
					t.Fatal(err)
				}
				if legacy.Model != record.Model || legacy.Alias != record.Alias || legacy.AuthID != record.AuthID || legacy.APIKey != record.APIKey || legacy.ServiceTier != record.ServiceTier || legacy.Detail.TotalTokens != 15 || legacy.Generate || !legacy.Failed {
					t.Fatalf("legacy JSON decoding changed billing fields: %+v", legacy)
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(encoded, &fields); err != nil {
					t.Fatal(err)
				}
				for _, field := range []string{"ResponseModel", "ResponseServiceTier", "Stream"} {
					if _, exists := fields[field]; !exists {
						t.Fatalf("missing native additive field %q", field)
					}
					delete(fields, field)
				}
				oldFields, err := json.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
				if i == 0 {
					legacyBaseline = oldFields
				} else if !bytes.Equal(oldFields, legacyBaseline) {
					t.Fatalf("additive metadata changed legacy payload fields: %s", oldFields)
				}
			}
		})
	}
}
