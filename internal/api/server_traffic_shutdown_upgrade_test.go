package api

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/traffic"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
)

func TestTrafficGracefulShutdownPersistsDrainedResponse(t *testing.T) {
	fx := newShutdownTestServer(t)
	fx.server.getConfig().ShutdownTimeoutSeconds = 5
	path := filepath.Join(t.TempDir(), "traffic.db")
	collector, errOpen := traffic.Open(traffic.Options{Enabled: true, Path: path, DisableBackground: true})
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	t.Cleanup(func() { _ = collector.Close(context.Background()) })
	fx.server.traffic.collector.Store(collector)
	manager := sdkaccess.NewManager()
	manager.SetProviders([]sdkaccess.Provider{trafficReplayAuthProvider{}})
	started, release := make(chan struct{}), make(chan struct{})
	const requestBody, responseBody = "request-before-shutdown", "response-during-graceful-drain"
	fx.server.server.Handler.(*gin.Engine).POST("/v1/traffic-drain", fx.server.trafficMiddleware(), AuthMiddleware(manager), func(c *gin.Context) {
		body, errRead := io.ReadAll(c.Request.Body)
		if errRead != nil || string(body) != requestBody {
			t.Errorf("request body = %q, error=%v", body, errRead)
		}
		close(started)
		select {
		case <-release:
		case <-c.Request.Context().Done():
		}
		c.String(http.StatusOK, responseBody)
	})
	requestDone := make(chan error, 1)
	go func() {
		request, errRequest := http.NewRequest(http.MethodPost, "http://"+fx.addr+"/v1/traffic-drain", strings.NewReader(requestBody))
		if errRequest != nil {
			requestDone <- errRequest
			return
		}
		request.Header.Set("X-Test-Key", "graceful-key")
		response, errRequest := http.DefaultClient.Do(request)
		if errRequest != nil {
			requestDone <- errRequest
			return
		}
		defer func() { _ = response.Body.Close() }()
		body, errRead := io.ReadAll(response.Body)
		if errRead == nil && (response.StatusCode != http.StatusOK || string(body) != responseBody) {
			errRead = errUnexpectedStatus(response.StatusCode)
		}
		requestDone <- errRead
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not enter the counted handler")
	}
	shutdownStarted := make(chan struct{})
	fx.server.server.RegisterOnShutdown(func() { close(shutdownStarted) })
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stopDone := make(chan error, 1)
	go func() { stopDone <- fx.server.Stop(stopCtx) }()
	select {
	case <-shutdownStarted:
	case <-stopCtx.Done():
		t.Fatal("graceful drain did not start")
	}
	close(release)
	if errStop := <-stopDone; errStop != nil {
		t.Fatal(errStop)
	}
	if errRequest := <-requestDone; errRequest != nil {
		t.Fatalf("drained request failed: %v", errRequest)
	}
	restored, errOpen := traffic.Open(traffic.Options{Enabled: true, Path: path, DisableBackground: true})
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	defer func() { _ = restored.Close(context.Background()) }()
	keys := restored.Keys(traffic.KeyQuery{Limit: 10})
	if len(keys.Keys) != 1 {
		t.Fatalf("persisted keys = %d, want 1", len(keys.Keys))
	}
	got := keys.Keys[0].Traffic.Total.Protocols[traffic.HTTP]
	if got.RxBytes != uint64(len(requestBody)) || got.TxBytes != uint64(len(responseBody)) {
		t.Fatalf("persisted drained traffic = %+v, want rx=%d tx=%d", got, len(requestBody), len(responseBody))
	}
}
