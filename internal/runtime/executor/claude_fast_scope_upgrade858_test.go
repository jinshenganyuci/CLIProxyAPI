package executor

import (
	"errors"
	"net/http"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestUpgrade858ClaudeFastDirectResponseSeparatesStopFromAvailabilityScope(t *testing.T) {
	for _, tc := range []struct {
		name              string
		status            int
		shared            bool
		modelLevelCooling bool
		wantRequest       bool
		wantCredential    bool
		wantModel         bool
	}{
		{name: "opaque400", status: 400, wantRequest: true},
		{name: "opaque401", status: 401, wantRequest: true},
		{name: "opaque429", status: 429, wantRequest: true},
		{name: "credential429", status: 429, shared: true, wantCredential: true},
		{name: "model429", status: 429, shared: true, modelLevelCooling: true, wantModel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const original = "{\n  \"error\": {\"type\":\"rate_limit_error\",\"message\":\"synthetic original response\"}\n}"
			body := []byte(original)
			headers := http.Header{"X-Fixture": {"preserved"}, "Content-Length": {"999"}, "Content-Encoding": {"gzip"}}
			if tc.shared {
				headers.Set("Anthropic-Ratelimit-Unified-Status", "rejected")
				headers.Set("Anthropic-Ratelimit-Unified-5h-Status", "rejected")
				headers.Set("Retry-After", "120")
			}
			err := newClaudeFastDirectResponseError(&http.Response{StatusCode: tc.status, Header: headers}, body, tc.modelLevelCooling)
			var scoped interface{ IsRequestScoped() bool }
			var credential interface{ IsCredentialScoped() bool }
			var model interface{ IsModelScoped() bool }
			var stop interface{ IsRequestStop() bool }
			if !errors.As(err, &stop) || !stop.IsRequestStop() {
				t.Fatal("Fast direct response must stop the request independently of cooldown scope")
			}
			if !errors.As(err, &scoped) || scoped.IsRequestScoped() != tc.wantRequest || !errors.As(err, &credential) || credential.IsCredentialScoped() != tc.wantCredential || !errors.As(err, &model) || model.IsModelScoped() != tc.wantModel {
				t.Fatal("Fast upstream scope differs from verified quota headers and model-level policy")
			}
			var terminal *cliproxyexecutor.RequestTerminatedError
			if !errors.As(err, &terminal) || terminal.HTTPStatus != tc.status || string(terminal.Body) != original || terminal.Header.Get("X-Fixture") != "preserved" {
				t.Fatal("original HTTP status, headers or body was replaced")
			}
			if terminal.Header.Get("Content-Length") != "" || terminal.Header.Get("Content-Encoding") != "" {
				t.Fatal("decoded response retained stale wire representation headers")
			}
			body[0] = '!'
			headers.Set("X-Fixture", "mutated")
			if string(terminal.Body) != original || terminal.Header.Get("X-Fixture") != "preserved" {
				t.Fatal("terminal response did not own its original body and header snapshot")
			}
		})
	}
}
