package executor

import "context"

// WebsocketResponseAdmission shares the selected credential's request capacity
// with subsequent responses on a duplex connection. Completion releases a slot,
// not the authenticated socket. The caller owns response-to-release matching.
type WebsocketResponseAdmission struct {
	ReleaseInitial func()
	Acquire        func(context.Context) (func(), error)
}

type websocketResponseAdmissionKey struct{}

func WithWebsocketResponseAdmission(ctx context.Context, admission *WebsocketResponseAdmission) context.Context {
	return context.WithValue(ctx, websocketResponseAdmissionKey{}, admission)
}

func WebsocketResponseAdmissionFromContext(ctx context.Context) *WebsocketResponseAdmission {
	if ctx == nil {
		return nil
	}
	admission, _ := ctx.Value(websocketResponseAdmissionKey{}).(*WebsocketResponseAdmission)
	return admission
}
