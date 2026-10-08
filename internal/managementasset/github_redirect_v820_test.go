package managementasset

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/githubauth"
)

func TestV820ManagementReleaseRedirectKeepsGitHubTokenOnAllowedOriginOnly(t *testing.T) {
	githubauth.SetToken("synthetic-management-token")
	t.Cleanup(func() { githubauth.SetToken("") })
	for _, target := range []string{
		"https://api.github.com/repos/owner/repo/releases/latest",
		"https://api.github.com:443/repos/owner/repo/releases/latest",
		"https://mirror.api.github.com/release",
		"https://downloads.example.invalid/release",
		"https://raw.githubusercontent.com/owner/repo/release",
		"http://api.github.com/release",
		"https://api.github.com:8443/release",
		"https://synthetic-user@api.github.com/release",
	} {
		t.Run(target, func(t *testing.T) {
			calls, redirects := 0, 0
			client := &http.Client{
				Transport: githubAssetTransport(func(req *http.Request) (*http.Response, error) {
					calls++
					want := ""
					if githubauth.TokenForURL(req.URL.String()) != "" {
						want = "Bearer synthetic-management-token"
					}
					if want != "" && req.Header.Get("Authorization") != want || want == "" && strings.Contains(req.Header.Get("Authorization"), "synthetic-management-token") {
						t.Errorf("hop %d forwarded credential outside its permitted API origin", calls)
					}
					if calls == 1 {
						return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{target}}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
					}
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"assets":[{"name":"management.html","digest":"sha256:synthetic"}]}`)), Request: req}, nil
				}),
				CheckRedirect: func(_ *http.Request, _ []*http.Request) error { redirects++; return nil },
			}
			if _, _, errFetch := fetchLatestAsset(t.Context(), client, "https://api.github.com/repos/owner/repo/releases/initial"); errFetch != nil {
				t.Fatal(errFetch)
			}
			if calls != 2 || redirects != 1 {
				t.Fatalf("redirect callback changed: calls=%d callbacks=%d", calls, redirects)
			}
		})
	}
}

func TestV820ManagementReleaseRedirectPreservesCallerPolicyAndLimit(t *testing.T) {
	githubauth.SetToken("synthetic-management-token")
	t.Cleanup(func() { githubauth.SetToken("") })
	for _, customPolicy := range []bool{false, true} {
		t.Run(fmt.Sprint(customPolicy), func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: githubAssetTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{fmt.Sprintf("https://api.github.com/redirect/%d", calls)}}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
			})}
			if customPolicy {
				client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return fmt.Errorf("synthetic caller redirect policy") }
			}
			_, _, errFetch := fetchLatestAsset(t.Context(), client, "https://api.github.com/initial")
			if errFetch == nil {
				t.Fatal("redirect policy was ignored")
			}
			want := 10
			if customPolicy {
				want = 1
			}
			if calls != want {
				t.Fatalf("calls=%d, want %d", calls, want)
			}
		})
	}
}
