package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
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
				id: "response-model-compat",
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
					UsagePlugin: plugin,
				}},
			})
			adapter := &usageAdapter{host: host, pluginID: "response-model-compat"}
			record := coreusage.Record{
				Provider: "codex", Model: "requested-model", Alias: "client-alias",
				AuthID: "credential-a", APIKey: "synthetic-key",
				Detail:          coreusage.Detail{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
				ResponseHeaders: http.Header{"X-Request-Id": {"synthetic-request"}},
			}
			adapter.HandleUsage(context.Background(), record)
			record.ResponseModel = "served-model"
			adapter.HandleUsage(context.Background(), record)
			if transport == "rpc" {
				if len(client.requests) != 2 {
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
				if !bytes.Equal(client.requests[0], client.requests[1]) {
					t.Fatalf("response metadata changed legacy RPC wire payload\n%s\n%s", client.requests[0], client.requests[1])
				}
			}
			if len(records) != 2 {
				t.Fatalf("plugin deliveries = %d, want exactly one per usage record", len(records))
			}
			if !reflect.DeepEqual(records[0], records[1]) {
				t.Fatalf("response metadata changed legacy plugin record\n%+v\n%+v", records[0], records[1])
			}
			got := records[1]
			if got.Model != "requested-model" || got.Alias != "client-alias" || got.AuthID != "credential-a" || got.APIKey != "synthetic-key" || got.Detail.TotalTokens != 15 {
				t.Fatalf("legacy accounting identity changed: %+v", got)
			}
			// The existing plugin ABI intentionally does not expose ResponseModel.
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"ResponseModel", "response_model"} {
				if _, exists := fields[field]; exists {
					t.Fatalf("unexpected legacy ABI extension %q", field)
				}
			}
		})
	}
}
