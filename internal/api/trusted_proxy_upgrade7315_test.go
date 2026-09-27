package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"golang.org/x/crypto/bcrypt"
)

func TestUpgrade7315TrustedProxyMetadataBoundary(t *testing.T) {
	tests := []struct {
		name, peer, forwarded, realIP, wantIP string
		trusted                               []string
	}{
		{name: "default ignores spoofed loopback", peer: "198.51.100.20:1234", forwarded: "127.0.0.1", realIP: "127.0.0.1", wantIP: "198.51.100.20"},
		{name: "trusted ipv4", peer: "192.0.2.10:1234", forwarded: "203.0.113.5", wantIP: "203.0.113.5", trusted: []string{"192.0.2.0/24"}},
		{name: "chain stops at untrusted hop", peer: "192.0.2.10:1234", forwarded: "127.0.0.1, 198.51.100.8", wantIP: "198.51.100.8", trusted: []string{"192.0.2.0/24"}},
		{name: "untrusted peer ignores both headers", peer: "198.51.100.20:1234", forwarded: "127.0.0.1", realIP: "127.0.0.1", wantIP: "198.51.100.20", trusted: []string{"192.0.2.0/24"}},
		{name: "trusted ipv6", peer: "[2001:db8::1]:1234", forwarded: "2001:db8:1::2", wantIP: "2001:db8:1::2", trusted: []string{"2001:db8::/64"}},
		{name: "invalid config fails closed", peer: "192.0.2.10:1234", forwarded: "127.0.0.1", wantIP: "192.0.2.10", trusted: []string{"not-an-ip"}},
		{name: "trusted real IP fallback", peer: "192.0.2.10:1234", realIP: "203.0.113.5", wantIP: "203.0.113.5", trusted: []string{"192.0.2.0/24"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestServerWithConfig(t, &proxyconfig.Config{TrustedProxies: tt.trusted})
			s.engine.GET("/upgrade-metadata", func(c *gin.Context) {
				ctx, cancel := s.handlers.GetContextWithCancel(nil, c, context.Background())
				defer cancel()
				c.JSON(http.StatusOK, logging.GetClientRequestMetadata(ctx))
			})
			req := httptest.NewRequest(http.MethodGet, "/upgrade-metadata", nil)
			req.RemoteAddr = tt.peer
			req.Header.Set("X-Forwarded-For", tt.forwarded)
			req.Header.Set("X-Real-IP", tt.realIP)
			req.Header.Set("Session-Id", "73150000-0000-4000-8000-000000000001")
			recorder := httptest.NewRecorder()
			s.engine.ServeHTTP(recorder, req)
			var metadata logging.ClientRequestMetadata
			if err := json.Unmarshal(recorder.Body.Bytes(), &metadata); err != nil {
				t.Fatal(err)
			}
			if metadata.ResolvedClientIP != tt.wantIP || metadata.XForwardedFor != tt.forwarded || metadata.SessionID != "codex:73150000-0000-4000-8000-000000000001" {
				t.Fatalf("unexpected metadata: %+v", metadata)
			}
			peerIP, _, errSplit := net.SplitHostPort(tt.peer)
			if errSplit != nil {
				t.Fatal(errSplit)
			}
			if metadata.ClientIP != peerIP {
				t.Fatalf("direct peer must remain independent: %+v", metadata)
			}
		})
	}
}

func TestUpgrade7315ManagementRejectsUntrustedLoopbackSpoof(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	hash, errHash := bcrypt.GenerateFromPassword([]byte("synthetic-upgrade-key"), bcrypt.MinCost)
	if errHash != nil {
		t.Fatal(errHash)
	}
	cfg := &proxyconfig.Config{}
	cfg.RemoteManagement.SecretKey = string(hash)
	s := newTestServerWithConfig(t, cfg)
	for _, tt := range []struct {
		name, peer, forwarded, key string
		want                       int
	}{
		{"spoof cannot bypass remote restriction", "198.51.100.20:1234", "127.0.0.1", "synthetic-upgrade-key", http.StatusForbidden},
		{"loopback still needs key", "127.0.0.1:1234", "", "", http.StatusUnauthorized},
		{"local valid key remains usable", "127.0.0.1:1234", "", "synthetic-upgrade-key", http.StatusOK},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v0/management/config", nil)
			req.RemoteAddr = tt.peer
			req.Header.Set("X-Forwarded-For", tt.forwarded)
			req.Header.Set("X-Real-IP", tt.forwarded)
			if tt.key != "" {
				req.Header.Set("Authorization", "Bearer "+tt.key)
			}
			recorder := httptest.NewRecorder()
			s.engine.ServeHTTP(recorder, req)
			if recorder.Code != tt.want {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, tt.want, recorder.Body.String())
			}
		})
	}
}
