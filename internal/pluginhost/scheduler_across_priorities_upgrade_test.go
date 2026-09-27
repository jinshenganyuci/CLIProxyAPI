package pluginhost

import (
	"context"
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestSchedulerAcrossPrioritiesRPCCompatibilityUpgrade(t *testing.T) {
	for _, schemaVersion := range []uint32{1, pluginabi.SchemaVersion} {
		for _, acrossPriorities := range []bool{false, true} {
			t.Run(fmt.Sprintf("schema-%d/across-%t", schemaVersion, acrossPriorities), func(t *testing.T) {
				plugin := validTestPlugin("priority-compat")
				plugin.Capabilities.SchedulerAcrossPriorities = acrossPriorities
				plugin.Capabilities.Scheduler = schedulerFunc(func(_ context.Context, request pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
					if len(request.Candidates) != 1 || request.Candidates[0].ID != "candidate" {
						t.Fatalf("RPC candidates changed: %#v", request.Candidates)
					}
					return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "candidate"}, nil
				})
				lookup := newTestSymbolLookup(&testPlugin{registerResult: plugin})
				lookup.schemaVersion = schemaVersion
				registered, errRegister := registerRPCPlugin(context.Background(), nil, "priority-compat", lookup, pluginabi.MethodPluginRegister, nil)
				if errRegister != nil {
					t.Fatal(errRegister)
				}
				if registered.SchemaVersion != schemaVersion || schedulerWantsAcrossPriorities(registered.Capabilities) != acrossPriorities {
					t.Fatalf("RPC capability/schema changed: %#v", registered)
				}
				response, errPick := registered.Capabilities.Scheduler.Pick(context.Background(), pluginapi.SchedulerPickRequest{
					Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "candidate", Priority: 10}},
				})
				if errPick != nil || !response.Handled || response.AuthID != "candidate" {
					t.Fatalf("RPC scheduler response/error = %#v/%v", response, errPick)
				}
			})
		}
	}
}
