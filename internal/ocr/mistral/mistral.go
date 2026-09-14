// Package mistral is the Mistral OCR provider: one batched document call per
// Recognize, authenticated from the environment only.
package mistral

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
	"strconv"
	"time"

	"github.com/m7medVision/docstomd-go/internal/ocr"
)

const (
	APIKeyEnv       = "MISTRAL_API_KEY"
	DefaultModel    = "mistral-ocr-latest"
	PricePerPageUSD = 0.004
	endpoint        = "https://api.mistral.ai/v1/ocr"
	maxRetries      = 3
	defaultBackoff  = time.Second
)

var (
	ErrMissingAPIKey     = errors.New("mistral: " + APIKeyEnv + " is not set; export it to enable OCR, or use a dry run to estimate cost without calling the API")
	ErrUnauthorized      = errors.New("mistral: authentication failed; check " + APIKeyEnv)
	ErrRateLimited       = errors.New("mistral: rate limited")
	ErrMalformedResponse = errors.New("mistral: malformed response")
)

type Options struct {
	Model        string
	HTTPClient   *http.Client
	RetryBackoff time.Duration
}

// Provider holds no credentials; the key is read from the environment per call
// so it can never be formatted, logged, or echoed from the value.
type Provider struct {
	model   string
	client  *http.Client
	backoff time.Duration
}

func New(opts Options) *Provider {
	p := &Provider{model: opts.Model, client: opts.HTTPClient, backoff: opts.RetryBackoff}
	if p.model == "" {
		p.model = DefaultModel
	}
	if p.client == nil {
		p.client = http.DefaultClient
	}
	if p.backoff == 0 {
		p.backoff = defaultBackoff
	}
	return p
}

func (p *Provider) Name() string { return "mistral" }

func (p *Provider) EstPageCost() float64 { return PricePerPageUSD }

type documentURL struct {
	Type        string `json:"type"`
	DocumentURL string `json:"document_url"`
}

type request struct {
	Model                 string      `json:"model"`
	Document              documentURL `json:"document"`
	Pages                 []int       `json:"pages"`
	ConfidenceGranularity string      `json:"confidence_scores_granularity"`
}

type response struct {
	Pages []struct {
		Index      int    `json:"index"`
		Markdown   string `json:"markdown"`
		Dimensions struct {
			DPI int `json:"dpi"`
		} `json:"dimensions"`
		ConfidenceScores *struct {
			Average float64 `json:"average_page_confidence_score"`
		} `json:"confidence_scores"`
		Blocks []struct {
			TopLeftX     float64 `json:"top_left_x"`
			TopLeftY     float64 `json:"top_left_y"`
			BottomRightX float64 `json:"bottom_right_x"`
			BottomRightY float64 `json:"bottom_right_y"`
		} `json:"blocks"`
	} `json:"pages"`
}

func (p *Provider) Recognize(ctx context.Context, doc ocr.Document, pages []int) ([]ocr.PageResult, error) {
	key := os.Getenv(APIKeyEnv)
	if key == "" {
		return nil, ErrMissingAPIKey
	}
	req := request{
		Model:                 p.model,
		Document:              documentURL{Type: "document_url", DocumentURL: "data:application/pdf;base64," + base64.StdEncoding.EncodeToString(doc.Bytes)},
		ConfidenceGranularity: "page",
	}
	for _, page := range pages {
		req.Pages = append(req.Pages, page-1)
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		resp, err := p.post(ctx, key, body)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusOK {
			pages, err := decode(resp.Body)
			_ = resp.Body.Close()
			return pages, err
		}
		detail := apiMessage(resp.Body)
		_ = resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			return nil, fmt.Errorf("%w (HTTP %d: %s)", ErrUnauthorized, resp.StatusCode, detail)
		case resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500:
			return nil, fmt.Errorf("mistral: HTTP %d: %s", resp.StatusCode, detail)
		case attempt == maxRetries && resp.StatusCode == http.StatusTooManyRequests:
			return nil, fmt.Errorf("%w after %d attempts: %s", ErrRateLimited, attempt+1, detail)
		case attempt == maxRetries:
			return nil, fmt.Errorf("mistral: HTTP %d after %d attempts: %s", resp.StatusCode, attempt+1, detail)
		}
		if err := sleep(ctx, p.wait(attempt, resp.Header.Get("Retry-After"))); err != nil {
			return nil, err
		}
	}
}

func (p *Provider) post(ctx context.Context, key string, body []byte) (*http.Response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+key)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	return p.client.Do(httpReq)
}

func (p *Provider) wait(attempt int, retryAfter string) time.Duration {
	if seconds, err := strconv.Atoi(retryAfter); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	return p.backoff << attempt
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func decode(r io.Reader) ([]ocr.PageResult, error) {
	var resp response
	if err := json.NewDecoder(r).Decode(&resp); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformedResponse, err)
	}
	out := make([]ocr.PageResult, 0, len(resp.Pages))
	for _, page := range resp.Pages {
		result := ocr.PageResult{Page: page.Index + 1, Markdown: page.Markdown}
		if page.ConfidenceScores != nil {
			result.Confidence = page.ConfidenceScores.Average
		}
		if page.Dimensions.DPI <= 0 {
			return nil, fmt.Errorf("%w: page %d has dpi %d", ErrMalformedResponse, page.Index, page.Dimensions.DPI)
		}
		scale := 72 / float64(page.Dimensions.DPI)
		for _, b := range page.Blocks {
			result.BBoxes = append(result.BBoxes, ocr.Box{
				Page: result.Page,
				X0:   b.TopLeftX * scale,
				Y0:   b.TopLeftY * scale,
				X1:   b.BottomRightX * scale,
				Y1:   b.BottomRightY * scale,
			})
		}
		out = append(out, result)
	}
	return out, nil
}

func apiMessage(r io.Reader) string {
	data, _ := io.ReadAll(io.LimitReader(r, 64<<10))
	var body struct {
		Message string `json:"message"`
		Detail  string `json:"detail"`
	}
	if json.Unmarshal(data, &body) != nil {
		return string(bytes.TrimSpace(data))
	}
	if body.Message != "" {
		return body.Message
	}
	return body.Detail
}
