package config

import "testing"

func TestV827NewOptionsCanonicalMigrationAndDefaults(t *testing.T) {
	for _, tc := range []struct {
		name, raw, rebind, identifier string
		body                          int64
		preToken                      bool
	}{
		{name: "defaults", raw: "config-version: 8\n", identifier: DefaultSignInWithChatGPTIdentifier},
		{name: "canonical", raw: "config-version: 8\nserver: {max-request-body-bytes: 4096}\nrouting: {session-affinity-rebind-on: quota}\nrequests: {streaming: {send-keepalive-before-ft: true}}\noauth: {providers: {codex: {signin-with-chatgpt-identifier: ' synthetic '}}}\n", body: 4096, rebind: "quota", identifier: "synthetic", preToken: true},
		{name: "legacy-negative", raw: "max-request-body-bytes: -1\nrouting: {session-affinity-rebind-on: any}\nstreaming: {send-keepalive-before-ft: false}\ncodex: {signin-with-chatgpt-identifier: ''}\n", body: -1, rebind: "any", identifier: DefaultSignInWithChatGPTIdentifier},
	} {
		t.Run(tc.name, func(t *testing.T) {
			migrated, _, errNormalize := NormalizeConfigLayout([]byte(tc.raw), true)
			if errNormalize != nil {
				t.Fatal(errNormalize)
			}
			if errValidate := ValidateV8Config(migrated); errValidate != nil {
				t.Fatal(errValidate)
			}
			cfg, errParse := ParseConfigBytes(migrated)
			if errParse != nil {
				t.Fatal(errParse)
			}
			for _, snapshot := range []*Config{cfg, cfg.CloneForRuntime()} {
				if snapshot.MaxRequestBodyBytes != tc.body || snapshot.Routing.SessionAffinityRebindOn != tc.rebind || snapshot.Streaming.SendKeepAliveBeforeFT != tc.preToken || snapshot.Codex.EffectiveSignInWithChatGPTIdentifier() != tc.identifier {
					t.Fatal("migration or runtime cloning changed a new optional setting")
				}
			}
		})
	}
}
