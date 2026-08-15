package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
	hostedAttestRequest = func(_ context.Context, method, requestURL string, data interface{}) (*auth.APIResponse, error) {
		requestedURL = requestURL
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
