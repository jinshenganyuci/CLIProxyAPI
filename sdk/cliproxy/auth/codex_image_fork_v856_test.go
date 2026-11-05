package auth

import (
	"context"
	"net/http"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func newV856ImageAuth(t *testing.T, id string, outer bool) *Auth {
	t.Helper()
	a := &Auth{ID: id, Provider: "codex", Status: StatusActive, ProxyURL: "http://127.0.0.1:1", Attributes: map[string]string{"priority": "100"}, Metadata: map[string]any{
		"use_siwc": true, "concurrency_limit": 1, "codex_identity_namespace": "fixture-namespace",
		"siwc": map[string]any{"access_token": "synthetic-siwc", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)},
	}}
	if outer {
		a.Metadata["access_token"] = "synthetic-outer"
		a.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339)
		a.Attributes["priority"] = "0"
	}
	registerTestCodexModels(t, a.ID)
	return a
}

func TestV856ImageSelectionPreservesNativeTrackAndBusinessSIWC(t *testing.T) {
	for _, mode := range []string{"execute", "count", "stream"} {
		t.Run(mode, func(t *testing.T) {
			m := NewManager(nil, &FillFirstSelector{}, nil)
			m.SetRetryConfig(0, 0, 0)
			siwc, err := m.Register(t.Context(), newV856ImageAuth(t, t.Name()+"-siwc-only", false))
			if err != nil {
				t.Fatal(err)
			}
			native, err := m.Register(t.Context(), newV856ImageAuth(t, t.Name()+"-native", true))
			if err != nil {
				t.Fatal(err)
			}
			var selected string
			execute := func(ctx context.Context, a *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				selected = a.ID
				if a.ProxyURL != "http://127.0.0.1:1" || a.Metadata["codex_identity_namespace"] != "fixture-namespace" || a.RegistrationEpoch == 0 {
					t.Error("credential routing or identity snapshot changed")
				}
				requireCredentialConcurrencySnapshot(t, m, a.ID, 1, 1)
				if req.Model == "gpt-image-2.6" && (IsCodexUseSIWCForRequest(ctx, a, opts.Headers) || authAccessTokenForRequest(ctx, a, opts.Headers) != "synthetic-outer") {
					t.Error("image did not use native token track")
				}
				if req.Model == "gpt-5.4" && !IsCodexUseSIWCForRequest(ctx, a, opts.Headers) {
					t.Error("ordinary business request left SIWC track")
				}
				return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
			}
			executor := &credentialConcurrencyExecutor{schedulerTestExecutor: schedulerTestExecutor{provider: "codex"}, execute: execute}
			executor.stream = func(ctx context.Context, a *Auth, r cliproxyexecutor.Request, o cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				resp, errExec := execute(ctx, a, r, o)
				if errExec != nil {
					return nil, errExec
				}
				ch := make(chan cliproxyexecutor.StreamChunk, 1)
				ch <- cliproxyexecutor.StreamChunk{Payload: resp.Payload}
				close(ch)
				return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
			}
			m.RegisterExecutor(executor)
			for _, imageRequest := range []bool{true, false} {
				model, format, want := "gpt-image-2.6", "openai-image", native.ID
				if !imageRequest {
					model, format, want = "gpt-5.4", "openai-response", siwc.ID
				}
				req := cliproxyexecutor.Request{Model: model}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString(format), Headers: http.Header{"User-Agent": {"fixture-client"}}}
				switch mode {
				case "execute":
					_, err = m.Execute(t.Context(), []string{"codex"}, req, opts)
				case "count":
					_, err = m.ExecuteCount(t.Context(), []string{"codex"}, req, opts)
				case "stream":
					var sr *cliproxyexecutor.StreamResult
					sr, err = m.ExecuteStream(t.Context(), []string{"codex"}, req, opts)
					if err == nil {
						for range sr.Chunks {
						}
					}
				}
				if err != nil || selected != want {
					t.Fatalf("image=%t selection=%s want=%s error=%v", imageRequest, selected, want, err)
				}
				requireCredentialConcurrencySnapshot(t, m, want, 0, 1)
			}
		})
	}
}

func TestV856ImageAlternativeProbeHonorsTrackCapacityAndPluginBoundary(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(&recordingSIWCExecutor{id: "codex"})
	a, err := m.Register(t.Context(), newV856ImageAuth(t, t.Name(), false))
	if err != nil {
		t.Fatal(err)
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-image")}
	if m.HasAlternativeTargetForModel(t.Context(), []string{"codex"}, "gpt-image-2.6", opts, "failed") {
		t.Fatal("SIWC-only credential counted as an image replay target")
	}
	a.Metadata["access_token"] = "synthetic-outer"
	a.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339)
	a, err = m.Update(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	if !m.HasAlternativeTargetForModel(t.Context(), []string{"codex"}, "gpt-image-2.6", opts, "failed") {
		t.Fatal("native credential not available")
	}
	release, err := m.AcquireCredentialConcurrency(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	if m.HasAlternativeTargetForModel(t.Context(), []string{"codex"}, "gpt-image-2.6", opts, "failed") {
		t.Error("full credential counted as replay target")
	}
	release()
	scheduler := &fakePluginScheduler{}
	m.SetPluginScheduler(scheduler)
	if m.HasAlternativeTargetForModel(t.Context(), []string{"codex"}, "gpt-image-2.6", opts, "failed") {
		t.Fatal("plugin-bound availability was inferred from an unrestricted pool")
	}
	if scheduler.calls != 0 {
		t.Fatal("read-only probe invoked plugin scheduler")
	}
	requireCredentialConcurrencySnapshot(t, m, a.ID, 0, 1)
}

func TestV856HomeImageRejectsSIWCOnlyAndReleasesAccountedScope(t *testing.T) {
	a := newV856ImageAuth(t, t.Name(), false)
	dispatcher := &accountedHomeExecutionDispatcher{auths: []Auth{*a}}
	m := NewManager(nil, nil, nil)
	m.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	reg := executionregistry.New()
	m.PublishHomeDispatch(dispatcher, reg, 1)
	m.RegisterExecutor(&recordingSIWCExecutor{id: "codex", onExecute: func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		t.Error("SIWC-only Home auth reached executor")
		return cliproxyexecutor.Response{}, nil
	}})
	_, err := m.Execute(t.Context(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-image-2.6"}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-image")})
	if err == nil {
		t.Fatal("Home returned SIWC-only credential without rejection")
	}
	if freeze := reg.FreezeInFlight(time.Now()); len(freeze.Executions) != 0 {
		t.Fatal("rejected Home image selection retained its scope")
	}
}

type v856ConstrainedHomeDispatcher struct {
	accountedHomeExecutionDispatcher
	releases             *atomic.Int32
	excluded             [][]string
	headers              []http.Header
	releasedBeforeSecond bool
}

func (d *v856ConstrainedHomeDispatcher) RPopAuthWithConstraints(ctx context.Context, model, session string, headers http.Header, count int, excluded []string, _ string) ([]byte, error) {
	if d.calls.Load() == 1 {
		d.releasedBeforeSecond = d.releases.Load() == 1
	}
	d.excluded = append(d.excluded, append([]string(nil), excluded...))
	d.headers = append(d.headers, headers.Clone())
	return d.RPopAuth(ctx, model, session, headers, count)
}

func TestV856HomeImageRedispatchIsBoundedAndPreservesExclusions(t *testing.T) {
	for _, mode := range []string{"native-second", "repeated-siwc-only", "pinned", "release-failed"} {
		t.Run(mode, func(t *testing.T) {
			first := newV856ImageAuth(t, t.Name()+"-first", false)
			second := newV856ImageAuth(t, t.Name()+"-second", mode == "native-second")
			var releases atomic.Int32
			dispatcher := &v856ConstrainedHomeDispatcher{accountedHomeExecutionDispatcher: accountedHomeExecutionDispatcher{auths: []Auth{*first, *second}}, releases: &releases}
			m := NewManager(nil, nil, nil)
			m.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			reg := executionregistry.New()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			reg.SetReleaseSink(func(group executionregistry.ReleaseGroup, sequence int64) *executionregistry.ReleaseTicket {
				releases.Add(1)
				if mode == "release-failed" {
					cancel()
					return executionregistry.NewReleaseTicket(group, sequence, make(chan struct{}))
				}
				return nil
			})
			m.PublishHomeDispatch(dispatcher, reg, 1)
			calls := 0
			m.RegisterExecutor(&recordingSIWCExecutor{id: "codex", onExecute: func(ctx context.Context, a *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				calls++
				if a.ID != second.ID || IsCodexUseSIWCForRequest(ctx, a, opts.Headers) {
					t.Error("Home image did not select native second credential")
				}
				return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
			}})
			opts := withHomeExcludedAuthIDs(cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-image"), Headers: http.Header{"User-Agent": {"fixture-client"}, "Authorization": {"Bearer synthetic-caller"}}}, map[string]struct{}{"already-excluded": {}})
			if mode == "pinned" {
				opts.Metadata[cliproxyexecutor.PinnedAuthMetadataKey] = first.ID
			}
			_, err := m.Execute(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-image-2.6"}, opts)
			if mode == "native-second" {
				if err != nil || calls != 1 {
					t.Fatalf("native retry calls=%d error=%v", calls, err)
				}
			} else if err == nil || calls != 0 {
				t.Fatalf("invalid native retry reached executor: calls=%d error=%v", calls, err)
			}
			wantDispatches := int32(2)
			if mode == "pinned" || mode == "release-failed" {
				wantDispatches = 1
			}
			if dispatcher.calls.Load() != wantDispatches || releases.Load() != wantDispatches {
				t.Fatalf("dispatch/release=%d/%d want=%d", dispatcher.calls.Load(), releases.Load(), wantDispatches)
			}
			if wantDispatches == 2 && (!dispatcher.releasedBeforeSecond || !slices.Contains(dispatcher.excluded[1], first.ID) || !slices.Contains(dispatcher.excluded[1], "already-excluded")) {
				t.Fatal("retry lost exclusion or preceded release acknowledgement")
			}
			for _, headers := range dispatcher.headers {
				if headers.Get("User-Agent") != "codex_cli_rs" || headers.Get("Authorization") != "Bearer synthetic-caller" {
					t.Fatal("Home native hint or caller binding headers changed")
				}
			}
			if opts.Headers.Get("User-Agent") != "fixture-client" || slices.Contains(homeExcludedAuthIDsFromMetadata(opts.Metadata), first.ID) {
				t.Fatal("retry mutated original headers or metadata")
			}
			if len(reg.FreezeInFlight(time.Now()).Executions) != 0 {
				t.Fatal("Home scope leaked")
			}
		})
	}
}

func TestV856HomeAlternativeProbeChecksKindAndReleaseFailure(t *testing.T) {
	for _, mode := range []string{"wrong-kind", "release-failed"} {
		t.Run(mode, func(t *testing.T) {
			a := newV856ImageAuth(t, t.Name(), true)
			m := NewManager(nil, nil, nil)
			m.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			reg := executionregistry.New()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			reg.SetReleaseSink(func(group executionregistry.ReleaseGroup, sequence int64) *executionregistry.ReleaseTicket {
				if mode == "release-failed" {
					cancel()
					return executionregistry.NewReleaseTicket(group, sequence, make(chan struct{}))
				}
				return nil
			})
			m.PublishHomeDispatch(&accountedHomeExecutionDispatcher{auths: []Auth{*a}}, reg, 1)
			m.RegisterExecutor(&recordingSIWCExecutor{id: "codex"})
			if mode == "wrong-kind" {
				ctx = withRequiredAuthKind(ctx, "api_key")
			}
			if m.HasAlternativeTargetForModel(ctx, []string{"codex"}, "gpt-image-2.6", cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-image")}, "previous") {
				t.Fatal("Home probe reported an ineligible or unacknowledged target")
			}
			if len(reg.FreezeInFlight(time.Now()).Executions) != 0 {
				t.Fatal("Home probe leaked scope")
			}
		})
	}
}

func TestV856ImageNativeAPIKeyAndCanceledStreamLease(t *testing.T) {
	m := NewManager(nil, nil, nil)
	a := newV856ImageAuth(t, t.Name(), false)
	a.Attributes[AttributeAPIKey] = "synthetic-native-key"
	a, err := m.Register(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	executor := &credentialConcurrencyExecutor{schedulerTestExecutor: schedulerTestExecutor{provider: "codex"}}
	executor.stream = func(ctx context.Context, selected *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
		if selected.ID != a.ID || IsCodexUseSIWCForRequest(ctx, selected, opts.Headers) {
			t.Error("native API key was not selected")
		}
		chunks := make(chan cliproxyexecutor.StreamChunk, 1)
		chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"partial":true}`)}
		go func() { <-ctx.Done(); close(chunks) }()
		return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
	}
	m.RegisterExecutor(executor)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req := cliproxyexecutor.Request{Model: "gpt-image-2.6"}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-image")}
	stream, err := m.ExecuteStream(ctx, []string{"codex"}, req, opts)
	if err != nil {
		t.Fatal(err)
	}
	requireCredentialConcurrencySnapshot(t, m, a.ID, 1, 1)
	if _, errBusy := m.Execute(t.Context(), []string{"codex"}, req, opts); !IsCredentialConcurrencyError(errBusy) {
		t.Fatalf("image bypassed credential capacity: %v", errBusy)
	}
	cancel()
	for range stream.Chunks {
	}
	requireCredentialConcurrencySnapshot(t, m, a.ID, 0, 1)
}
