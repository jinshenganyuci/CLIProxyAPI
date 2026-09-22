package proxyutil

import (
	"net/http"
	"strings"
)

// ExecutionProxyError rejects one request without invalidating its selected credential.
type ExecutionProxyError struct{ message string }

func (e *ExecutionProxyError) Error() string       { return e.message }
func (*ExecutionProxyError) StatusCode() int       { return http.StatusBadRequest }
func (*ExecutionProxyError) IsRequestScoped() bool { return true }

// ResolveExecutionProxy prevents request routing from replacing an explicit credential route.
// Credentials without a configured route may use the request override before the global route.
func ResolveExecutionProxy(requestProxy, credentialProxy, globalProxy string) (string, error) {
	requestProxy = strings.TrimSpace(requestProxy)
	credentialProxy = strings.TrimSpace(credentialProxy)
	globalProxy = strings.TrimSpace(globalProxy)
	if requestProxy == "" {
		if credentialProxy != "" {
			return credentialProxy, nil
		}
		return globalProxy, nil
	}
	requestSetting, errRequest := Parse(requestProxy)
	if errRequest != nil || (requestSetting.Mode != ModeDirect && !ValidRequestProxy(requestProxy)) {
		return "", &ExecutionProxyError{message: "invalid execution proxy override"}
	}
	if credentialProxy == "" {
		return normalizedExecutionProxy(requestSetting), nil
	}
	credentialSetting, errCredential := Parse(credentialProxy)
	if errCredential != nil || normalizedExecutionProxy(requestSetting) != normalizedExecutionProxy(credentialSetting) {
		return "", &ExecutionProxyError{message: "execution proxy override conflicts with the credential proxy"}
	}
	return normalizedExecutionProxy(credentialSetting), nil
}

func normalizedExecutionProxy(setting Setting) string {
	if setting.Mode == ModeDirect {
		return "direct"
	}
	if setting.URL != nil {
		return setting.URL.String()
	}
	return setting.Raw
}
