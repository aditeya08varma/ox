package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sageox/ox/internal/auth"
)

func TestReadHostedAttestFailuresUsesExactGrantRouteAndVerifiesEvidence(t *testing.T) {
	failureRaw := []byte(`{"version":1,"publicationId":"attpub_test","failures":[{"scenario":"denied"}],"runLevelFailures":[]}`)
	manifestRaw := []byte(`{"files":[{"path":"failures/index.json","bytes":` +
		jsonNumber(len(failureRaw)) + `,"sha256":"` + testSHA256(failureRaw) + `"}]}`)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/__grant":
			if err := r.ParseForm(); err != nil || r.Form.Get("token") != "secret-capability" {
				http.Error(w, "bad grant", http.StatusBadRequest)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "CloudFront-Policy", Value: "policy", Path: "/runs/attpub_test/"})
			w.WriteHeader(http.StatusNoContent)
		case "/runs/attpub_test/manifest.json":
			if _, err := r.Cookie("CloudFront-Policy"); err != nil {
				http.Error(w, "missing cookie", http.StatusForbidden)
				return
			}
			_, _ = w.Write(manifestRaw)
		case "/runs/attpub_test/failures/index.json":
			if _, err := r.Cookie("CloudFront-Policy"); err != nil {
				http.Error(w, "missing cookie", http.StatusForbidden)
				return
			}
			_, _ = w.Write(failureRaw)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	oldRequest := hostedAttestRequest
	t.Cleanup(func() { hostedAttestRequest = oldRequest })
	var requestedURL string
	hostedAttestRequest = func(_ context.Context, apiEndpoint, method, requestURL string, data any) (*auth.APIResponse, error) {
		requestedURL = requestURL
		if apiEndpoint != "https://sageox.ai" {
			t.Fatalf("grant endpoint = %q", apiEndpoint)
		}
		if method != http.MethodPost || data != nil {
			t.Fatalf("grant request = %s data=%#v", method, data)
		}
		return &auth.APIResponse{StatusCode: http.StatusOK, Data: map[string]any{
			"base_url": server.URL + "/runs/attpub_test/",
			"bootstrap": map[string]any{
				"url": server.URL + "/__grant", "method": "POST", "token": "secret-capability",
			},
			"manifest": map[string]any{"path": "manifest.json", "sha256": "sha256:" + testSHA256(manifestRaw)},
		}}, nil
	}

	failures, err := readHostedAttestFailures(context.Background(), "/repo", "attpub_test")
	if err != nil {
		t.Fatalf("readHostedAttestFailures: %v", err)
	}
	if requestedURL != "https://sageox.ai/api/v1/attest/attpub_test/grants" {
		t.Fatalf("grant URL = %q", requestedURL)
	}
	if failures.PublicationID != "attpub_test" || rawJSONArrayLength(failures.Failures) != 1 {
		t.Fatalf("failures = %#v", failures)
	}
}

func TestHostedAttestRequestDoesNotUseGlobalEndpointToken(t *testing.T) {
	globalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "global endpoint must not receive the project request", http.StatusInternalServerError)
	}))
	defer globalServer.Close()

	projectRequests := make(chan string, 1)
	projectServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		projectRequests <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer projectServer.Close()

	t.Setenv("SAGEOX_ENDPOINT", globalServer.URL)
	t.Setenv("SAGEOX_TOKEN", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OX_XDG_DISABLE", "")

	globalToken := &auth.StoredToken{
		AccessToken: "global-secret",
		ExpiresAt:   time.Now().Add(time.Hour),
	}
	if err := auth.SaveTokenForEndpoint(globalServer.URL, globalToken); err != nil {
		t.Fatal(err)
	}

	_, err := hostedAttestRequest(context.Background(), projectServer.URL, http.MethodGet, projectServer.URL+"/api/v1/attest/attpub_test", nil)
	var authErr *auth.AuthenticationError
	if !errors.As(err, &authErr) {
		t.Fatalf("request without project credentials error = %v, want AuthenticationError", err)
	}
	select {
	case authorization := <-projectRequests:
		t.Fatalf("project received a request without a project credential: %q", authorization)
	default:
	}

	projectToken := &auth.StoredToken{
		AccessToken: "project-secret",
		ExpiresAt:   time.Now().Add(time.Hour),
	}
	if err := auth.SaveTokenForEndpoint(projectServer.URL, projectToken); err != nil {
		t.Fatal(err)
	}
	response, err := hostedAttestRequest(context.Background(), projectServer.URL, http.MethodGet, projectServer.URL+"/api/v1/attest/attpub_test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !response.Ok() {
		t.Fatalf("project request status = %d", response.StatusCode)
	}
	projectAuthorization := <-projectRequests
	if projectAuthorization != "Bearer project-secret" {
		t.Fatalf("project Authorization = %q, want project-scoped credential", projectAuthorization)
	}
}

func TestFetchHostedAttestFailuresRejectsManifestDigestMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__grant" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = w.Write([]byte(`{"files":[]}`))
	}))
	defer server.Close()

	grant := &hostedAttestGrant{BaseURL: server.URL + "/runs/attpub_test/"}
	grant.Bootstrap.URL = server.URL + "/__grant"
	grant.Bootstrap.Method = http.MethodPost
	grant.Bootstrap.Token = "secret-capability"
	grant.Manifest.Path = "manifest.json"
	grant.Manifest.SHA256 = "sha256:" + testSHA256([]byte("different"))

	if _, err := fetchHostedAttestFailures(context.Background(), grant); err == nil {
		t.Fatal("expected manifest digest mismatch")
	}
}

func TestValidateHostedAttestGrantRejectsCrossOriginExchange(t *testing.T) {
	grant := &hostedAttestGrant{BaseURL: "https://attest.sageox.ai/runs/attpub_test/"}
	grant.Bootstrap.URL = "https://evil.example/__grant"
	grant.Bootstrap.Method = http.MethodPost
	grant.Bootstrap.Token = "secret-capability"
	grant.Manifest.Path = "manifest.json"

	if _, _, err := validateHostedAttestGrant(grant); err == nil {
		t.Fatal("expected cross-origin grant rejection")
	}
}

func testSHA256(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func jsonNumber(value int) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
