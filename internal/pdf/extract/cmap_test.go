package extract

import "testing"

func TestParseToUnicodeCMap(t *testing.T) {
	tests := []struct {
		name string
		cmap string
		raw  []byte
		want string
	}{
		{
			// Some producers write bfrange destinations as arrays, one
			// entry per code.
			name: "bfrange array destination",
			cmap: "/CIDSystemInfo << /Registry (Adobe) /Ordering (UCS) /Supplement 0 >> def\n" +
				"1 begincodespacerange\n<0000> <FFFF>\nendcodespacerange\n" +
				"2 beginbfrange\n<0000> <0000> <0000>\n<0001> <0003> [<0049> <004E> <0054> ]\nendbfrange\n",
			raw:  []byte{0, 1, 0, 2, 0, 3},
			want: "INT",
		},
		{
			name: "bfrange before bfchar",
			cmap: "1 beginbfrange\n<0010> <0011> <0041>\nendbfrange\n" +
				"1 beginbfchar\n<0012> <0043>\nendbfchar\n",
			raw:  []byte{0, 0x10, 0, 0x11, 0, 0x12},
			want: "ABC",
		},
		{
			name: "bfrange increments last char of multi-char destination",
			cmap: "1 beginbfrange\n<0001> <0002> <00660066>\nendbfrange\n",
			raw:  []byte{0, 1, 0, 2},
			want: "fffg",
		},
		{
			name: "surrogate pair destination",
			cmap: "1 beginbfchar\n<0005> <D835DC00>\nendbfchar\n",
			raw:  []byte{0, 5},
			want: "\U0001D400",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseToUnicodeCMap([]byte(tt.cmap)).decode(tt.raw); got != tt.want {
				t.Errorf("decode = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCMapTokensTerminates(t *testing.T) {
	for _, in := range []string{">>", "<< >> >", "a>b", "<<<", "[<00>]>>", "\x00>"} {
		if toks := cmapTokens(in); len(toks) > len(in) {
			t.Errorf("cmapTokens(%q) = %d tokens, want at most %d", in, len(toks), len(in))
		}
	}
}
