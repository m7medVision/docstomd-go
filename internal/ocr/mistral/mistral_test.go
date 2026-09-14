package mistral_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/m7medVision/docstomd-go/internal/ocr"
	"github.com/m7medVision/docstomd-go/internal/ocr/mistral"
)

const testKey = "sk-test-secret-value"

type reply struct {
	status  int
	fixture string
	header  http.Header
}

type fixtureTransport struct {
	t        *testing.T
	replies  []reply
	requests []*http.Request
	bodies   [][]byte
}

func (f *fixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	f.requests = append(f.requests, req)
	f.bodies = append(f.bodies, body)
	r := f.replies[min(len(f.requests), len(f.replies))-1]
	data, err := os.ReadFile(filepath.Join("testdata", r.fixture))
	if err != nil {
		f.t.Fatal(err)
	}
	header := r.header
	if header == nil {
		header = http.Header{}
	}
	return &http.Response{
		StatusCode: r.status,
		Header:     header,
		Body:       io.NopCloser(bytes.NewReader(data)),
		Request:    req,
	}, nil
}

func newProvider(t *testing.T, replies ...reply) (*mistral.Provider, *fixtureTransport) {
	t.Helper()
	t.Setenv(mistral.APIKeyEnv, testKey)
	transport := &fixtureTransport{t: t, replies: replies}
	provider := mistral.New(mistral.Options{
		HTTPClient:   &http.Client{Transport: transport},
		RetryBackoff: time.Millisecond,
	})
	return provider, transport
}

var pdf = ocr.Document{Bytes: []byte("%PDF-1.7 fixture bytes")}

func TestRecognizeParsesRecordedSuccess(t *testing.T) {
	provider, transport := newProvider(t, reply{status: 200, fixture: "success.json"})
	pages, err := provider.Recognize(context.Background(), pdf, []int{1})
	if err != nil {
		t.Fatalf("Recognize: %v", err)
	}
	if len(transport.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(transport.requests))
	}
	if len(pages) != 1 || pages[0].Page != 1 {
		t.Fatalf("pages = %+v, want page 1", pages)
	}
	if !strings.HasPrefix(pages[0].Markdown, "# Order Detail Report by Account") {
		t.Errorf("markdown = %q", pages[0].Markdown)
	}
	if pages[0].Confidence < 0.98 || pages[0].Confidence > 0.99 {
		t.Errorf("confidence = %v, want average page score", pages[0].Confidence)
	}
	if len(pages[0].BBoxes) != 6 {
		t.Fatalf("bboxes = %d, want one per block", len(pages[0].BBoxes))
	}
	box := pages[0].BBoxes[0]
	if box.Page != 1 || box.X0 != 59*72.0/93 || box.Y1 != 56*72.0/93 {
		t.Errorf("first box = %+v, want block pixels scaled to points", box)
	}
}

func TestRecognizeSendsBatchedPayload(t *testing.T) {
	provider, transport := newProvider(t, reply{status: 200, fixture: "success.json"})
	if _, err := provider.Recognize(context.Background(), pdf, []int{2, 5}); err != nil {
		t.Fatalf("Recognize: %v", err)
	}
	req := transport.requests[0]
	if req.Method != http.MethodPost || req.URL.String() != "https://api.mistral.ai/v1/ocr" {
		t.Errorf("request = %s %s", req.Method, req.URL)
	}
	if req.Header.Get("Authorization") != "Bearer "+testKey {
		t.Error("authorization header must carry the environment key")
	}
	var payload struct {
		Model    string `json:"model"`
		Document struct {
			Type string `json:"type"`
			URL  string `json:"document_url"`
		} `json:"document"`
		Pages []int `json:"pages"`
	}
	if err := json.Unmarshal(transport.bodies[0], &payload); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if payload.Model != mistral.DefaultModel || mistral.DefaultModel != "mistral-ocr-latest" {
		t.Errorf("model = %q, want latest alias", payload.Model)
	}
	if payload.Document.Type != "document_url" || payload.Document.URL != "data:application/pdf;base64,"+base64.StdEncoding.EncodeToString(pdf.Bytes) {
		t.Errorf("document = %+v, want base64 data URI", payload.Document)
	}
	if fmt.Sprint(payload.Pages) != "[1 4]" {
		t.Errorf("pages = %v, want zero-indexed [1 4]", payload.Pages)
	}
}

func TestPinnedModel(t *testing.T) {
	t.Setenv(mistral.APIKeyEnv, testKey)
	transport := &fixtureTransport{t: t, replies: []reply{{status: 200, fixture: "success.json"}}}
	provider := mistral.New(mistral.Options{Model: "mistral-ocr-2512", HTTPClient: &http.Client{Transport: transport}})
	if _, err := provider.Recognize(context.Background(), pdf, []int{1}); err != nil {
		t.Fatalf("Recognize: %v", err)
	}
	if !bytes.Contains(transport.bodies[0], []byte(`"model":"mistral-ocr-2512"`)) {
		t.Errorf("pinned model not sent: %.80s", transport.bodies[0])
	}
}

func TestRateLimitBacksOffThenSucceeds(t *testing.T) {
	provider, transport := newProvider(t,
		reply{status: 429, fixture: "rate_limit.json"},
		reply{status: 429, fixture: "rate_limit.json", header: http.Header{"Retry-After": {"0"}}},
		reply{status: 200, fixture: "success.json"},
	)
	pages, err := provider.Recognize(context.Background(), pdf, []int{1})
	if err != nil {
		t.Fatalf("Recognize: %v", err)
	}
	if len(transport.requests) != 3 || len(pages) != 1 {
		t.Errorf("requests = %d pages = %d, want 3 attempts then 1 page", len(transport.requests), len(pages))
	}
}

func TestRateLimitExhaustsRetries(t *testing.T) {
	provider, transport := newProvider(t, reply{status: 429, fixture: "rate_limit.json"})
	_, err := provider.Recognize(context.Background(), pdf, []int{1})
	if !errors.Is(err, mistral.ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
	if !strings.Contains(err.Error(), "Requests rate limit exceeded") {
		t.Errorf("err = %v, want API message", err)
	}
	if len(transport.requests) != 4 {
		t.Errorf("requests = %d, want 1 + 3 retries", len(transport.requests))
	}
}

func TestRateLimitBackoffHonorsCancellation(t *testing.T) {
	t.Setenv(mistral.APIKeyEnv, testKey)
	transport := &fixtureTransport{t: t, replies: []reply{{status: 429, fixture: "rate_limit.json"}}}
	provider := mistral.New(mistral.Options{HTTPClient: &http.Client{Transport: transport}, RetryBackoff: time.Hour})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := provider.Recognize(ctx, pdf, []int{1})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context deadline", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("backoff ignored context cancellation")
	}
}

func TestAuthFailureIsTypedAndNeverEchoesKey(t *testing.T) {
	provider, transport := newProvider(t, reply{status: 401, fixture: "auth_failure.json"})
	_, err := provider.Recognize(context.Background(), pdf, []int{1})
	if !errors.Is(err, mistral.ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	if len(transport.requests) != 1 {
		t.Errorf("requests = %d, auth failures must not retry", len(transport.requests))
	}
	if strings.Contains(err.Error(), testKey) || strings.Contains(fmt.Sprintf("%+v", provider), testKey) {
		t.Error("API key leaked into error or provider formatting")
	}
	if !strings.Contains(err.Error(), "Invalid API Key") || !strings.Contains(err.Error(), mistral.APIKeyEnv) {
		t.Errorf("err = %v, want API detail and actionable env hint", err)
	}
}

func TestMalformedResponseIsTyped(t *testing.T) {
	provider, _ := newProvider(t, reply{status: 200, fixture: "malformed.json"})
	_, err := provider.Recognize(context.Background(), pdf, []int{1})
	if !errors.Is(err, mistral.ErrMalformedResponse) {
		t.Fatalf("err = %v, want ErrMalformedResponse", err)
	}
}

func TestMissingKeyIsTypedWithoutNetwork(t *testing.T) {
	provider, transport := newProvider(t, reply{status: 200, fixture: "success.json"})
	t.Setenv(mistral.APIKeyEnv, "")
	_, err := provider.Recognize(context.Background(), pdf, []int{1})
	if !errors.Is(err, mistral.ErrMissingAPIKey) {
		t.Fatalf("err = %v, want ErrMissingAPIKey", err)
	}
	if !strings.Contains(err.Error(), mistral.APIKeyEnv) {
		t.Errorf("err = %v, want env var named", err)
	}
	if len(transport.requests) != 0 {
		t.Error("missing key must not reach the network")
	}
}

func TestProviderIdentity(t *testing.T) {
	provider := mistral.New(mistral.Options{})
	if provider.Name() != "mistral" || provider.EstPageCost() != 0.004 {
		t.Errorf("name=%q cost=%v", provider.Name(), provider.EstPageCost())
	}
}
