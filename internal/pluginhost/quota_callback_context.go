package pluginhost

import (
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type quotaCallbackHTTPClientKey struct{}

func (a rpcQuotaProvider) openQuotaCallbackContext(ctx context.Context, client pluginapi.HostHTTPClient) (string, func(), error) {
	if client == nil {
		return "", nil, fmt.Errorf("quota host HTTP client is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(context.WithValue(ctx, quotaCallbackHTTPClientKey{}, client))
	callbackID, closeCallback := a.openHostCallbackContext(ctx)
	if callbackID == "" {
		cancel()
		return "", nil, fmt.Errorf("quota host callback context is unavailable")
	}
	a.host.callbackContexts.requireQuotaScope(a.id)
	return callbackID, func() {
		cancel()
		closeCallback()
	}, nil
}

func quotaCallbackHTTPClient(ctx context.Context) pluginapi.HostHTTPClient {
	if ctx == nil {
		return nil
	}
	client, _ := ctx.Value(quotaCallbackHTTPClientKey{}).(pluginapi.HostHTTPClient)
	return client
}

func (h *Host) resolveHTTPCallback(ctx context.Context, callbackID string) (context.Context, pluginapi.HostHTTPClient, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	callerPluginID := hostCallbackPluginIDFromContext(ctx)
	if callbackID == "" && callerPluginID != "" {
		if h != nil && h.callbackContexts.requiresQuotaScope(callerPluginID) {
			return nil, nil, fmt.Errorf("quota host HTTP callback context is required")
		}
		// Quota plugins must retain the scope supplied by the host RPC request.
		// Legacy plugins without quota capability may still make unscoped calls.
		for _, record := range h.activeRecords() {
			if record.id == callerPluginID && record.plugin.Capabilities.QuotaProvider != nil {
				return nil, nil, fmt.Errorf("quota host HTTP callback context is required")
			}
		}
	}
	if callbackID != "" {
		if h == nil || h.callbackContexts == nil {
			return nil, nil, fmt.Errorf("host HTTP callback context is unavailable")
		}
		h.callbackContexts.mu.RLock()
		entry, exists := h.callbackContexts.contexts[callbackID]
		h.callbackContexts.mu.RUnlock()
		if !exists || entry.ctx == nil {
			return nil, nil, fmt.Errorf("host HTTP callback context is invalid or expired")
		}
		if callerPluginID != "" && callerPluginID != entry.pluginID {
			return nil, nil, fmt.Errorf("host HTTP callback belongs to another plugin")
		}
		ctx = entry.ctx
	}
	client := quotaCallbackHTTPClient(ctx)
	if client != nil && (callerPluginID == "" || callerPluginID != hostCallbackPluginIDFromContext(ctx)) {
		return nil, nil, fmt.Errorf("quota host HTTP callback caller is invalid")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if client == nil {
		client = h.newHTTPClientWithProxy(nil, oauthProxyURLFromContext(ctx))
	}
	return ctx, client, nil
}
