// Package ocr routes pages to an OCR provider under cost guardrails and
// fuses the results with native page content.
package ocr

import (
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/m7medVision/docstomd-go/internal/pdf/quality"
)

type Mode int

const (
	Off Mode = iota
	Auto
	Force
)

// Document is the provider call payload: raw PDF bytes plus optional password.
type Document struct {
	Bytes    []byte
	Password string
}

// Box is a provider annotation rectangle in page coordinates.
type Box struct {
	Page int     `json:"page"`
	X0   float64 `json:"x0"`
	Y0   float64 `json:"y0"`
	X1   float64 `json:"x1"`
	Y1   float64 `json:"y1"`
}

// PageResult is one provider-recognized page.
type PageResult struct {
	Page       int     `json:"page"`
	Markdown   string  `json:"markdown"`
	Confidence float64 `json:"confidence"`
	BBoxes     []Box   `json:"bboxes,omitempty"`
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
		if healthy(ocrPage) {
			result.PageMarkdown[page] = ocrPage.Markdown
		} else {
			result.NeedsReview = append(result.NeedsReview, page)
		}
	}
	return result, nil
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
