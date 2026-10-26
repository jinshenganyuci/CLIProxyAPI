package executor

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestV844CodexCompressionKeepsFinalIdentityProxyUsageAndLease(t *testing.T) {
	for _, end := range []string{"complete", "retry400", "retry415", "malformed", "truncated", "cancel"} {
		for _, stream := range []bool{false, true} {
			if end == "cancel" && !stream {
				continue
			}
			t.Run(fmt.Sprintf("%s/stream_%t", end, stream), func(t *testing.T) {
				const model = "gpt-5.4"
				authID := t.Name()
				var manager *coreauth.Manager
				var selectedHits, fallbackHits, injectedHits, wrappers, attempts atomic.Int32
				var bodiesMu sync.Mutex
				var firstBody []byte
				selected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					attempt := selectedHits.Add(1)
					if active, limit := manager.CredentialConcurrencySnapshot(authID); active != 1 || limit != 10 {
						t.Errorf("active lease = %d/%d", active, limit)
					}
					if r.URL.Hostname() != "chatgpt.com" || r.URL.Path != "/backend-api/codex/responses" {
						t.Errorf("selected proxy target = %s", r.URL)
					}
					wire, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						t.Error(errRead)
						return
					}
					plain := wire
					if attempt == 1 {
						if r.Header.Get("Content-Encoding") != "zstd" {
							t.Errorf("first request encoding = %q", r.Header.Get("Content-Encoding"))
						}
						decoder, errDecoder := zstd.NewReader(nil)
						if errDecoder != nil {
							t.Error(errDecoder)
							return
						}
						plain, errRead = decoder.DecodeAll(wire, nil)
						decoder.Close()
						if errRead != nil {
							t.Error(errRead)
							return
						}
					} else if r.Header.Get("Content-Encoding") != "" {
						t.Errorf("plaintext retry retained encoding %q", r.Header.Get("Content-Encoding"))
					}
					bodiesMu.Lock()
					if firstBody == nil {
						firstBody = bytes.Clone(plain)
					} else if !bytes.Equal(plain, firstBody) {
						t.Errorf("retry changed the finalized/mapped business body")
					}
					bodiesMu.Unlock()
					mappedSession := gjson.GetBytes(plain, "prompt_cache_key").String()
					mappedInstall := gjson.GetBytes(plain, "client_metadata.x-codex-installation-id").String()
					if mappedSession == "" || mappedSession == "configured-cache844" || mappedInstall == "" || mappedInstall == "configured-install844" {
						t.Errorf("final rules escaped identity mapping: %s", plain)
					}
					if codexSessionHeaderValue(r.Header) != mappedSession || gjson.GetBytes(plain, "service_tier").String() != "priority" || gjson.GetBytes(plain, "operator_marker").String() != "final844" {
						t.Errorf("final body/header mismatch: %s, headers=%v", plain, r.Header)
					}
					w.Header().Set("Content-Type", "text/event-stream")
					w.Header().Set("Content-Encoding", "gzip")
					if attempt == 1 && strings.HasPrefix(end, "retry") {
						status := http.StatusBadRequest
						if end == "retry415" {
							status = http.StatusUnsupportedMediaType
						}
						w.WriteHeader(status)
						gz := gzip.NewWriter(w)
						_, _ = io.WriteString(gz, `{"error":"synthetic compression rejection"}`)
						_ = gz.Close()
						return
					}
					if end == "malformed" {
						_, _ = io.WriteString(w, "not a gzip stream")
						return
					}
					partial := `data: {"type":"response.output_text.delta","delta":"partial844","response":{"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}}` + "\n\n"
					if end == "cancel" {
						gz := gzip.NewWriter(w)
						_, _ = io.WriteString(gz, partial)
						_ = gz.Flush()
						w.(http.Flusher).Flush()
						<-r.Context().Done()
						return
					}
					var response bytes.Buffer
					gz := gzip.NewWriter(&response)
					if end == "truncated" {
						_, _ = io.WriteString(gz, partial)
					} else {
						_, _ = fmt.Fprintf(gz, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp844\",\"model\":%q,\"status\":\"completed\",\"service_tier\":\"default\",\"prompt_cache_key\":%q,\"installation_id\":%q,\"output\":[{\"id\":\"msg844\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok844\"}]}],\"usage\":{\"input_tokens\":5,\"output_tokens\":2,\"total_tokens\":7}}}\n\n", model, mappedSession, mappedInstall)
					}
					_ = gz.Close()
					data := response.Bytes()
					if end == "truncated" {
						data = data[:len(data)-8]
					}
					w.Header().Set("Content-Length", fmt.Sprint(len(data)))
					_, _ = w.Write(data)
				}))
				defer selected.Close()
				fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					fallbackHits.Add(1)
					http.Error(w, "fallback forbidden", http.StatusBadGateway)
				}))
				defer fallback.Close()
				cfg := codexCredentialIdentityTestConfig(false)
				cfg.ProxyURL = fallback.URL
				cfg.Codex.CredentialProxyPolicy = "require"
				cfg.Codex.UpstreamRequestCompression = true
				cfg.Codex.StreamBootstrapBuffering = true
				cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"operator_marker": "final844", "service_tier": "priority", "prompt_cache_key": "configured-cache844", "client_metadata.x-codex-installation-id": "configured-install844"}}}
				auth := codexCredentialIdentityTestAuth(authID, "0bdfb02c-eaf4-4bea-a449-c66074b475aa")
				auth.ProxyURL = selected.URL
				auth.Attributes = map[string]string{"base_url": "http://chatgpt.com/backend-api/codex"}
				manager = coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
				manager.SetRetryConfig(0, 0, 0)
				manager.RegisterExecutor(NewCodexExecutor(cfg))
				registered, errRegister := manager.Register(t.Context(), auth)
				if errRegister != nil {
					t.Fatal(errRegister)
				}
				registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
				capture := &providerV810UsageCapture{authID: authID, records: make(chan usage.Record, 8)}
				usage.RegisterNamedPlugin(authID, capture)
				t.Cleanup(func() { usage.RegisterNamedPlugin(authID, antigravityUsageNoop{}) })
				ctx, cancel := context.WithCancel(usage.WithServiceTier(t.Context(), "auto"))
				defer cancel()
				ctx = context.WithValue(ctx, "cliproxy.roundtripper", roundTripperFunc(func(*http.Request) (*http.Response, error) {
					injectedHits.Add(1)
					return nil, fmt.Errorf("injected route forbidden")
				}))
				ctx = httptransport.WithDecorator(ctx, func(next http.RoundTripper) http.RoundTripper {
					wrappers.Add(1)
					return roundTripperFunc(func(r *http.Request) (*http.Response, error) { attempts.Add(1); return next.RoundTrip(r) })
				})
				req := coreexecutor.Request{Model: model, Payload: []byte(fmt.Sprintf(`{"model":%q,"input":[{"role":"user","content":%q}],"service_tier":"auto"}`, model, strings.Repeat("synthetic compressible input ", 400)))}
				opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: stream}
				var received bytes.Buffer
				var headers http.Header
				var executionErr error
				if stream {
					result, errStream := manager.ExecuteStream(ctx, []string{"codex"}, req, opts)
					executionErr = errStream
					if result != nil {
						headers = result.Headers
						for chunk := range result.Chunks {
							received.Write(chunk.Payload)
							if chunk.Err != nil {
								executionErr = chunk.Err
							}
							if end == "cancel" && bytes.Contains(chunk.Payload, []byte("partial844")) {
								cancel()
							}
						}
					}
				} else {
					result, errExecute := manager.Execute(ctx, []string{"codex"}, req, opts)
					executionErr, headers = errExecute, result.Headers
					received.Write(result.Payload)
				}
				wantFailure := end == "malformed" || end == "truncated" || end == "cancel"
				if end != "cancel" && (executionErr != nil) != wantFailure {
					t.Fatalf("execution error = %v, failure=%t", executionErr, wantFailure)
				}
				if headers.Get("Content-Encoding") != "" || headers.Get("Content-Length") != "" {
					t.Fatalf("decoded response retained stale encoding/length: %v", headers)
				}
				if !wantFailure && (!bytes.Contains(received.Bytes(), []byte("configured-cache844")) || !bytes.Contains(received.Bytes(), []byte("configured-install844"))) {
					t.Fatalf("decoded identity not restored: %s", received.Bytes())
				}
				if active, limit := manager.CredentialConcurrencySnapshot(authID); active != 0 || limit != 10 {
					t.Fatalf("settled lease = %d/%d", active, limit)
				}
				release, errAcquire := manager.AcquireCredentialConcurrency(t.Context(), registered)
				if errAcquire != nil {
					t.Fatal(errAcquire)
				}
				release()
				wantHits := int32(1)
				if strings.HasPrefix(end, "retry") {
					wantHits = 2
				}
				if selectedHits.Load() != wantHits || attempts.Load() != wantHits || wrappers.Load() != 1 || fallbackHits.Load() != 0 || injectedHits.Load() != 0 {
					t.Fatalf("selected/attempts/wrappers/fallback/injected = %d/%d/%d/%d/%d", selectedHits.Load(), attempts.Load(), wrappers.Load(), fallbackHits.Load(), injectedHits.Load())
				}
				records := antigravityUsageRecordsAfterDelivery815(t, capture)
				if len(records) != 1 || records[0].Failed != wantFailure || records[0].ServiceTier != "auto" || records[0].EffectiveServiceTier != "priority" {
					t.Fatalf("usage outcome = %+v", records)
				}
				if !wantFailure && (records[0].Detail.InputTokens != 5 || records[0].Detail.OutputTokens != 2 || records[0].ResponseServiceTier != "default") {
					t.Fatalf("decoded usage missing: %+v", records[0])
				}
			})
		}
	}
}

// net/http retains the original response while its body is being consumed.
// Decoding may change the caller's metadata, but must not mutate that response.
func TestV844CodexDecodeKeepsTransportResponseMetadata(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(fmt.Sprintf("retry_%t", retry), func(t *testing.T) {
			const content = `{"ok":"decoded844"}`
			var encoded bytes.Buffer
			gz := gzip.NewWriter(&encoded)
			_, _ = io.WriteString(gz, content)
			_ = gz.Close()
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Encoding", "gzip")
				w.Header().Set("Content-Length", fmt.Sprint(encoded.Len()))
				if calls.Add(1) == 1 && retry {
					w.WriteHeader(http.StatusUnsupportedMediaType)
				}
				_, _ = w.Write(encoded.Bytes())
			}))
			defer server.Close()
			transport := &http.Transport{DisableCompression: true}
			defer transport.CloseIdleConnections()
			type received struct {
				response *http.Response
				body     io.ReadCloser
			}
			var originals []received
			client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				response, errRoundTrip := transport.RoundTrip(req)
				if response != nil {
					originals = append(originals, received{response, response.Body})
				}
				return response, errRoundTrip
			})}
			plain := []byte(`{"input":"request844"}`)
			encoder, errEncoder := zstd.NewWriter(nil)
			if errEncoder != nil {
				t.Fatal(errEncoder)
			}
			wire := encoder.EncodeAll(plain, nil)
			encoder.Close()
			req, errRequest := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL, bytes.NewReader(wire))
			if errRequest != nil {
				t.Fatal(errRequest)
			}
			req.Header.Set("Content-Encoding", "zstd")
			decoded, errDo := doCodexRequest(client, req, plain, func(*http.Request) {}, nil)
			if errDo != nil {
				t.Fatal(errDo)
			}
			body, errRead := io.ReadAll(decoded.Body)
			if errClose := decoded.Body.Close(); errClose != nil {
				t.Fatal(errClose)
			}
			if errRead != nil || string(body) != content || decoded.ContentLength != -1 || decoded.Header.Get("Content-Encoding") != "" || decoded.Header.Get("Content-Length") != "" {
				t.Fatalf("decoded response: body=%q error=%v length=%d headers=%v", body, errRead, decoded.ContentLength, decoded.Header)
			}
			wantCalls := 1
			if retry {
				wantCalls = 2
			}
			if len(originals) != wantCalls {
				t.Fatalf("response count=%d, want %d", len(originals), wantCalls)
			}
			for index, original := range originals {
				response := original.response
				if response == decoded || response.ContentLength != int64(encoded.Len()) || response.Header.Get("Content-Encoding") != "gzip" || response.Header.Get("Content-Length") != fmt.Sprint(encoded.Len()) || response.Body != original.body {
					t.Errorf("transport response %d mutated: same=%t length=%d headers=%v bodyReplaced=%t", index, response == decoded, response.ContentLength, response.Header, response.Body != original.body)
				}
			}
		})
	}
}
