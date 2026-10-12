package registry

import "testing"

func TestV824ToolSearchCapabilitySnapshotsAreIsolated(t *testing.T) {
	for _, initial := range []bool{false, true} {
		name := "false"
		if initial {
			name = "true"
		}
		t.Run(name, func(t *testing.T) {
			reg := newTestModelRegistry()
			value := initial
			input := &ModelInfo{ID: "snapshot-tool-search", NativeCapabilities: &NativeCapabilities{ToolSearch: &value}}
			reg.RegisterClient("snapshot-client", "claude", []*ModelInfo{input})
			value = !initial
			first := reg.GetModelsForClient("snapshot-client")
			if len(first) != 1 || first[0].NativeCapabilities == nil || first[0].NativeCapabilities.ToolSearch == nil || *first[0].NativeCapabilities.ToolSearch != initial {
				t.Fatal("registered tool-search capability changed through input alias")
			}
			*first[0].NativeCapabilities.ToolSearch = !initial
			second := reg.GetModelsForClient("snapshot-client")
			if *second[0].NativeCapabilities.ToolSearch != initial {
				t.Fatal("registered tool-search capability changed through output snapshot")
			}
		})
	}
}
