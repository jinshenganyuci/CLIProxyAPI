package handlers

import (
	"bytes"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
)

func TestV827CompressedBodyLimitChecksDecodedContent(t *testing.T) {
	payload := append([]byte(`{"input":"`), bytes.Repeat([]byte("x"), 2048)...)
	payload = append(payload, []byte(`"}`)...)
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if errClose := encoder.Close(); errClose != nil {
			t.Error(errClose)
		}
	}()
	encoded := encoder.EncodeAll(payload, nil)
	if len(encoded) >= len(payload)/2 {
		t.Fatal("fixture is not compressed enough to distinguish wire and decoded bounds")
	}
	for _, tc := range []struct {
		name, encoding string
		body           []byte
		limit          int64
		wantTooLarge   bool
	}{
		{name: "decoded-over-limit", encoding: "zstd", body: encoded, limit: 512, wantTooLarge: true},
		{name: "stacked-decoded-over-limit", encoding: "zstd,zstd", body: encoder.EncodeAll(encoded, nil), limit: 512, wantTooLarge: true},
		{name: "exact-boundary", encoding: "zstd", body: encoded, limit: int64(len(payload))},
		{name: "one-byte-over-boundary", encoding: "zstd", body: encoded, limit: int64(len(payload)) - 1, wantTooLarge: true},
		{name: "default-unlimited", encoding: "zstd", body: encoded},
		{name: "max-int-no-overflow", encoding: "zstd", body: encoded, limit: math.MaxInt64},
		{name: "mislabeled-json-compatible", encoding: "zstd", body: payload, limit: int64(len(payload))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Set(MaxRequestBodyBytesContextKey, tc.limit)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(tc.body))
			c.Request.Header.Set("Content-Encoding", tc.encoding)
			got, errRead := ReadRequestBody(c)
			if tc.wantTooLarge {
				var maxErr *http.MaxBytesError
				if !errors.As(errRead, &maxErr) || maxErr.Limit != tc.limit || clienterror.HTTPStatusFromError(errRead) != http.StatusRequestEntityTooLarge || got != nil {
					t.Fatalf("decoded body must reject with typed 413 before payload use: size=%d err=%v", len(got), errRead)
				}
				return
			}
			if errRead != nil || !bytes.Equal(got, payload) {
				t.Fatalf("valid or unlimited body changed: bytes=%d err=%v", len(got), errRead)
			}
		})
	}
}
