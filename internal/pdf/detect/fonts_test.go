package detect

import "testing"

func TestCmapSubtableHasMappings(t *testing.T) {
	tests := []struct {
		name string
		sub  []byte
		want bool
	}{
		{
			// segCountX2 claims 200 segments but the arrays are missing.
			name: "format 4 shorter than its segment arrays",
			sub:  []byte{0, 4, 0, 14, 0, 0, 0, 200, 0, 0, 0, 0, 0, 0},
			want: false,
		},
		{
			name: "format 4 with one mapped segment",
			sub: []byte{
				0, 4, 0, 24, 0, 0, 0, 2, 0, 0, 0, 0, 0, 0, // header, segCountX2 = 2
				0, 0x41, // endCode
				0, 0, // reservedPad
				0, 0x41, // startCode
				0, 0, // idDelta
				0, 0, // idRangeOffset
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cmapSubtableHasMappings(tt.sub); got != tt.want {
				t.Errorf("cmapSubtableHasMappings = %v, want %v", got, tt.want)
			}
		})
	}
}
