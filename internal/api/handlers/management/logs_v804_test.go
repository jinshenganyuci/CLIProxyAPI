package management

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

func TestV804RequestLogLookupHistoricalAndUUIDSuffix(t *testing.T) {
	dir := t.TempDir()
	h := newLogsTestHandler(dir, true)
	for _, tc := range []struct {
		name, stored, query, body string
	}{
		{"historical zero", "00000000", "00000000", "legacy zero log"},
		{"historical hex", "abcdef12", "abcdef12", "legacy hex log"},
		{"UUID full", "8765abcd", "01996ab1-1234-7abc-8ef0-12348765abcd", "UUID body"},
		{"UUID short", "8765abcd", "8765abcd", "UUID body"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name := "v1_responses-2026-09-30T120000-" + tc.stored + ".log"
			if errWrite := os.WriteFile(filepath.Join(dir, name), []byte(tc.body), 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Params = gin.Params{{Key: "id", Value: tc.query}}
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/management/logs/request/"+tc.query, nil)
			h.GetRequestLogByID(c)
			if rec.Code != http.StatusOK || rec.Body.String() != tc.body || !strings.Contains(rec.Header().Get("Content-Disposition"), name) {
				t.Fatalf("lookup mismatch: status=%d body=%s disposition=%s", rec.Code, rec.Body.String(), rec.Header().Get("Content-Disposition"))
			}
		})
	}
}

func TestV804RequestLogShortSuffixCollisionPreservesFilesButSelectsLatest(t *testing.T) {
	dir := t.TempDir()
	logger := logging.NewFileRequestLogger(true, dir, "", 0)
	ids := []string{"01996ab1-1234-7abc-8ef0-1234deadbeef", "01996ab2-5678-7def-9abc-5678deadbeef"}
	for i, id := range ids {
		body := []byte([]string{"first-UUID-response", "second-UUID-response"}[i])
		if errLog := logger.LogRequest("/v1/responses", http.MethodPost, nil, []byte("synthetic"), http.StatusOK, nil, body, nil, nil, nil, nil, nil, id, time.Now(), time.Now()); errLog != nil {
			t.Fatal(errLog)
		}
	}
	entries, errRead := os.ReadDir(dir)
	if errRead != nil || len(entries) != 2 {
		t.Fatalf("same-short-ID logs overwritten: count=%d err=%v", len(entries), errRead)
	}
	var latestBody string
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), "-deadbeef.log") {
			t.Fatalf("unexpected local filename: %s", entry.Name())
		}
		path := filepath.Join(dir, entry.Name())
		body, errBody := os.ReadFile(path)
		if errBody != nil {
			t.Fatal(errBody)
		}
		modified := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
		if strings.Contains(string(body), "second-UUID-response") {
			modified = modified.Add(time.Second)
			latestBody = string(body)
		} else if !strings.Contains(string(body), "first-UUID-response") {
			t.Fatalf("unexpected or overwritten body: %s", body)
		}
		if errTimes := os.Chtimes(path, modified, modified); errTimes != nil {
			t.Fatal(errTimes)
		}
	}
	if latestBody == "" {
		t.Fatal("second UUID response was not persisted")
	}
	h := newLogsTestHandler(dir, true)
	for _, query := range append(ids, "deadbeef") {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Params = gin.Params{{Key: "id", Value: query}}
		c.Request = httptest.NewRequest(http.MethodGet, "/v1/management/logs/request/"+query, nil)
		h.GetRequestLogByID(c)
		// Native lookup matches the short suffix and newest file, even for a full UUID query.
		if rec.Code != http.StatusOK || rec.Body.String() != latestBody {
			t.Fatalf("short-suffix lookup contract changed for %s: status=%d body=%s", query, rec.Code, rec.Body.String())
		}
	}
}
