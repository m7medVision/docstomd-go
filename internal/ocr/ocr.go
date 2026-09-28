// Package ocr routes pages to an OCR provider under cost guardrails and
// fuses the results with native page content.
package ocr

import (
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/m7medVision/docstomd-go/internal/pdf/markdown"
	"github.com/m7medVision/docstomd-go/internal/pdf/quality"
)

type Mode int

const (
	Off Mode = iota
	Auto
	Force
)

// Document is the provider call payload: the raw PDF bytes.
type Document struct {
	Bytes []byte
}

// Box is a provider annotation rectangle in page coordinates.
type Box struct {
	Page int     `json:"page"`
	X0   float64 `json:"x0"`
	Y0   float64 `json:"y0"`
	X1   float64 `json:"x1"`
	Y1   float64 `json:"y1"`
}

// PageResult is one provider-recognized page. A provider answers with
// Markdown, or with Lines that the router renders through the PDF Markdown
// rules; Markdown wins when both are set. Width and Height are the size of
// the coordinate space the line boxes use (for example the page image in
// pixels); zero means points.
type PageResult struct {
	Page       int     `json:"page"`
	Markdown   string  `json:"markdown"`
	Confidence float64 `json:"confidence"`
	BBoxes     []Box   `json:"bboxes,omitempty"`
	Lines      []Line  `json:"lines,omitempty"`
	Width      float64 `json:"width,omitempty"`
	Height     float64 `json:"height,omitempty"`
}

// Provider is the vendor seam: recognize a document's listed pages in one
// batched call. Implementations report their per-page price for estimates.
type Provider interface {
	Name() string
	EstPageCost() float64
	Recognize(ctx context.Context, doc Document, pages []int) ([]PageResult, error)
}

// CostReport summarizes what a run billed (or would bill, dry-run).
type CostReport struct {
	Provider         string  `json:"provider"`
	PagesBilled      int     `json:"pages_billed"`
	BilledPages      []int   `json:"billed_pages"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd"`
	DryRun           bool    `json:"dry_run"`
	Truncated        bool    `json:"truncated_by_caps"`
	// Local is set when the provider runs on this machine: nothing is
	// billed, and PagesBilled counts the pages it processed.
	Local bool `json:"local,omitempty"`
}

// LocalProvider is implemented by providers that may run on this machine.
type LocalProvider interface {
	Local() bool
}

func isLocal(p Provider) bool {
	l, ok := p.(LocalProvider)
	return ok && l.Local()
}

// Result carries the router outcome for fusion.
type Result struct {
	PageMarkdown map[int]string
	NeedsReview  []int
	Cost         CostReport
}

// Budget counts the pages billed across every router call that shares it, so
// MaxPagesPerRun spans them. The zero value is ready; it is safe for
// concurrent use.
type Budget struct {
	mu     sync.Mutex
	billed int
}

// Router applies caps and dry-run estimates. Routers sharing a Budget share
// one run; a Router without one keeps its own.
type Router struct {
	MaxPagesPerDoc int
	MaxPagesPerRun int
	Budget         *Budget
	// Lines renders a line-based page result as Markdown; nil renders it
	// with the default Markdown options, treating box coordinates as points.
	Lines func(PageResult) string
}

// selectPages returns the pages the mode bills, after caps, plus the pages
// the caps dropped (they keep their native content unreviewed). Unless dry,
// the billed pages are reserved against the budget before any provider call,
// so concurrent calls cannot overspend the run.
func (r *Router) selectPages(mode Mode, routed []int, pageCount int, dryRun bool) (pages []int, dropped []int, truncated bool) {
	switch mode {
	case Force:
		for p := 1; p <= pageCount; p++ {
			pages = append(pages, p)
		}
	case Auto:
		pages = append([]int{}, routed...)
		slices.Sort(pages)
		pages = slices.Compact(pages)
	default:
		return nil, nil, false
	}
	full := pages
	if r.MaxPagesPerDoc > 0 && len(pages) > r.MaxPagesPerDoc {
		pages = pages[:r.MaxPagesPerDoc]
		truncated = true
	}
	if r.Budget == nil {
		r.Budget = &Budget{}
	}
	r.Budget.mu.Lock()
	if left := r.MaxPagesPerRun - r.Budget.billed; r.MaxPagesPerRun > 0 && len(pages) > left {
		pages = pages[:max(0, left)]
		truncated = true
	}
	if !dryRun {
		r.Budget.billed += len(pages)
	}
	r.Budget.mu.Unlock()
	if len(pages) < len(full) {
		dropped = full[len(pages):]
	}
	return pages, dropped, truncated
}

// Run routes, calls the provider once per document, validates OCR output
// through the text-quality checks, and fuses: healthy OCR replaces the page's
// native markdown; weak or garbled OCR keeps native and flags needs-review.
func (r *Router) Run(ctx context.Context, provider Provider, doc Document, mode Mode, routed []int, pageCount int, dryRun bool) (*Result, error) {
	pages, dropped, truncated := r.selectPages(mode, routed, pageCount, dryRun)
	result := &Result{
		PageMarkdown: map[int]string{},
		Cost: CostReport{
			Provider:         provider.Name(),
			PagesBilled:      len(pages),
			BilledPages:      append([]int{}, pages...),
			EstimatedCostUSD: float64(len(pages)) * provider.EstPageCost(),
			DryRun:           dryRun,
			Truncated:        truncated,
			Local:            isLocal(provider),
		},
		NeedsReview: dropped,
	}
	if dryRun || len(pages) == 0 {
		return result, nil
	}
	recognized, err := provider.Recognize(ctx, doc, pages)
	if err != nil {
		r.Budget.mu.Lock()
		r.Budget.billed -= len(pages)
		r.Budget.mu.Unlock()
		return nil, err
	}
	// External engines learn their name and price in their handshake, which
	// only happens on the first call.
	result.Cost.Provider = provider.Name()
	result.Cost.EstimatedCostUSD = float64(len(pages)) * provider.EstPageCost()
	result.Cost.Local = isLocal(provider)
	byPage := map[int]PageResult{}
	for _, page := range recognized {
		byPage[page.Page] = page
	}
	for _, page := range pages {
		ocrPage, ok := byPage[page]
		if !ok {
			result.NeedsReview = append(result.NeedsReview, page)
			continue
		}
		if strings.TrimSpace(ocrPage.Markdown) == "" && len(ocrPage.Lines) > 0 {
			ocrPage.Markdown = r.renderLines(ocrPage)
			if ocrPage.Confidence == 0 {
				ocrPage.Confidence = linesConfidence(ocrPage.Lines)
			}
		}
		if healthy(ocrPage) {
			result.PageMarkdown[page] = ocrPage.Markdown
		} else {
			result.NeedsReview = append(result.NeedsReview, page)
		}
	}
	return result, nil
}

func (r *Router) renderLines(page PageResult) string {
	if r.Lines != nil {
		return r.Lines(page)
	}
	return LinesMarkdown(page, 0, 0, markdown.DefaultOptions())
}

func healthy(page PageResult) bool {
	if page.Confidence > 0 && page.Confidence < 0.5 {
		return false
	}
	if strings.TrimSpace(page.Markdown) == "" {
		return false
	}
	return !quality.Analyze(page.Markdown)
}
