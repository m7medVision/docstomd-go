package externaltest

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"slices"
	"testing"
	"time"

	"github.com/m7medVision/docstomd-go/internal/ocr"
	"github.com/m7medVision/docstomd-go/internal/ocr/external"
)

// Conformance checks that the engine cfg starts speaks protocol v1: a valid
// handshake, an answer for each requested page of pdf, a second request in
// the same session, requests with unknown fields, and a prompt exit when
// stdin closes.
func Conformance(t *testing.T, cfg external.Config, pdf []byte, pages []int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	p := external.New(cfg)
	defer func() { _ = p.Close() }()
	for round := range 2 {
		results, err := p.Recognize(ctx, ocr.Document{Bytes: pdf}, pages)
		if err != nil {
			t.Fatalf("request %d: %v", round+1, err)
		}
		var got []int
		for _, r := range results {
			got = append(got, r.Page)
			if r.Markdown == "" && len(r.Lines) == 0 {
				t.Errorf("request %d page %d: no markdown and no lines", round+1, r.Page)
			}
		}
		slices.Sort(got)
		if !slices.Equal(got, pages) {
			t.Errorf("request %d answered pages %v, want %v", round+1, got, pages)
		}
	}
	if p.Starts() != 1 {
		t.Errorf("engine started %d times for two requests; a session engine starts once", p.Starts())
	}
	if p.Name() == "" {
		t.Error("handshake has no engine name")
	}
	start := time.Now()
	_ = p.Close()
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("engine took %v to exit after stdin closed", d)
	}

	// Speak the protocol by hand: unknown fields must be ignored.
	path, err := exec.LookPath(cfg.Command)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, path, cfg.Args...)
	cmd.Env = append(os.Environ(), cfg.Env...)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Wait() }()
	defer func() { _ = stdin.Close() }()
	out := bufio.NewReader(stdout)
	var hello map[string]any
	readJSON(t, out, &hello)
	if hello["type"] != "hello" || hello["protocol"] != float64(1) || hello["engine"] == "" {
		t.Errorf("hello = %v", hello)
	}
	req, _ := json.Marshal(map[string]any{
		"type": "recognize", "id": "conformance-7", "pdf": base64.StdEncoding.EncodeToString(pdf),
		"pages": pages[:1], "x_future_field": map[string]any{"a": 1},
	})
	if _, err := stdin.Write(append(req, '\n')); err != nil {
		t.Fatal(err)
	}
	var resp map[string]any
	readJSON(t, out, &resp)
	if resp["type"] != "result" || resp["id"] != "conformance-7" {
		t.Errorf("reply to a request with unknown fields = %v", resp)
	}
}

func readJSON(t *testing.T, r *bufio.Reader, v any) {
	t.Helper()
	line, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatalf("reading engine output: %v", err)
	}
	if err := json.Unmarshal(line, v); err != nil {
		t.Fatalf("engine wrote a non-JSON line %q: %v", line, err)
	}
}
