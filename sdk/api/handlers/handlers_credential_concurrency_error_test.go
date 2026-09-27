package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestBuildErrorResponseBodyPreservesLocalCredentialConcurrencyCode(t *testing.T) {
	busy := &coreauth.Error{
		Code:       "credential_concurrency_exceeded",
		Message:    "all eligible credentials are at their concurrency limit",
		HTTPStatus: http.StatusTooManyRequests,
	}
	for _, test := range []struct {
		name    string
		err     error
		errText string
		code    string
	}{
		{name: "local admission", err: busy, errText: busy.Error(), code: busy.Code},
		{name: "wrapped local admission", err: fmt.Errorf("execute: %w", busy), errText: busy.Error(), code: busy.Code},
		{name: "structured local explanation", err: busy, errText: `{"error":{"code":"ignored","message":"busy"}}`, code: busy.Code},
		{name: "upstream plain quota", err: errors.New("quota exhausted"), errText: "quota exhausted", code: "rate_limit_exceeded"},
		{name: "untyped matching text", err: errors.New(busy.Error()), errText: busy.Error(), code: "rate_limit_exceeded"},
		{name: "upstream structured quota", err: errors.New("quota exhausted"), errText: `{"error":{"type":"rate_limit_error","code":"usage_limit_reached","message":"quota exhausted"}}`, code: "usage_limit_reached"},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := BuildErrorResponseBodyWithError(http.StatusTooManyRequests, test.errText, test.err)
			if got := gjson.GetBytes(body, "error.code").String(); got != test.code {
				t.Fatalf("error code = %q, want %q: %s", got, test.code, body)
			}
			if coreauth.IsCredentialConcurrencyError(test.err) {
				if got := gjson.GetBytes(body, "error.type").String(); got != "rate_limit_error" {
					t.Fatalf("local admission error type = %q", got)
				}
				if got := gjson.GetBytes(body, "error.message").String(); got != test.errText {
					t.Fatalf("local admission message = %q, want %q", got, test.errText)
				}
			}
		})
	}
}
