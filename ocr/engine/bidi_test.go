package engine

import "testing"

func TestVisualToLogical(t *testing.T) {
	cases := []struct{ visual, logical string }{
		// Latin and Chinese lines are untouched.
		{"Total cost: 1,250 USD", "Total cost: 1,250 USD"},
		{"季度报告", "季度报告"},
		// Arabic only: reversed.
		{"ثلاثلا عبرلا ريرقت", "تقرير الربع الثالث"},
		// Numbers keep their digit order.
		{"2024 ماع يف ارانيد 1250 تاعيبملا تغلب", "بلغت المبيعات 1250 دينارا في عام 2024"},
		// A Latin word inside Arabic stays readable.
		{"دحألا موي SAP ماظن ثيدحت مت", "تم تحديث نظام SAP يوم الأحد"},
		// Punctuation between Arabic and a number, and a percentage.
		{"7781 :بلطلا مقر", "رقم الطلب: 7781"},
		{"50% ةبسنب", "بنسبة 50%"},
		// Brackets are mirrored back.
		{"(ةدحولا) ةيمك", "كمية (الوحدة)"},
	}
	for _, tc := range cases {
		if got := visualToLogical(tc.visual); got != tc.logical {
			t.Errorf("visualToLogical(%q) = %q, want %q", tc.visual, got, tc.logical)
		}
	}
}

// TestVisualToLogicalRoundTrip: for right-to-left lines the reordering is
// its own inverse.
func TestVisualToLogicalRoundTrip(t *testing.T) {
	for _, logical := range []string{"بلغت المبيعات 1250 دينارا في عام 2024", "تم تحديث نظام SAP يوم الأحد", "رقم الطلب: 7781"} {
		if got := visualToLogical(visualToLogical(logical)); got != logical {
			t.Errorf("round trip of %q = %q", logical, got)
		}
	}
}
