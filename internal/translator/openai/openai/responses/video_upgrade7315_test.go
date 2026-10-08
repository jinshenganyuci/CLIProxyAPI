package responses

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

func TestVideoUpgrade7315PreservesObjectBytesAndUnknownFields(t *testing.T) {
	const video = `{ "url" : "https://example.invalid/clip?sig=a%2Fb&mode=<raw>", "processing":"static", "vendor_frame":9223372036854775807, "vendor_meta":{"keep":"<>&\\u0000"} }`
	raw := []byte(`{"input":[{"role":"user","content":[{"type":"input_video","video_url":` + video + `}]}]}`)
	before := bytes.Clone(raw)
	for _, stream := range []bool{false, true} {
		out, errConvert := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("video-model", raw, stream)
		if errConvert != nil {
			t.Fatalf("request translation failed: %v", errConvert)
		}
		part := gjson.GetBytes(out, "messages.0.content.0.video_url")
		if part.Raw != video {
			t.Fatalf("video object was reserialized: got %s, want %s", part.Raw, video)
		}
		if !bytes.Equal(raw, before) {
			t.Fatal("input buffer was mutated")
		}
	}
}
