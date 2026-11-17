package responses

import (
	"strings"
	"testing"

	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/ojson"
)

func TestUpgrade875GeminiImagesDoNotRetargetSignatures(t *testing.T) {
	imageBytes, errDecode := base64.StdEncoding.DecodeString(testResponsesGeminiThoughtSignature)
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	imageBytes[len(imageBytes)-1] ^= 1
	imageSignature := base64.StdEncoding.EncodeToString(imageBytes)
	for _, tc := range []struct {
		name       string
		prefix     string
		wantSigned bool
		tool       bool
	}{
		{name: "signed text", prefix: `{"text":"before-image","thoughtSignature":"` + testResponsesGeminiThoughtSignature + `"}`, wantSigned: true},
		{name: "unsigned text", prefix: `{"text":"before-image"}`},
		{name: "signed tool", prefix: `{"functionCall":{"id":"call_before","name":"inspect","args":{}},"thoughtSignature":"` + testResponsesGeminiThoughtSignature + `"}`, wantSigned: true, tool: true},
		{name: "unsigned thought", prefix: `{"thought":true,"text":"before-image"}`},
	} {
		for _, stream := range []bool{false, true} {
			mode := "nonstream"
			if stream {
				mode = "stream"
			}
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				rawDoc := ojson.NewDocString(`{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3},"modelVersion":"gemini-3.6-flash-high"}`)
				_ = rawDoc.Set("responseId", t.Name())
				_ = rawDoc.SetRaw("candidates.0.content.parts", `[`+tc.prefix+`,{"inlineData":{"mimeType":"image/png","data":"aW1hZ2U="},"thoughtSignature":"`+imageSignature+`"},{"text":"after-image"}]`)
				var output ojson.Result
				if stream {
					var param any
					events := ConvertGeminiResponseToOpenAIResponses(t.Context(), "gemini-3.6-flash-high", nil, nil, append([]byte("data: "), rawDoc.Bytes()...), &param)
					events = append(events, ConvertGeminiResponseToOpenAIResponses(t.Context(), "gemini-3.6-flash-high", nil, nil, []byte("[DONE]"), &param)...)
					for _, event := range events {
						name, data := parseSSEEvent(t, event)
						if name == "response.completed" {
							output = data.Get("response.output")
						}
					}
				} else {
					response := ConvertGeminiResponseToOpenAIResponsesNonStream(t.Context(), "gemini-3.6-flash-high", nil, nil, rawDoc.Bytes(), nil)
					output = ojson.GetBytesNoCopy(response, "output")
				}
				if !output.IsArray() {
					t.Fatal("missing completed output")
				}
				items := output.Array()
				images, imageCarriers := 0, 0
				for index, item := range items {
					if item.Get("type").String() == "image_generation_call" {
						images++
						if item.Get("result").String() != "aW1hZ2U=" {
							t.Fatalf("image data changed: %s", item.Raw)
						}
					}
					signature, direction, target, marked, ok := decodeGeminiResponsesCarrier(item.Get("encrypted_content").String())
					if signature == imageSignature {
						imageCarriers++
						if !marked || !ok || direction != geminiResponsesCarrierNext || target != geminiResponsesCarrierAny || index+1 >= len(items) || items[index+1].Get("type").String() != "image_generation_call" {
							t.Fatalf("image signature escaped its image boundary: %s", output.Raw)
						}
					}
				}
				if images != 1 || imageCarriers != 1 {
					t.Fatalf("images/carriers = %d/%d, want 1/1: %s", images, imageCarriers, output.Raw)
				}
				requestDoc := ojson.NewDocString(`{"model":"gemini-3.6-flash-high","input":[]}`)
				_ = requestDoc.SetRaw("input", output.Raw)
				for _, item := range items {
					if item.Get("type").String() == "function_call" {
						reply := ojson.NewDocString(`{"type":"function_call_output","call_id":"","output":"ok"}`)
						_ = reply.Set("call_id", item.Get("call_id").String())
						_ = requestDoc.SetRawBytes("input.-1", reply.Bytes())
					}
				}
				// Replay a completed turn before a new user request, rather than
				// an unsigned assistant prefill that the translator intentionally strips.
				_ = requestDoc.SetRaw("input.-1", `{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}`)
				replayed, errReplay := ConvertOpenAIResponsesRequestToGemini("gemini-3.6-flash-high", requestDoc.Bytes(), false)
				if errReplay != nil {
					t.Fatal(errReplay)
				}
				if strings.Contains(string(replayed), imageSignature) {
					t.Fatalf("unsupported image signature was assigned to a replay target: %s", replayed)
				}
				originalTargetFound, afterFound := false, false
				for _, content := range ojson.GetBytesNoCopy(replayed, "contents").Array() {
					for _, part := range content.Get("parts").Array() {
						if part.Get("text").String() == "after-image" {
							afterFound = true
						}
						if (tc.tool && part.Get("functionCall.name").String() == "inspect") || (!tc.tool && part.Get("text").String() == "before-image") {
							originalTargetFound = true
							if tc.wantSigned && part.Get("thoughtSignature").String() != testResponsesGeminiThoughtSignature {
								t.Fatalf("original target signature changed: %s", replayed)
							}
						}
					}
				}
				if !originalTargetFound || !afterFound {
					t.Fatalf("neighboring content was lost: %s", replayed)
				}
			})
		}
	}
}
