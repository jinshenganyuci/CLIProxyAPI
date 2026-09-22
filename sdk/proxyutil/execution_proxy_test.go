package proxyutil

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestResolveExecutionProxyPreservesCredentialRoute(t *testing.T) {
	for _, test := range []struct {
		name, request, credential, global, want string
		rejected                                bool
	}{
		{name: "credential", credential: "http://credential.example:8080", global: "http://global.example:8081", want: "http://credential.example:8080"},
		{name: "matching", request: "http://credential.example:8080", credential: "http://credential.example:8080", want: "http://credential.example:8080"},
		{name: "matching trimmed", request: " http://credential.example:8080 ", credential: "http://credential.example:8080", want: "http://credential.example:8080"},
		{name: "different endpoint", request: "http://request.example:8080", credential: "http://credential.example:8080", rejected: true},
		{name: "different proxy identity", request: "http://other:secret@proxy.example:8080", credential: "http://user:secret@proxy.example:8080", rejected: true},
		{name: "direct credential", request: "http://request.example:8080", credential: "direct", rejected: true},
		{name: "none credential", request: "http://request.example:8080", credential: "none", rejected: true},
		{name: "matching internal direct", request: "none", credential: "direct", want: "direct"},
		{name: "request above global", request: "socks5h://request.example:1080", global: "http://global.example:8081", want: "socks5h://request.example:1080"},
		{name: "invalid global overridden", request: "http://request.example:8080", global: "invalid://global", want: "http://request.example:8080"},
		{name: "global", global: "http://global.example:8081", want: "http://global.example:8081"},
		{name: "no route", want: ""},
		{name: "invalid scheme", request: "invalid://user:secret@proxy.example", rejected: true},
		{name: "invalid endpoint", request: "http://", rejected: true},
		{name: "invalid port zero", request: "http://proxy.example:0", rejected: true},
		{name: "invalid port range", request: "http://proxy.example:65536", rejected: true},
		{name: "invalid credential cannot replaced", request: "http://request.example:8080", credential: "invalid://credential", rejected: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, errResolve := ResolveExecutionProxy(test.request, test.credential, test.global)
			if !test.rejected {
				if errResolve != nil || got != test.want {
					t.Fatalf("resolved=%q err=%v want=%q", got, errResolve, test.want)
				}
				return
			}
			var policyErr *ExecutionProxyError
			if got != "" || !errors.As(errResolve, &policyErr) || policyErr.StatusCode() != http.StatusBadRequest || !policyErr.IsRequestScoped() {
				t.Fatalf("incorrect policy failure: route=%q err=%v", got, errResolve)
			}
			if strings.Contains(errResolve.Error(), "secret") || strings.Contains(errResolve.Error(), "proxy.example") {
				t.Fatal("proxy error exposed route credentials")
			}
		})
	}
}
