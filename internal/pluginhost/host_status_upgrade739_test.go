package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func testStatusUpgrade739Callback(t *testing.T, call func(*Host, string, []byte) ([]byte, error)) {
	t.Helper()
	for _, stream := range []bool{false, true} {
		for _, tc := range []struct {
			name       string
			status     int
			underlying error
			wantStatus int
		}{
			{"bad-request", 400, errors.New("invalid input"), 400},
			{"forbidden", 403, errors.New("not permitted"), 403},
			{"quota", 429, errors.New("rate limited"), 429},
			{"unavailable", 503, errors.New("temporarily unavailable"), 503},
			{"nil-cause", 502, nil, 502},
			{"explicit-overrides-underlying", 503, testHostStatusError{error: errors.New("nested quota"), status: 429}, 503},
			{"underlying-only", 0, testHostStatusError{error: errors.New("unauthorized"), status: 401}, 401},
			{"canceled", 0, context.Canceled, 499},
			{"deadline", 0, context.DeadlineExceeded, 504},
			{"unknown", 0, errors.New("plain error"), 0},
		} {
			t.Run(fmt.Sprintf("%s/stream-%t", tc.name, stream), func(t *testing.T) {
				host := New()
				calls := 0
				var streamContext context.Context
				errorMessage := &interfaces.ErrorMessage{StatusCode: tc.status, Error: tc.underlying}
				checkRequest := func(req handlers.ModelExecutionRequest) {
					calls++
					if req.AuthID != "selected-auth" || req.ForcedProvider != "selected-provider" || req.Model != "requested-model" {
						t.Errorf("host callback lost binding/model: %+v", req)
					}
				}
				host.SetModelExecutor(&fakeHostModelExecutor{
					executeModel: func(_ context.Context, req handlers.ModelExecutionRequest) (handlers.ModelExecutionResponse, *interfaces.ErrorMessage) {
						checkRequest(req)
						return handlers.ModelExecutionResponse{}, errorMessage
					},
					executeModelStream: func(ctx context.Context, req handlers.ModelExecutionRequest) (handlers.ModelExecutionStream, *interfaces.ErrorMessage) {
						checkRequest(req)
						streamContext = ctx
						return handlers.ModelExecutionStream{}, errorMessage
					},
				})
				request, err := json.Marshal(pluginapi.HostModelExecutionRequest{Model: "requested-model", AuthID: "selected-auth", ForcedProvider: "selected-provider", Stream: stream})
				if err != nil {
					t.Fatal(err)
				}
				method := pluginabi.MethodHostModelExecute
				if stream {
					method = pluginabi.MethodHostModelExecuteStream
				}
				raw, errCall := call(host, method, request)
				if errCall != nil {
					t.Fatal(errCall)
				}
				var envelope pluginabi.Envelope
				if err := json.Unmarshal(raw, &envelope); err != nil {
					t.Fatal(err)
				}
				if envelope.OK || envelope.Error == nil || envelope.Error.Code != "host_call_failed" || envelope.Error.HTTPStatus != tc.wantStatus {
					t.Fatalf("status was lost or replaced: %s", raw)
				}
				_, decoded := decodeEnvelopeResult[rpcEmptyResponse](envelope)
				if got := clienterror.HTTPStatusFromError(decoded); got != tc.wantStatus {
					t.Errorf("decoded status=%d, want %d", got, tc.wantStatus)
				}
				if tc.underlying != nil && decoded.Error() != tc.underlying.Error() {
					t.Errorf("message changed: %v", decoded)
				}
				if got := clienterror.IsRequestFault(0, decoded); got != (tc.wantStatus == http.StatusBadRequest) {
					t.Errorf("request/credential fault classification changed for status %d: %t", tc.wantStatus, got)
				}
				if calls != 1 {
					t.Errorf("callback repeated model execution: %d", calls)
				}
				if stream && streamContext.Err() != context.Canceled {
					t.Errorf("failed stream leaked context: %v", streamContext.Err())
				}
			})
		}
	}
}

func TestHostStatusUpgrade739RPCRoundTrip(t *testing.T) {
	testStatusUpgrade739Callback(t, func(host *Host, method string, request []byte) ([]byte, error) {
		result, err := host.callFromPlugin(context.Background(), method, request)
		if err != nil {
			return marshalRPCError("host_call_failed", err.Error(), clienterror.HTTPStatusFromError(err)), nil
		}
		return result, nil
	})
}
