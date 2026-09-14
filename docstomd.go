// Package docstomd converts documents (PDF, DOCX, XLSX, PPTX) to
// LLM-ready Markdown, classifying PDFs and routing only scanned pages to OCR.
package docstomd

import (
	"context"
	"errors"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/m7medVision/docstomd-go/internal/formats"
	"github.com/m7medVision/docstomd-go/internal/formats/docx"
	"github.com/m7medVision/docstomd-go/internal/formats/pptx"
	"github.com/m7medVision/docstomd-go/internal/formats/xlsx"
	"github.com/m7medVision/docstomd-go/internal/model"
	pdfocr "github.com/m7medVision/docstomd-go/internal/ocr"
	"github.com/m7medVision/docstomd-go/internal/ocr/mistral"
	"github.com/m7medVision/docstomd-go/internal/opc"
	pdfdetect "github.com/m7medVision/docstomd-go/internal/pdf/detect"
	pdfextract "github.com/m7medVision/docstomd-go/internal/pdf/extract"
	pdfmarkdown "github.com/m7medVision/docstomd-go/internal/pdf/markdown"
	"github.com/m7medVision/docstomd-go/internal/pdf/parse"
	pdfquality "github.com/m7medVision/docstomd-go/internal/pdf/quality"
	"github.com/m7medVision/docstomd-go/internal/render/gfm"
)

type Format = formats.Format

const (
	FormatUnknown = formats.Unknown
	FormatPDF     = formats.PDF
	FormatDocx    = formats.Docx
	FormatXlsx    = formats.Xlsx
	FormatPptx    = formats.Pptx
	FormatRtf     = formats.Rtf
	FormatOle     = formats.Ole
)

type Options struct {
	Format Format
	// FileName is consulted for its extension only when the content is
	// inconclusive (an empty file, a broken zip); content always wins.
	FileName string
	Markdown pdfmarkdown.Options
	OCR      OCROptions
}

// OCRProvider is the vendor seam implementations plug into.
type OCRProvider = pdfocr.Provider

// OCRDocument is the provider call payload.
type OCRDocument = pdfocr.Document

type OCRMode = pdfocr.Mode

const (
	OCROff   = pdfocr.Off
	OCRAuto  = pdfocr.Auto
	OCRForce = pdfocr.Force
)

type OCROptions struct {
	Mode           OCRMode
	Provider       OCRProvider
	MaxPagesPerDoc int
	MaxPagesPerRun int
	// Run carries MaxPagesPerRun across Convert calls: calls sharing one
	// OCRRun draw on one page budget. Without it each call is its own run.
	Run    *OCRRun
	DryRun bool
}

// OCRRun is a page budget shared by the Convert calls that carry it. The zero
// value is ready and it is safe for concurrent use.
type OCRRun = pdfocr.Budget

// MistralOptions configures the Mistral provider; the API key is read from
// the MISTRAL_API_KEY environment variable only.
type MistralOptions = mistral.Options

var (
	ErrOCRMissingKey   = mistral.ErrMissingAPIKey
	ErrOCRUnauthorized = mistral.ErrUnauthorized
	ErrOCRRateLimited  = mistral.ErrRateLimited
)

// NewMistralProvider returns the Mistral OCR provider, the default when
// OCROptions.Provider is nil.
func NewMistralProvider(opts MistralOptions) OCRProvider {
	return mistral.New(opts)
}

// OCRCostReport summarizes what a run billed (or would bill, dry-run).
type OCRCostReport = pdfocr.CostReport

type PDFDetection = pdfdetect.Result

// TextItem mirrors the extraction item payload for the debug surface.
type TextItem = pdfextract.TextItem

type ItemsResult struct {
	Items             []TextItem `json:"items"`
	TotalItems        int        `json:"total_items"`
	HasEncodingIssues bool       `json:"has_encoding_issues"`
}

type Detection struct {
	Format Format        `json:"format"`
	PDF    *PDFDetection `json:"pdf,omitempty"`
}

type Result struct {
	Markdown          string            `json:"markdown"`
	Format            Format            `json:"format"`
	PageCount         int               `json:"page_count"`
	PagesNeedingOCR   []int             `json:"pages_needing_ocr"`
	OCRReasons        map[int][]string  `json:"ocr_reasons_by_page,omitempty"`
	Layout            *LayoutComplexity `json:"layout,omitempty"`
	DurationMS        int64             `json:"processing_time_ms"`
	HasEncodingIssues bool              `json:"has_encoding_issues"`
	OCRCost           *OCRCostReport    `json:"ocr_cost,omitempty"`
	NeedsReview       []int             `json:"needs_review,omitempty"`
}

// LayoutComplexity reports whether layout-sensitive conversion (tables,
// columns) is present, per page.
type LayoutComplexity = pdfmarkdown.Complexity

func Detect(ctx context.Context, r io.Reader, name string) (*Detection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	format := formats.Detect(data, name)
	detection := &Detection{Format: format}
	if format == FormatPDF {
		result, err := pdfdetect.Detect(data)
		if err != nil {
			return nil, mapPDFError(err)
		}
		detection.PDF = result
	}
	return detection, nil
}

func mapPDFError(err error) error {
	if errors.Is(err, parse.ErrEncrypted) {
		return Encrypted()
	}
	var malformed *parse.MalformedError
	if errors.As(err, &malformed) {
		return Malformed("pdf", malformed.Detail)
	}
	return err
}

func orderOCRReasons(list []string) []string {
	priority := map[string]int{
		pdfdetect.ReasonSuspectedGarble: 0,
		pdfdetect.ReasonVectorText:      1,
		pdfdetect.ReasonScanned:         2,
		pdfdetect.ReasonNoText:          3,
	}
	out := append([]string{}, list...)
	sort.SliceStable(out, func(a, b int) bool {
		return priority[out[a]] < priority[out[b]]
	})
	return out
}

func dedupInts(list []int) []int {
	out := make([]int, 0, len(list))
	for i, v := range list {
		if i == 0 || list[i-1] != v {
			out = append(out, v)
		}
	}
	return out
}

func stripRoutedPages(pages []pdfextract.PageResult, routed []int) []pdfextract.PageResult {
	routedSet := map[int]bool{}
	for _, p := range routed {
		routedSet[p] = true
	}
	for i := range pages {
		pageNum := i + 1
		if len(pages[i].Items) > 0 {
			pageNum = pages[i].Items[0].Page
		}
		if routedSet[pageNum] {
			pages[i].Items = nil
		}
	}
	return pages
}

// ExtractItems dumps positioned extraction items for PDF input — the debug
// surface behind `docstomd convert --items-json`.
func ExtractItems(ctx context.Context, r io.Reader) (*ItemsResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if formats.Detect(data, "") != FormatPDF {
		return nil, Unsupported(FormatUnknown.String())
	}
	doc, err := parse.Parse(data)
	if err != nil {
		return nil, mapPDFError(err)
	}
	pages := pdfextract.Extract(doc)
	result := &ItemsResult{}
	for _, page := range pages {
		result.Items = append(result.Items, page.Items...)
		if page.HasEncodingIssues {
			result.HasEncodingIssues = true
		}
	}
	result.TotalItems = len(result.Items)
	return result, nil
}

func convertOffice(start time.Time, f Format, data []byte, parse func([]byte) (*model.Document, error)) (*Result, error) {
	doc, err := parse(data)
	if err != nil {
		return nil, mapOfficeError(err)
	}
	return &Result{Markdown: gfm.Render(doc), Format: f, DurationMS: time.Since(start).Milliseconds()}, nil
}

func Convert(ctx context.Context, r io.Reader, opts Options) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	start := time.Now()
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if opc.IsEncrypted(data) {
		return nil, Encrypted()
	}
	f := opts.Format
	if f == FormatUnknown {
		f = formats.Detect(data, opts.FileName)
	}
	switch f {
	case FormatPDF:
	case FormatDocx:
		return convertOffice(start, f, data, docx.Parse)
	case FormatPptx:
		return convertOffice(start, f, data, pptx.Parse)
	case FormatXlsx:
		return convertOffice(start, f, data, xlsx.Parse)
	default:
		return nil, Unsupported(f.String())
	}
	doc, err := parse.Parse(data)
	if err != nil {
		return nil, mapPDFError(err)
	}
	detection := pdfdetect.FromDocument(doc, pdfdetect.DefaultConfig())
	needsOcr := detection.Type == pdfdetect.TypeScanned || detection.Type == pdfdetect.TypeImageBase
	if needsOcr && opts.OCR.Mode == pdfocr.Off {
		return nil, NeedsOcr(detection.PagesNeedingOCR, detection.PageCount)
	}
	pages := pdfextract.Extract(doc)
	var allItems []pdfextract.TextItem
	for _, page := range pages {
		allItems = append(allItems, page.Items...)
	}
	qualityReport := pdfquality.AnalyzeItems(allItems)
	if detection.Type == pdfdetect.TypeTextBased {
		pages = stripRoutedPages(pages, detection.PagesNeedingOCR)
	}
	mdOpts := opts.Markdown
	if mdOpts == (pdfmarkdown.Options{}) {
		mdOpts = pdfmarkdown.DefaultOptions()
	}
	md, complexity := pdfmarkdown.Convert(pages, mdOpts)
	var ocrResult *pdfocr.Result
	routed := append([]int{}, detection.PagesNeedingOCR...)
	routed = append(routed, qualityReport.PagesNeedingOCR...)
	sort.Ints(routed)
	routed = dedupInts(routed)
	if opts.OCR.Mode != pdfocr.Off && (needsOcr || opts.OCR.Mode == pdfocr.Force || len(routed) > 0 || opts.OCR.DryRun) {
		provider := opts.OCR.Provider
		if provider == nil {
			provider = NewMistralProvider(MistralOptions{})
		}
		router := &pdfocr.Router{MaxPagesPerDoc: opts.OCR.MaxPagesPerDoc, MaxPagesPerRun: opts.OCR.MaxPagesPerRun, Budget: opts.OCR.Run}
		ocrResult, err = router.Run(ctx, provider, pdfocr.Document{Bytes: data}, opts.OCR.Mode, routed, detection.PageCount, opts.OCR.DryRun)
		if err != nil {
			return nil, err
		}
		if len(ocrResult.PageMarkdown) > 0 {
			var parts []string
			for _, pm := range pdfmarkdown.ConvertPerPage(pages, mdOpts) {
				if replaced, ok := ocrResult.PageMarkdown[pm.Page]; ok {
					parts = append(parts, replaced)
				} else if strings.TrimSpace(pm.Markdown) != "" {
					parts = append(parts, pm.Markdown)
				}
			}
			md = strings.Join(parts, "\n\n")
		}
	}
	hasIssues := false
	for _, page := range pages {
		if page.HasEncodingIssues {
			hasIssues = true
		}
	}
	reasons := map[int][]string{}
	for _, entry := range detection.OCRReasonsByPage {
		reasons[entry.Page] = entry.Reasons
	}
	for _, p := range qualityReport.PagesNeedingOCR {
		reasons[p] = pdfquality.AppendReason(reasons[p], pdfquality.ReasonSuspectedGarble)
	}
	for p, list := range reasons {
		reasons[p] = orderOCRReasons(list)
	}
	if len(reasons) == 0 {
		reasons = nil
	}
	var ocrCost *OCRCostReport
	var needsReview []int
	if ocrResult != nil {
		ocrCost = &ocrResult.Cost
		needsReview = ocrResult.NeedsReview
	}
	return &Result{
		OCRCost:           ocrCost,
		NeedsReview:       needsReview,
		Markdown:          md,
		Format:            FormatPDF,
		PageCount:         detection.PageCount,
		PagesNeedingOCR:   routed,
		OCRReasons:        reasons,
		Layout:            &complexity,
		DurationMS:        time.Since(start).Milliseconds(),
		HasEncodingIssues: hasIssues,
	}, nil
}
