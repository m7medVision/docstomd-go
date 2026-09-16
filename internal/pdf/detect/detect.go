// Package detect classifies PDFs and routes pages to OCR, mirroring the
// reference detector's heuristics and thresholds.
package detect

import (
	"slices"

	"github.com/m7medVision/docstomd-go/internal/pdf/parse"
)

type Type string

const (
	TypeTextBased Type = "text_based"
	TypeScanned   Type = "scanned"
	TypeImageBase Type = "image_based"
	TypeMixed     Type = "mixed"
)

const (
	ReasonScanned         = "scanned"
	ReasonNoText          = "no_text"
	ReasonVectorText      = "vector_text"
	ReasonSuspectedGarble = "suspected_garbled_text"
)

type Strategy struct {
	Kind  StrategyKind
	Max   int
	Pages []int
}

type StrategyKind int

const (
	Sample StrategyKind = iota
	Full
	Pages
	EarlyExit
)

type Config struct {
	Strategy               Strategy
	MinTextOpsPerPage      int
	TextPageRatioThreshold float64
}

func DefaultConfig() Config {
	return Config{
		Strategy:               Strategy{Kind: Sample, Max: 8},
		MinTextOpsPerPage:      3,
		TextPageRatioThreshold: 0.6,
	}
}

type PageReasons struct {
	Page    int      `json:"page"`
	Reasons []string `json:"reasons"`
}

type Result struct {
	Type             Type          `json:"pdf_type"`
	PageCount        int           `json:"page_count"`
	PagesSampled     int           `json:"pages_sampled"`
	PagesWithText    int           `json:"pages_with_text"`
	Confidence       float64       `json:"confidence"`
	Title            *string       `json:"title"`
	OCRRecommended   bool          `json:"ocr_recommended"`
	PagesNeedingOCR  []int         `json:"pages_needing_ocr"`
	OCRReasonsByPage []PageReasons `json:"ocr_reasons_by_page"`
}

func Detect(data []byte) (*Result, error) {
	doc, err := parse.Parse(data)
	if err != nil {
		return nil, err
	}
	return FromDocument(doc, DefaultConfig()), nil
}

func FromDocument(doc *parse.Document, cfg Config) *Result {
	pages := doc.Pages()
	totalPages := len(pages)

	var sampleIndices []int
	allowEarlyExit := false
	switch cfg.Strategy.Kind {
	case Full:
		for i := 1; i <= totalPages; i++ {
			sampleIndices = append(sampleIndices, i)
		}
	case EarlyExit:
		allowEarlyExit = true
		for i := 1; i <= totalPages; i++ {
			sampleIndices = append(sampleIndices, i)
		}
	case Pages:
		seen := map[int]bool{}
		for _, p := range cfg.Strategy.Pages {
			if p >= 1 && p <= totalPages && !seen[p] {
				seen[p] = true
				sampleIndices = append(sampleIndices, p)
			}
		}
		slices.Sort(sampleIndices)
	default:
		sampleIndices = distributePages(cfg.Strategy.Max, totalPages)
	}

	pagesWithText := 0
	pagesWithImages := 0
	pagesWithTemplateImages := 0
	pagesWithVectorText := 0
	totalTextOps := 0
	analysisCache := map[int]pageAnalysis{}
	pagesSampled := 0

	for _, pageNum := range sampleIndices {
		analysis := analyzePageContent(doc, pages[pageNum-1])
		pagesSampled++
		isImageDominated := analysis.imageCount > 10 && analysis.imageCount > analysis.textOperatorCount*3
		effectiveMinOps := cfg.MinTextOpsPerPage
		if analysis.hasImages || analysis.imageCount > 0 {
			effectiveMinOps = max(cfg.MinTextOpsPerPage, 10)
		}
		if analysis.textOperatorCount >= effectiveMinOps &&
			!isImageDominated &&
			analysis.uniqueTextChars >= 5 &&
			!analysis.hasVectorText &&
			!analysis.hasOnlyType3Fonts {
			pagesWithText++
		}
		if analysis.hasImages {
			pagesWithImages++
		}
		if analysis.hasTemplateImage && analysis.looksLikeScan() {
			pagesWithTemplateImages++
		}
		if analysis.hasVectorText {
			pagesWithVectorText++
		}
		totalTextOps += analysis.textOperatorCount
		analysisCache[pageNum] = analysis

		if allowEarlyExit &&
			(analysis.textOperatorCount < cfg.MinTextOpsPerPage || isImageDominated || analysis.uniqueTextChars < 5) &&
			(analysis.hasImages || analysis.hasTemplateImage) {
			break
		}
	}

	textRatio := 0.0
	if pagesSampled > 0 {
		textRatio = float64(pagesWithText) / float64(pagesSampled)
	}
	hasTemplateImages := pagesWithTemplateImages > 0
	templateRatio := 0.0
	if pagesSampled > 0 {
		templateRatio = float64(pagesWithTemplateImages) / float64(pagesSampled)
	}

	var pdfType Type
	var confidence float64
	ocrRecommended := false
	switch {
	case hasTemplateImages && pagesWithText > 0:
		ocrRecommended = true
		pdfType = TypeMixed
		confidence = 0.5 + 0.3*(1.0-templateRatio)
	case textRatio >= cfg.TextPageRatioThreshold:
		pdfType = TypeTextBased
		confidence = textRatio
	case pagesWithText == 0 && (pagesWithImages > 0 || pagesWithVectorText > 0):
		ocrRecommended = true
		if totalTextOps == 0 && pagesWithVectorText == 0 {
			pdfType = TypeScanned
			confidence = 0.95
		} else {
			pdfType = TypeImageBase
			confidence = 0.8
		}
	case pagesWithText > 0 && (pagesWithImages > 0 || pagesWithVectorText > 0):
		ocrRecommended = true
		pdfType = TypeMixed
		confidence = 0.7
	case totalTextOps == 0:
		ocrRecommended = true
		pdfType = TypeScanned
		confidence = 0.9
	default:
		pdfType = TypeTextBased
		confidence = max(textRatio, 0.5)
	}

	if pdfType == TypeTextBased && pagesSampled >= 3 {
		newspaperPages := 0
		for _, analysis := range analysisCache {
			ratio := 1.0
			if analysis.textOperatorCount > 0 {
				ratio = float64(analysis.fontChangeCount) / float64(analysis.textOperatorCount)
			}
			if analysis.textOperatorCount >= 1500 && analysis.fontChangeCount >= 50 && ratio < 0.15 {
				newspaperPages++
			}
		}
		if float64(newspaperPages)/float64(pagesSampled) >= 0.5 {
			ocrRecommended = true
		}
	}

	var pagesNeedingOCR []int
	switch pdfType {
	case TypeTextBased:
		pagesNeedingOCR = []int{}
	case TypeScanned, TypeImageBase:
		for i := 1; i <= totalPages; i++ {
			pagesNeedingOCR = append(pagesNeedingOCR, i)
		}
	case TypeMixed:
		for pageNum := 1; pageNum <= totalPages; pageNum++ {
			analysis, cached := analysisCache[pageNum]
			if !cached {
				analysis = analyzePageContent(doc, pages[pageNum-1])
				analysisCache[pageNum] = analysis
			}
			sparseTextOverScan := analysis.hasTemplateImage &&
				analysis.textOperatorCount < max(cfg.MinTextOpsPerPage, 10)
			if (analysis.hasTemplateImage && analysis.looksLikeScan()) ||
				analysis.hasVectorText ||
				sparseTextOverScan ||
				(analysis.textOperatorCount < cfg.MinTextOpsPerPage && analysis.hasImages) {
				pagesNeedingOCR = append(pagesNeedingOCR, pageNum)
			}
		}
	}

	for pageNum, analysis := range analysisCache {
		if (analysis.hasIdentityHNoToUnicode || analysis.hasOnlyType3Fonts) && !slices.Contains(pagesNeedingOCR, pageNum) {
			pagesNeedingOCR = append(pagesNeedingOCR, pageNum)
		}
	}
	if len(pagesNeedingOCR) < totalPages {
		for pageNum := 1; pageNum <= totalPages; pageNum++ {
			if _, cached := analysisCache[pageNum]; cached || slices.Contains(pagesNeedingOCR, pageNum) {
				continue
			}
			analysis := analyzePageContent(doc, pages[pageNum-1])
			if analysis.hasIdentityHNoToUnicode || analysis.hasOnlyType3Fonts {
				pagesNeedingOCR = append(pagesNeedingOCR, pageNum)
				analysisCache[pageNum] = analysis
			}
		}
	}
	slices.Sort(pagesNeedingOCR)
	pagesNeedingOCR = slices.Compact(pagesNeedingOCR)

	reasons := []PageReasons{}
	for _, pageNum := range pagesNeedingOCR {
		var rs []string
		if analysis, cached := analysisCache[pageNum]; cached {
			rs = pageOCRReasons(analysis)
		} else {
			rs = []string{ReasonScanned}
		}
		reasons = append(reasons, PageReasons{Page: pageNum, Reasons: rs})
	}

	return &Result{
		Type:             pdfType,
		PageCount:        totalPages,
		PagesSampled:     pagesSampled,
		PagesWithText:    pagesWithText,
		Confidence:       confidence,
		Title:            documentTitle(doc),
		OCRRecommended:   ocrRecommended,
		PagesNeedingOCR:  pagesNeedingOCR,
		OCRReasonsByPage: reasons,
	}
}

func pageOCRReasons(a pageAnalysis) []string {
	var reasons []string
	if a.hasIdentityHNoToUnicode || a.hasOnlyType3Fonts {
		reasons = append(reasons, ReasonSuspectedGarble)
	}
	if a.hasVectorText {
		reasons = append(reasons, ReasonVectorText)
	}
	if len(reasons) == 0 {
		hasExtractableText := a.textOperatorCount > 0 && a.uniqueTextChars > 0
		if !hasExtractableText && !a.hasImages && !a.hasTemplateImage {
			reasons = append(reasons, ReasonNoText)
		} else {
			reasons = append(reasons, ReasonScanned)
		}
	}
	return reasons
}

func distributePages(n, total int) []int {
	if n <= 0 {
		return nil
	}
	if n >= total {
		out := make([]int, 0, total)
		for i := 1; i <= total; i++ {
			out = append(out, i)
		}
		return out
	}
	indices := []int{1}
	if n > 1 {
		indices = append(indices, total)
	}
	remaining := n - 2
	if remaining < 0 {
		remaining = 0
	}
	if remaining > 0 && total > 2 {
		step := (total - 2) / (remaining + 1)
		for i := 1; i <= remaining; i++ {
			idx := 1 + step*i
			if idx > 1 && idx < total && !slices.Contains(indices, idx) {
				indices = append(indices, idx)
			}
		}
	}
	slices.Sort(indices)
	return slices.Compact(indices)
}
