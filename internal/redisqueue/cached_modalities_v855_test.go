package redisqueue

import (
	"context"
	"testing"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestV855UsageQueuePreservesOptionalCachedModalities(t *testing.T) {
	for _, tc := range []struct {
		name         string
		image, audio *int64
	}{
		{name: "omitted"},
		{name: "explicit-zero", image: coreusage.Int64Ptr(0), audio: coreusage.Int64Ptr(0)},
		{name: "nonzero", image: coreusage.Int64Ptr(2), audio: coreusage.Int64Ptr(3)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withEnabledQueue(t, func() {
				wantImage, wantAudio := coreusage.CloneInt64Ptr(tc.image), coreusage.CloneInt64Ptr(tc.audio)
				(&usageQueuePlugin{}).HandleUsage(context.Background(), coreusage.Record{
					Provider: "gemini", Model: "test", AuthIndex: "selected-credential",
					Detail: coreusage.Detail{InputTokens: 10, CachedTokens: 5, OutputTokens: 4, TotalTokens: 14, CachedImageTokens: tc.image, CachedAudioTokens: tc.audio},
				})
				if tc.image != nil {
					*tc.image = 99
				}
				if tc.audio != nil {
					*tc.audio = 99
				}
				payload := popSinglePayload(t)
				tokens := requireTokensPayload(t, payload)
				for name, want := range map[string]*int64{"cached_image_tokens": wantImage, "cached_audio_tokens": wantAudio} {
					if want == nil {
						requireMissingField(t, tokens, name)
					} else {
						requireIntField(t, tokens, name, int(*want))
					}
				}
				requireIntField(t, tokens, "input_tokens", 10)
				requireIntField(t, tokens, "cached_tokens", 5)
				requireIntField(t, tokens, "total_tokens", 14)
				requireStringField(t, payload, "auth_index", "selected-credential")
			})
		})
	}
}
