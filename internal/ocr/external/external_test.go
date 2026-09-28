package external_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/m7medVision/docstomd-go/internal/ocr"
	"github.com/m7medVision/docstomd-go/internal/ocr/external"
	"github.com/m7medVision/docstomd-go/internal/ocr/external/externaltest"
)

// The test binary doubles as the stub engine: with DOCSTOMD_STUB set it
// speaks the protocol in the named mode instead of running tests.
func TestMain(m *testing.M) {
	if mode := os.Getenv(externaltest.EnvMode); mode != "" {
		os.Exit(externaltest.Stub(mode))
	}
	os.Exit(m.Run())
}

func stubProvider(t *testing.T, mode string) *external.Provider {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p := external.New(external.Config{Command: exe, Env: []string{externaltest.EnvMode + "=" + mode}})
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func TestMarkdownReply(t *testing.T) {
	p := stubProvider(t, "markdown")
	got, err := p.Recognize(context.Background(), ocr.Document{Bytes: []byte("doc")}, []int{1, 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Markdown != "page 1 of doc" || got[1].Page != 3 {
		t.Errorf("results = %+v", got)
	}
	if p.Name() != "stub" || p.EstPageCost() != 0.5 || p.Local() {
		t.Errorf("handshake: name=%q cost=%v local=%v", p.Name(), p.EstPageCost(), p.Local())
	}
}

func TestLinesReply(t *testing.T) {
	p := stubProvider(t, "lines")
	got, err := p.Recognize(context.Background(), ocr.Document{Bytes: []byte("doc")}, []int{2})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[0].Lines) != 1 || got[0].Width != 1000 || got[0].Lines[0].Text != "line on page 2 of doc" {
		t.Errorf("results = %+v", got)
	}
	if !p.Local() {
		t.Error("handshake local flag lost")
	}
}

func TestSessionServesManyRequests(t *testing.T) {
	p := stubProvider(t, "markdown")
	for i := range 3 {
		if _, err := p.Recognize(context.Background(), ocr.Document{Bytes: fmt.Appendf(nil, "doc%d", i)}, []int{1}); err != nil {
			t.Fatal(err)
		}
	}
	if p.Starts() != 1 {
		t.Errorf("engine started %d times, want 1", p.Starts())
	}
}

func TestSingleShotEngineRestarts(t *testing.T) {
	p := stubProvider(t, "single")
	for i := range 3 {
		got, err := p.Recognize(context.Background(), ocr.Document{Bytes: fmt.Appendf(nil, "doc%d", i)}, []int{1})
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if want := fmt.Sprintf("page 1 of doc%d", i); got[0].Markdown != want {
			t.Errorf("request %d = %q, want %q", i, got[0].Markdown, want)
		}
	}
	if p.Starts() != 3 {
		t.Errorf("engine started %d times, want 3", p.Starts())
	}
}

func TestErrors(t *testing.T) {
	cases := []struct {
		mode string
		want error
		text string
	}{
		{"nohello", external.ErrProtocol, "model file missing"},
		{"badhello", external.ErrProtocol, "hello"},
		{"version2", external.ErrProtocol, "protocol 2"},
		{"malformed", external.ErrProtocol, "malformed"},
		{"extrapage", external.ErrProtocol, "page 99"},
		{"empty", external.ErrProtocol, "neither markdown nor lines"},
		{"wrongid", external.ErrProtocol, "id"},
		{"crash", external.ErrProtocol, "segfault in detector"},
		{"unavailable", external.ErrUnavailable, "not installed"},
		{"failed", nil, "out of memory"},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			p := stubProvider(t, tc.mode)
			_, err := p.Recognize(context.Background(), ocr.Document{Bytes: []byte("doc")}, []int{1})
			if err == nil {
				t.Fatal("want error")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
			if tc.want == nil && (errors.Is(err, external.ErrProtocol) || errors.Is(err, external.ErrUnavailable)) {
				t.Errorf("engine failure misclassified: %v", err)
			}
			if !strings.Contains(err.Error(), tc.text) || !strings.Contains(err.Error(), "docstomd") && !strings.Contains(err.Error(), "stub") && !strings.Contains(err.Error(), "external.test") {
				t.Errorf("err = %q, want it to name the provider and contain %q", err, tc.text)
			}
		})
	}
}

func TestMissingProgramIsUnavailable(t *testing.T) {
	p := external.New(external.Config{Command: "/nonexistent/ocr-engine"})
	_, err := p.Recognize(context.Background(), ocr.Document{}, []int{1})
	if !errors.Is(err, external.ErrUnavailable) || !strings.Contains(err.Error(), "/nonexistent/ocr-engine") {
		t.Errorf("err = %v", err)
	}
}

func TestCancelKillsHungEngine(t *testing.T) {
	p := stubProvider(t, "hang")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := p.Recognize(ctx, ocr.Document{}, []int{1})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("cancel took %v", time.Since(start))
	}
}

func TestPublishedSchemaMatchesProtocol(t *testing.T) {
	data, err := os.ReadFile("../../../docs/schemas/ocr-protocol-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Defs map[string]struct {
			Properties map[string]struct {
				Const any `json:"const"`
			} `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	if got := schema.Defs["hello"].Properties["protocol"].Const; got != float64(external.Protocol) {
		t.Errorf("schema protocol const = %v, want %d", got, external.Protocol)
	}
	for _, def := range []string{"hello", "recognize", "result", "error", "page", "line", "box"} {
		if _, ok := schema.Defs[def]; !ok {
			t.Errorf("schema lacks $defs.%s", def)
		}
	}
}

func TestStubConforms(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"markdown", "lines"} {
		t.Run(mode, func(t *testing.T) {
			externaltest.Conformance(t, external.Config{Command: exe, Env: []string{externaltest.EnvMode + "=" + mode}}, []byte("%PDF-stub"), []int{1, 2})
		})
	}
}
