// Package external is the exec: OCR provider: it drives any program that
// speaks the docstomd OCR protocol (docs/ocr-protocol.md) over stdin/stdout.
//
// One engine process serves many requests (a session). A program that
// answers one request and exits is also valid: the provider starts it again
// for the next request.
package external

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/m7medVision/docstomd-go/internal/ocr"
)

// Protocol is the protocol version this client speaks.
const Protocol = 1

// MinProtocol is the oldest protocol version still accepted.
const MinProtocol = 1

// maxMessage bounds one protocol message (one JSON line).
const maxMessage = 256 << 20

// stderrTail is how much of the engine's stderr is kept for error messages.
const stderrTail = 4 << 10

// closeGrace is how long Close waits for the engine to exit after stdin
// closes before killing it.
const closeGrace = 5 * time.Second

var (
	// ErrUnavailable: the engine cannot run here (program missing, not
	// executable, or it reported that a runtime or model is missing).
	ErrUnavailable = errors.New("OCR engine unavailable")
	// ErrProtocol: the engine broke the protocol (malformed or unexpected
	// output, unsupported version, exit mid-request).
	ErrProtocol = errors.New("OCR engine broke the protocol")
)

// Config describes how to start an engine.
type Config struct {
	// Name labels the provider in cost reports and errors; defaults to the
	// engine's handshake name, then the command.
	Name    string
	Command string
	Args    []string
	// Env is added to the inherited environment as KEY=VALUE pairs.
	Env []string
	// Cost overrides the engine's reported per-page cost when non-nil.
	Cost *float64
	// Local marks an engine that runs on this machine; the engine's
	// handshake can also declare it.
	Local bool
}

// Hello is the engine's handshake message.
type Hello struct {
	Type     string  `json:"type"`
	Protocol int     `json:"protocol"`
	Engine   string  `json:"engine"`
	Version  string  `json:"version,omitempty"`
	PageCost float64 `json:"page_cost,omitempty"`
	Local    bool    `json:"local,omitempty"`
}

// Request asks the engine to recognize pages of a PDF.
type Request struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	PDF      string `json:"pdf"`
	Pages    []int  `json:"pages"`
	Password string `json:"password,omitempty"`
}

// Response is the engine's answer to one request.
type Response struct {
	Type    string       `json:"type"`
	ID      string       `json:"id"`
	Pages   []PageResult `json:"pages,omitempty"`
	Code    string       `json:"code,omitempty"`
	Message string       `json:"message,omitempty"`
}

// PageResult is one page of a response.
type PageResult struct {
	Page       int        `json:"page"`
	Markdown   *string    `json:"markdown,omitempty"`
	Lines      []ocr.Line `json:"lines,omitempty"`
	Width      float64    `json:"width,omitempty"`
	Height     float64    `json:"height,omitempty"`
	Confidence *float64   `json:"confidence,omitempty"`
}

// Error codes an engine may report in an error response.
const (
	CodeUnavailable = "unavailable"
	CodeFailed      = "failed"
)

// Provider is an ocr.Provider backed by an external engine. It is safe for
// concurrent use; requests are serialized on the one engine process.
type Provider struct {
	cfg Config

	mu      sync.Mutex
	proc    *process
	hello   *Hello
	nextID  int
	started int
	// lastStderr is the stderr tail of the last stopped engine.
	lastStderr string
}

// New returns a provider for cfg. Nothing starts until the first request.
func New(cfg Config) *Provider {
	return &Provider{cfg: cfg}
}

// Name is the configured name, else the engine's handshake name once known,
// else the command.
func (p *Provider) Name() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.nameLocked()
}

func (p *Provider) nameLocked() string {
	switch {
	case p.cfg.Name != "":
		return p.cfg.Name
	case p.hello != nil && p.hello.Engine != "":
		return p.hello.Engine
	}
	return p.cfg.Command
}

// EstPageCost is the configured cost, else the engine's handshake cost
// (0 until the engine has started).
func (p *Provider) EstPageCost() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cfg.Cost != nil {
		return *p.cfg.Cost
	}
	if p.hello != nil {
		return p.hello.PageCost
	}
	return 0
}

// Local reports whether the engine runs on this machine.
func (p *Provider) Local() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cfg.Local || (p.hello != nil && p.hello.Local)
}

// Starts reports how many engine processes this provider has started.
func (p *Provider) Starts() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.started
}

// Recognize sends one request for pages and returns the engine's results.
func (p *Provider) Recognize(ctx context.Context, doc ocr.Document, pages []int) ([]ocr.PageResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextID++
	req := Request{Type: "recognize", ID: strconv.Itoa(p.nextID), PDF: base64.StdEncoding.EncodeToString(doc.Bytes), Pages: pages}
	line, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	line = append(line, '\n')
	reused := p.proc != nil
	msg, err := p.roundTrip(ctx, line)
	if errors.Is(err, errExited) && reused && ctx.Err() == nil {
		// A single-shot engine exited after its previous answer: start it
		// again for this request.
		msg, err = p.roundTrip(ctx, line)
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, errExited) {
			return nil, p.protocolError("engine exited before answering%s", p.lastStderr)
		}
		return nil, err
	}
	var resp Response
	if err := json.Unmarshal(msg, &resp); err != nil {
		p.stop()
		return nil, p.protocolError("malformed response: %v", err)
	}
	return p.convert(req, &resp)
}

var (
	errExited   = errors.New("engine exited")
	errTooLarge = errors.New("message too large")
)

// roundTrip writes one request line and reads one message, starting the
// engine first when none runs. Canceling ctx kills the engine.
func (p *Provider) roundTrip(ctx context.Context, line []byte) ([]byte, error) {
	if p.proc == nil {
		if err := p.start(ctx); err != nil {
			return nil, err
		}
	}
	proc := p.proc
	defer context.AfterFunc(ctx, proc.kill)()
	if _, err := proc.stdin.Write(line); err != nil {
		p.stop()
		return nil, errExited
	}
	msg, err := proc.read()
	if err != nil {
		p.stop()
		if errors.Is(err, errTooLarge) {
			return nil, p.protocolError("message larger than %d bytes", maxMessage)
		}
		return nil, err
	}
	return msg, nil
}

func (p *Provider) convert(req Request, resp *Response) ([]ocr.PageResult, error) {
	switch resp.Type {
	case "error":
		msg := resp.Message
		if msg == "" {
			msg = resp.Code
		}
		if resp.Code == CodeUnavailable {
			return nil, fmt.Errorf("%w: %s: %s", ErrUnavailable, p.nameLocked(), msg)
		}
		return nil, fmt.Errorf("OCR engine %s failed: %s", p.nameLocked(), msg)
	case "result":
	default:
		p.stop()
		return nil, p.protocolError("unexpected message type %q", resp.Type)
	}
	if resp.ID != req.ID {
		p.stop()
		return nil, p.protocolError("response id %q does not match request id %q", resp.ID, req.ID)
	}
	out := make([]ocr.PageResult, 0, len(resp.Pages))
	seen := map[int]bool{}
	for _, page := range resp.Pages {
		if !slices.Contains(req.Pages, page.Page) || seen[page.Page] {
			return nil, p.protocolError("page %d was not requested or is answered twice", page.Page)
		}
		seen[page.Page] = true
		if page.Markdown == nil && page.Lines == nil {
			return nil, p.protocolError("page %d has neither markdown nor lines", page.Page)
		}
		if page.Width < 0 || page.Height < 0 {
			return nil, p.protocolError("page %d has a negative coordinate space", page.Page)
		}
		result := ocr.PageResult{Page: page.Page, Lines: page.Lines, Width: page.Width, Height: page.Height}
		if page.Markdown != nil {
			result.Markdown = *page.Markdown
		}
		if page.Confidence != nil {
			result.Confidence = *page.Confidence
		}
		out = append(out, result)
	}
	return out, nil
}

func (p *Provider) protocolError(format string, args ...any) error {
	return fmt.Errorf("%w: %s: %s", ErrProtocol, p.nameLocked(), fmt.Sprintf(format, args...))
}

// start launches the engine and reads its handshake.
func (p *Provider) start(ctx context.Context) error {
	if p.cfg.Command == "" {
		return fmt.Errorf("%w: no engine command configured", ErrUnavailable)
	}
	path, err := exec.LookPath(p.cfg.Command)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrUnavailable, p.nameLocked(), err)
	}
	cmd := exec.Command(path, p.cfg.Args...)
	cmd.Env = append(os.Environ(), p.cfg.Env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	proc := &process{cmd: cmd, stdin: stdin, stdout: bufio.NewReaderSize(stdout, 64<<10), stderr: &tailBuffer{max: stderrTail}}
	cmd.Stderr = proc.stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrUnavailable, p.nameLocked(), err)
	}
	p.started++
	p.proc = proc
	stopKill := context.AfterFunc(ctx, proc.kill)
	msg, err := proc.read()
	stopKill()
	if err != nil {
		p.stop()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return p.protocolError("no handshake from engine%s", p.lastStderr)
	}
	var hello Hello
	if err := json.Unmarshal(msg, &hello); err != nil || hello.Type != "hello" {
		p.stop()
		return p.protocolError("first message is not a hello handshake")
	}
	if hello.Protocol < MinProtocol || hello.Protocol > Protocol {
		p.stop()
		return p.protocolError("engine speaks protocol %d; this docstomd speaks %d–%d", hello.Protocol, MinProtocol, Protocol)
	}
	if hello.PageCost < 0 {
		p.stop()
		return p.protocolError("negative page cost")
	}
	p.hello = &hello
	return nil
}

// stop kills the current engine process, if any, keeping its stderr tail
// for error messages.
func (p *Provider) stop() {
	if p.proc == nil {
		return
	}
	p.proc.stdin.Close()
	p.proc.kill()
	p.proc.cmd.Wait()
	p.lastStderr = ""
	if tail := bytes.TrimSpace(p.proc.stderr.bytes()); len(tail) > 0 {
		p.lastStderr = " (stderr: " + string(tail) + ")"
	}
	p.proc = nil
}

// Close ends the session: stdin closes, the engine gets a grace period to
// exit, then it is killed.
func (p *Provider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.proc == nil {
		return nil
	}
	proc := p.proc
	p.proc = nil
	proc.stdin.Close()
	exited := make(chan error, 1)
	go func() { exited <- proc.cmd.Wait() }()
	select {
	case <-exited:
	case <-time.After(closeGrace):
		proc.cmd.Process.Kill()
		<-exited
	}
	return nil
}

func (proc *process) kill() {
	if proc.cmd.Process != nil {
		proc.cmd.Process.Kill()
	}
}

type process struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr *tailBuffer
}

// read returns the next non-empty protocol line. EOF before a full line
// means the engine exited.
func (proc *process) read() ([]byte, error) {
	for {
		var msg []byte
		for {
			chunk, err := proc.stdout.ReadSlice('\n')
			msg = append(msg, chunk...)
			if len(msg) > maxMessage {
				return nil, errTooLarge
			}
			if err == nil {
				break
			}
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			return nil, errExited
		}
		if msg = bytes.TrimSpace(msg); len(msg) > 0 {
			return msg, nil
		}
	}
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, b...)
	if len(t.buf) > t.max {
		t.buf = append([]byte{}, t.buf[len(t.buf)-t.max:]...)
	}
	return len(b), nil
}

func (t *tailBuffer) bytes() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]byte{}, t.buf...)
}
