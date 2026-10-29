package pluginhost

import (
	"bytes"
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestV847RawPatchPreservesCallerIsolationAndOpaqueBytes(t *testing.T) {
	original := []byte("{\n  \"model\":\"synthetic\", \"text\":\"汉字 old\", \"opaque\":9007199254740993, \"proxy_url\":\"direct\"\n}")
	patchCalls, metadataCalls, legacyCalls := 0, 0, 0
	patcher := funcRequestBodyPatcher(func(_ context.Context, body []byte) (pluginapi.RequestBodyPatchResponse, error) {
		patchCalls++
		offset := bytes.Index(body, []byte("old"))
		if offset < 0 {
			t.Fatal("patch input changed")
		}
		// A native plugin receives an isolated copy, while edits remain relative to
		// the exact raw input, including multibyte text and large opaque numbers.
		body[0] = 'X'
		return pluginapi.RequestBodyPatchResponse{Edits: []pluginapi.RequestBodyEdit{{Start: offset, End: offset + 3, Replacement: "ignored", ReplacementBytes: []byte("new")}}}, nil
	})
	host := newHostWithRecords(capabilityRecord{id: "patcher", plugin: pluginapi.Plugin{SchemaVersion: pluginabi.SchemaVersionRequestInterceptorBodyPatch, Capabilities: pluginapi.Capabilities{
		RequestBodyPatcherBefore: patcher, RequestBodyPatcherAfter: patcher, RequestInterceptorOmitBodyBefore: true, RequestInterceptorOmitBodyAfter: true,
		RequestInterceptor: requestInterceptorFunc(func(_ context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
			metadataCalls++
			if req.Body != nil || req.CallerScope != "caller-v847" || req.Metadata[pluginapi.CallerScopeMetadataKey] != "caller-v847" {
				t.Fatal("metadata-only patch phase lost caller scope")
			}
			req.Metadata["auth_index"] = "must-not-escape"
			return pluginapi.RequestInterceptResponse{}, nil
		}),
	}}}, capabilityRecord{id: "observer", plugin: pluginapi.Plugin{SchemaVersion: 6, Capabilities: pluginapi.Capabilities{RequestInterceptorOmitBodyBefore: true, RequestInterceptorOmitBodyAfter: true, RequestInterceptor: requestInterceptorFunc(func(_ context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
		legacyCalls++
		if !bytes.Contains(req.Body, []byte("9007199254740993")) || req.Metadata["auth_index"] != "selected" {
			t.Fatal("legacy/body or selection isolation changed")
		}
		return pluginapi.RequestInterceptResponse{}, nil
	})}}})
	req := pluginapi.RequestInterceptRequest{Body: original, Metadata: map[string]any{pluginapi.CallerScopeMetadataKey: "caller-v847", "auth_index": "selected"}}
	expected := bytes.Replace(original, []byte("old"), []byte("new"), 1)
	for _, before := range []bool{true, false} {
		var out pluginapi.RequestInterceptResponse
		if before {
			out = host.InterceptRequestBeforeAuth(context.Background(), req)
		} else {
			out = host.InterceptRequestAfterAuth(context.Background(), req)
		}
		if !bytes.Equal(out.Body, expected) || original[0] != '{' || req.Metadata["auth_index"] != "selected" {
			t.Fatal("raw patch reserialized bytes or leaked plugin mutations")
		}
	}
	replay := host.InterceptRequestBeforeAuthExcept(context.Background(), req, "patcher")
	if replay.Body != nil || patchCalls != 2 || metadataCalls != 2 || legacyCalls != 3 {
		t.Fatal("nested replay invoked its originating patcher or omitted legacy observer")
	}
}
