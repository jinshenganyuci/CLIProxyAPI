package api

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/httpwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/traffic"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

type serverTrafficState struct {
	collector         atomic.Pointer[traffic.Collector]
	mu                sync.Mutex
	started, stopping bool
	path              string
	cancel            context.CancelFunc
	done              chan struct{}
	requests          sync.WaitGroup
	connections       map[*trafficTrackedConn]struct{}
}

func newServerTrafficState() *serverTrafficState {
	return &serverTrafficState{connections: make(map[*trafficTrackedConn]struct{})}
}

// Start only with the serving lifecycle, not when constructing a router.
func (s *Server) startTraffic() {
	if s.traffic == nil {
		return
	}
	state := s.traffic
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.started || state.stopping {
		return
	}
	cfg := s.getConfig()
	if cfg == nil {
		return
	}
	state.path = filepath.Join(cfg.AuthDir, "traffic.db")
	collector, err := traffic.Open(traffic.Options{Enabled: cfg.Traffic.IsEnabled(), Path: state.path, Interface: cfg.Traffic.Interface})
	if err != nil {
		log.WithError(err).Warn("traffic statistics persistence unavailable; API service remains available")
	}
	state.collector.Store(collector)
	state.started = true
	ctx, cancel := context.WithCancel(context.Background())
	state.cancel, state.done = cancel, make(chan struct{})
	go func() {
		defer close(state.done)
		s.syncTrafficKeys(ctx, collector)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.syncTrafficKeys(ctx, collector)
			}
		}
	}()
}

func (s *Server) reconfigureTraffic(cfg *config.Config) {
	if s.traffic == nil || cfg == nil {
		return
	}
	if collector := s.traffic.collector.Load(); collector != nil {
		collector.Configure(cfg.Traffic.IsEnabled(), cfg.Traffic.Interface)
	}
}

func (s *Server) stopTrafficAdmission() {
	if s.traffic == nil {
		return
	}
	state := s.traffic
	state.mu.Lock()
	state.stopping = true
	if state.cancel != nil {
		state.cancel()
	}
	connections := make([]*trafficTrackedConn, 0, len(state.connections))
	for conn := range state.connections {
		connections = append(connections, conn)
	}
	state.mu.Unlock()
	for _, conn := range connections {
		_ = conn.Close()
	}
}

func (s *Server) stopTraffic(ctx context.Context) error {
	if s.traffic == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	state := s.traffic
	state.mu.Lock()
	directoryDone := state.done
	state.mu.Unlock()
	if directoryDone != nil {
		select {
		case <-directoryDone:
		case <-ctx.Done():
		}
	}
	finished := make(chan struct{})
	go func() { state.requests.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-ctx.Done():
	}
	if collector := state.collector.Load(); collector != nil {
		return collector.Close(ctx)
	}
	return nil
}

func trafficBusinessPath(path string) bool {
	// The provider websocket multiplexes upstream calls; it is not a client API.
	if path == "/v1/ws" {
		return false
	}
	return strings.HasPrefix(path, "/v1/") || strings.HasPrefix(path, "/v1beta/") ||
		strings.HasPrefix(path, "/openai/v1/") || strings.HasPrefix(path, "/backend-api/codex/") ||
		strings.HasPrefix(path, "/api/provider/")
}

func (s *Server) trafficMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s.traffic == nil || !trafficBusinessPath(c.Request.URL.Path) {
			c.Next()
			return
		}
		state := s.traffic
		collector := state.collector.Load()
		if collector == nil {
			c.Next()
			return
		}
		meter := collector.NewMeter()
		if meter == nil {
			c.Next()
			return
		}
		state.mu.Lock()
		if state.stopping {
			state.mu.Unlock()
			c.Next()
			return
		}
		state.requests.Add(1)
		state.mu.Unlock()
		defer state.requests.Done()
		defer meter.Finish()
		c.Request = c.Request.WithContext(traffic.WithMeter(c.Request.Context(), meter))
		// This wrapper precedes request logging and frontend authentication. Their
		// subsequent in-memory body replays must not count a second ingress.
		if c.Request.Body != nil {
			c.Request.Body = &trafficRequestBody{ReadCloser: c.Request.Body, meter: meter}
		}
		original := c.Writer
		c.Writer = &trafficResponseWriter{ResponseWriter: original, meter: meter, state: state}
		defer func() { c.Writer = original }()
		c.Next()
	}
}

type trafficRequestBody struct {
	io.ReadCloser
	meter *traffic.Meter
}

func (r *trafficRequestBody) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.meter.AddRx(traffic.HTTP, n)
	return n, err
}

type trafficResponseWriter struct {
	gin.ResponseWriter
	meter *traffic.Meter
	state *serverTrafficState
}

func (w *trafficResponseWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	w.meter.AddTx(traffic.HTTP, n)
	return n, err
}
func (w *trafficResponseWriter) WriteString(value string) (int, error) {
	n, err := w.ResponseWriter.WriteString(value)
	w.meter.AddTx(traffic.HTTP, n)
	return n, err
}
func (w *trafficResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *trafficResponseWriter) FlushError() error           { return httpwire.FlushResponse(w.ResponseWriter) }
func (w *trafficResponseWriter) Flush()                      { _ = w.FlushError() }
func (w *trafficResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := w.ResponseWriter.Hijack()
	if err != nil {
		return conn, rw, err
	}
	tracked := &trafficTrackedConn{Conn: conn, state: w.state}
	w.state.mu.Lock()
	stopping := w.state.stopping
	if !stopping {
		w.state.connections[tracked] = struct{}{}
	}
	w.state.mu.Unlock()
	if stopping {
		_ = tracked.Close()
		return nil, nil, net.ErrClosed
	}
	// Preserve the original buffered reader/writer, including any bytes already
	// read with the upgrade. Message counters own websocket payload accounting.
	return tracked, rw, nil
}

type trafficTrackedConn struct {
	net.Conn
	state *serverTrafficState
	once  sync.Once
	err   error
}

func (c *trafficTrackedConn) Close() error {
	c.once.Do(func() {
		c.err = c.Conn.Close()
		c.state.mu.Lock()
		delete(c.state.connections, c)
		c.state.mu.Unlock()
	})
	return c.err
}

func bindTrafficIdentity(ctx context.Context, provider, principal string, metadata map[string]string) {
	meter := traffic.FromContext(ctx)
	if meter == nil || principal == "" {
		return
	}
	identity := traffic.Identity{Provider: provider}
	if coreusage.IsNativeAccessProvider(provider) {
		identity.Provider = sdkaccess.DefaultAccessProviderName
		identity.Key = principal
		identity.Mask = maskTrafficKey(principal)
	} else if keyID := strings.TrimSpace(metadata["key_id"]); keyID != "" {
		identity.Key = keyID
	} else if strings.HasPrefix(provider, "plugin:access-guard:") {
		// Realtime client secrets retain the verified issuer's plugin key ID as
		// their principal, even when frontend-auth metadata is no longer present.
		identity.Key = principal
	} else {
		// A generic plugin principal can be an account shared by multiple keys.
		// Without a trusted per-key ID it must remain explicitly unattributed.
		return
	}
	meter.Bind(identity)
}

func maskTrafficKey(key string) string {
	if len(key) < 10 {
		return "••••"
	}
	return key[:3] + "…" + key[len(key)-4:]
}

func (s *Server) syncTrafficKeys(ctx context.Context, collector *traffic.Collector) {
	if collector == nil || ctx.Err() != nil {
		return
	}
	cfg := s.getConfig()
	if cfg == nil {
		return
	}
	identities := make([]traffic.Identity, 0, len(cfg.APIKeys))
	for _, key := range cfg.APIKeys {
		if key = strings.TrimSpace(key); key != "" {
			identities = append(identities, traffic.Identity{Provider: sdkaccess.DefaultAccessProviderName, Key: key, Mask: maskTrafficKey(key)})
		}
	}
	collector.SyncNativeKeys(identities)
	if s.pluginHost == nil || s.accessManager == nil {
		return
	}
	providerID := ""
	for _, provider := range s.accessManager.Providers() {
		if provider != nil && strings.HasPrefix(provider.Identifier(), "plugin:access-guard:") {
			providerID = provider.Identifier()
			break
		}
	}
	if providerID == "" {
		return
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://cpa.internal/v0/management/plugins/access-guard/keys", nil)
	if err != nil {
		return
	}
	response := &trafficDirectoryResponse{headers: make(http.Header)}
	if !s.pluginHost.ServeManagementHTTP(response, request) || response.code != http.StatusOK || response.overflow {
		return
	}
	data := response.body.Bytes()
	if !ojson.ValidBytes(data) {
		return
	}
	rows := ojson.GetBytes(data, "keys")
	if !rows.IsArray() {
		return
	}
	identities = identities[:0]
	for _, row := range rows.Array() {
		id := strings.Clone(strings.TrimSpace(row.Get("id").String()))
		if id == "" {
			continue
		}
		identities = append(identities, traffic.Identity{Provider: providerID, Key: id, Name: strings.Clone(row.Get("name").String()), Disabled: !row.Get("enabled").Bool()})
	}
	collector.SyncPluginKeys(providerID, identities)
}

type trafficDirectoryResponse struct {
	headers  http.Header
	code     int
	body     bytes.Buffer
	overflow bool
}

func (w *trafficDirectoryResponse) Header() http.Header  { return w.headers }
func (w *trafficDirectoryResponse) WriteHeader(code int) { w.code = code }
func (w *trafficDirectoryResponse) Write(p []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	if w.body.Len()+len(p) > 4<<20 {
		w.overflow = true
		return 0, errors.New("traffic key directory exceeds size limit")
	}
	return w.body.Write(p)
}

func (s *Server) getTraffic(c *gin.Context) {
	granularity := c.DefaultQuery("granularity", "day")
	if granularity != "day" && granularity != "month" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "granularity must be day or month"})
		return
	}
	if s.traffic == nil || s.traffic.collector.Load() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "traffic statistics have not started", "state": "unavailable"})
		return
	}
	collector := s.traffic.collector.Load()
	keyID := c.Query("key_id")
	snapshot := collector.Summary(traffic.Query{KeyID: keyID, Granularity: granularity})
	if snapshot.State == "key_not_found" {
		c.JSON(http.StatusNotFound, gin.H{"error": "traffic key not found"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, snapshot)
}

func (s *Server) getTrafficKeys(c *gin.Context) {
	if s.traffic == nil || s.traffic.collector.Load() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "traffic statistics have not started"})
		return
	}
	offset, errOffset := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, errLimit := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if errOffset != nil || errLimit != nil || offset < 0 || limit < 1 || limit > 200 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "offset must be nonnegative and limit must be 1..200"})
		return
	}
	group := c.Query("group")
	if group != "" && group != "native" && group != "plugin" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "group must be native or plugin"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, s.traffic.collector.Load().Keys(traffic.KeyQuery{Group: group, Search: c.Query("search"), IncludeDeleted: c.DefaultQuery("include_deleted", "true") != "false", Offset: offset, Limit: limit}))
}
