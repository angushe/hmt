package report

import (
	"testing"
)

func TestSplitSegments(t *testing.T) {
	tests := []struct {
		name      string
		costs     []float64
		totalRows int
		want      []int
	}{
		{
			name:      "empty input",
			costs:     []float64{},
			totalRows: 10,
			want:      []int{},
		},
		{
			name:      "zero rows",
			costs:     []float64{1, 2, 3},
			totalRows: 0,
			want:      []int{0, 0, 0},
		},
		{
			name:      "zero costs",
			costs:     []float64{0, 0, 0},
			totalRows: 10,
			want:      []int{0, 0, 0},
		},
		{
			name:      "equal split with leftover",
			costs:     []float64{1, 1, 1},
			totalRows: 10,
			want:      []int{4, 3, 3},
		},
		{
			name:      "lopsided exact",
			costs:     []float64{7, 2, 1},
			totalRows: 10,
			want:      []int{7, 2, 1},
		},
		{
			name:      "tiny segment dropped",
			costs:     []float64{0.1, 5, 5},
			totalRows: 10,
			want:      []int{0, 5, 5},
		},
		{
			name:      "two segments equal",
			costs:     []float64{1, 1},
			totalRows: 5,
			want:      []int{3, 2},
		},
		{
			name:      "single segment",
			costs:     []float64{42},
			totalRows: 8,
			want:      []int{8},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitSegments(tt.costs, tt.totalRows)
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d (got %v)", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("[%d] = %d, want %d (full got=%v)", i, got[i], tt.want[i], got)
				}
			}
			var sum int
			for _, v := range got {
				sum += v
			}
			var costSum float64
			for _, c := range tt.costs {
				costSum += c
			}
			if costSum > 0 && tt.totalRows > 0 && sum > tt.totalRows {
				t.Errorf("sum %d exceeds totalRows %d", sum, tt.totalRows)
			}
		})
	}
}
