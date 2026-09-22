package translator

import (
	"bytes"
	"context"
	"testing"
)

func TestRegistryUpgrade7312UnregisterDoesNotCorruptOtherPairs(t *testing.T) {
	registry := NewRegistry()
	from, to, other := Format("upgrade-source"), Format("upgrade-target"), Format("upgrade-other")
	entered := make(chan struct{})
	release := make(chan struct{})
	result := make(chan []byte, 1)
	registry.Register(from, to, func(_ string, body []byte, _ bool) []byte {
		close(entered)
		<-release
		return []byte(`{"handled":"selected"}`)
	}, ResponseTransform{})
	registry.Register(from, other, func(_ string, body []byte, _ bool) []byte { return []byte(`{"handled":"other"}`) }, ResponseTransform{})
	go func() { result <- registry.TranslateRequest(from, to, "", []byte(`{"input":"original"}`), false) }()
	<-entered
	registry.Unregister(from, to)
	if registry.HasRequestTransformer(from, to) || !registry.HasRequestTransformer(from, other) {
		t.Fatal("unregister removed wrong pair")
	}
	input := []byte(`{"input":"fallback"}`)
	output := registry.TranslateRequestEnvelope(context.Background(), from, to, RequestEnvelope{Body: input})
	if !bytes.Equal(output.Body, input) {
		t.Fatalf("removed transform was still selected: %s", output.Body)
	}
	close(release)
	if got := <-result; string(got) != `{"handled":"selected"}` {
		t.Fatalf("in-flight transform changed: %s", got)
	}
	if got := registry.TranslateRequest(from, other, "", nil, false); string(got) != `{"handled":"other"}` {
		t.Fatalf("other pair changed: %s", got)
	}
}
