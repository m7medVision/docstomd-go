package table

import (
	"testing"

	"github.com/m7medVision/docstomd-go/internal/pdf/extract"
)

// wideFirstColRegion mirrors the financial-statement grid shape: a wide
// first column for row labels, narrow value columns after it.
func wideFirstColRegion() *Region {
	return &Region{
		X0: 0, X1: 600, Y0: 0, Y1: 100,
		ColX: []float64{0, 300, 350, 400, 450, 500, 550, 600},
		RowY: []float64{100, 90},
	}
}

func wideLabelBand(labelX float64) []extract.TextItem {
	band := []extract.TextItem{
		{ItemType: extract.ItemText, Text: "Total long term deposits and balances", X: labelX, Y: 85, Width: 210, Height: 10, FontSize: 10},
	}
	for _, x := range []float64{310, 360, 410} {
		band = append(band, extract.TextItem{ItemType: extract.ItemText, Text: "100", X: x, Y: 85, Width: 20, Height: 10, FontSize: 10})
	}
	return band
}

func TestExtendDataRowsKeepsWideLeftLabel(t *testing.T) {
	region := wideFirstColRegion()
	items := wideLabelBand(5)
	extendDataRows(region, items)
	if len(region.RowY) != 4 {
		t.Fatalf("RowY = %v, want the band added (4 boundaries)", region.RowY)
	}
	fillCells(region, items, map[*extract.TextItem]bool{})
	if region.Cells[1][0] != "Total long term deposits and balances" {
		t.Fatalf("label cell = %q, want the label in column 0", region.Cells[1][0])
	}
	if region.Cells[1][1] != "100" {
		t.Fatalf("value cell = %q, want 100", region.Cells[1][1])
	}
}

func TestExtendDataRowsRejectsWideInnerItem(t *testing.T) {
	region := wideFirstColRegion()
	items := wideLabelBand(305)
	extendDataRows(region, items)
	if len(region.RowY) != 2 {
		t.Fatalf("RowY = %v, want the band rejected (2 boundaries)", region.RowY)
	}
}
