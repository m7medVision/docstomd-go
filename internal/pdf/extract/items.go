// Package extract turns PDF content streams into positioned text items.
package extract

type charWidth struct {
	ch rune
	w  uint16
}

type codeChar struct {
	code byte
	ch   rune
}

type ItemType string

const (
	ItemText  ItemType = "text"
	ItemImage ItemType = "image"
	ItemLink  ItemType = "link"
)

// TextItem is a positioned extraction item in the visible-page-box frame:
// points, y-up, baseline at y for horizontal text. JSON field names and
// order are the stable --items-json contract pinned by the goldens.
type TextItem struct {
	Text          string   `json:"text"`
	Page          int      `json:"page"`
	X             float64  `json:"x"`
	Y             float64  `json:"y"`
	Width         float64  `json:"width"`
	Height        float64  `json:"height"`
	Rotation      float64  `json:"rotation"`
	AdvanceKnown  bool     `json:"advance_known"`
	Font          string   `json:"font"`
	FontTag       string   `json:"font_tag"`
	FontSize      float64  `json:"font_size"`
	IsBold        bool     `json:"is_bold"`
	IsItalic      bool     `json:"is_italic"`
	IsUnderline   bool     `json:"is_underline"`
	IsStrikeout   bool     `json:"is_strikeout"`
	BaselineShift float64  `json:"baseline_shift"`
	ItemType      ItemType `json:"item_type"`
	MCID          *int64   `json:"mcid"`
	URL           string   `json:"url,omitempty"`
}

type Rect struct {
	X, Y, Width, Height float64
	Page                int
}

type Line struct {
	X1, Y1, X2, Y2 float64
	Page           int
}

type PageResult struct {
	Items             []TextItem
	Rects             []Rect
	Lines             []Line
	HasEncodingIssues bool
}
