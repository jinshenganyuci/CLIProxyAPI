package helps

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestAntigravityTraceRetainsProxyAndNeverFallsBackUpgrade812(t *testing.T) {
	logger := log.StandardLogger()
	oldLevel, oldOutput := logger.GetLevel(), logger.Out
	oldHooks := logger.ReplaceHooks(make(log.LevelHooks))
	logger.SetLevel(log.DebugLevel)
	logger.SetOutput(io.Discard)
	hook := logtest.NewLocal(logger)
	t.Cleanup(func() { logger.SetLevel(oldLevel); logger.SetOutput(oldOutput); logger.ReplaceHooks(oldHooks) })
	var directCalls, proxyCalls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { directCalls.Add(1); _, _ = io.WriteString(w, "direct") }))
	defer origin.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		proxyCalls.Add(1)
		if request.Header.Get("Proxy-Authorization") == "" {
			t.Error("selected authenticated proxy lost credentials")
		}
		_, _ = io.WriteString(w, "selected")
	}))
	defer proxy.Close()
	proxyURL, errParse := url.Parse(proxy.URL)
	if errParse != nil {
		t.Fatal(errParse)
	}
	proxyURL.User = url.UserPassword("secret-user", "secret-password")
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	traced := WithAntigravityHTTPClientTrace(client, &coreauth.Auth{Index: "safe-index", ID: "secret-id", ProxyURL: proxyURL.String(), Metadata: map[string]any{"access_token": "secret-token"}}, "generate")
	request, errRequest := http.NewRequestWithContext(logging.WithRequestID(context.Background(), "safe-request"), http.MethodGet, origin.URL+"/secret-path?key=secret-query", nil)
	if errRequest != nil {
		t.Fatal(errRequest)
	}
	request.Header.Set("Authorization", "Bearer secret-header")
	response, errDo := traced.Do(request)
	if errDo != nil {
		t.Fatal(errDo)
	}
	data, errRead := io.ReadAll(response.Body)
	errClose := response.Body.Close()
	if errRead != nil || errClose != nil || string(data) != "selected" || proxyCalls.Load() != 1 || directCalls.Load() != 0 {
		t.Fatal("tracing changed selected proxy routing")
	}
	if client.Transport != transport {
		t.Fatal("trace wrapper mutated original transport")
	}
	for _, entry := range hook.AllEntries() {
		formatted, errFormat := (&logging.LogFormatter{}).Format(entry)
		if errFormat != nil || strings.Contains(string(formatted), "secret") {
			t.Fatal("connection tracing leaked a credential, URL path or query")
		}
	}
	if len(hook.AllEntries()) != 1 {
		t.Fatal("expected exactly one connection trace")
	}
	proxy.Close()
	traced.CloseIdleConnections()
	if response, errDo = traced.Do(request.Clone(request.Context())); errDo == nil {
		_ = response.Body.Close()
		t.Fatal("failed selected proxy unexpectedly succeeded")
	}
	if directCalls.Load() != 0 {
		t.Fatal("failed traced proxy fell back to direct")
	}
}
