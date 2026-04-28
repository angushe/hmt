package report

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAssignColors_AllFit(t *testing.T) {
	rows := []Row{
		{Model: "model-a", Cost: 30, HasCost: true},
		{Model: "model-b", Cost: 20, HasCost: true},
		{Model: "model-c", Cost: 50, HasCost: true},
	}
	colors := assignColors(rows, 6, costMetric)
	if colors["model-c"] != 0 {
		t.Errorf("model-c (highest) = %d, want 0", colors["model-c"])
	}
	if colors["model-a"] != 1 {
		t.Errorf("model-a (mid) = %d, want 1", colors["model-a"])
	}
	if colors["model-b"] != 2 {
		t.Errorf("model-b (lowest) = %d, want 2", colors["model-b"])
	}
}

func TestAssignColors_OverflowToOther(t *testing.T) {
	rows := []Row{
		{Model: "m1", Cost: 80, HasCost: true},
		{Model: "m2", Cost: 70, HasCost: true},
		{Model: "m3", Cost: 60, HasCost: true},
		{Model: "m4", Cost: 50, HasCost: true},
		{Model: "m5", Cost: 40, HasCost: true},
		{Model: "m6", Cost: 30, HasCost: true},
		{Model: "m7", Cost: 20, HasCost: true},
		{Model: "m8", Cost: 10, HasCost: true},
	}
	colors := assignColors(rows, 6, costMetric)
	if colors["m1"] != 0 {
		t.Errorf("m1 = %d, want 0", colors["m1"])
	}
	if colors["m6"] != 5 {
		t.Errorf("m6 = %d, want 5", colors["m6"])
	}
	if colors["m7"] != -1 {
		t.Errorf("m7 (overflow) = %d, want -1", colors["m7"])
	}
	if colors["m8"] != -1 {
		t.Errorf("m8 (overflow) = %d, want -1", colors["m8"])
	}
}

func TestAssignColors_TiebreakAlphabetic(t *testing.T) {
	rows := []Row{
		{Model: "zebra", Cost: 10, HasCost: true},
		{Model: "apple", Cost: 10, HasCost: true},
		{Model: "mango", Cost: 10, HasCost: true},
	}
	colors := assignColors(rows, 6, costMetric)
	if colors["apple"] != 0 {
		t.Errorf("apple (first alphabetically among ties) = %d, want 0", colors["apple"])
	}
	if colors["mango"] != 1 {
		t.Errorf("mango = %d, want 1", colors["mango"])
	}
	if colors["zebra"] != 2 {
		t.Errorf("zebra = %d, want 2", colors["zebra"])
	}
}

func TestAssignColors_AggregatesAcrossRows(t *testing.T) {
	rows := []Row{
		{Key: "d1", Model: "alpha", Cost: 5, HasCost: true},
		{Key: "d2", Model: "alpha", Cost: 5, HasCost: true},
		{Key: "d1", Model: "beta", Cost: 7, HasCost: true},
	}
	colors := assignColors(rows, 6, costMetric)
	if colors["alpha"] != 0 {
		t.Errorf("alpha = %d, want 0", colors["alpha"])
	}
	if colors["beta"] != 1 {
		t.Errorf("beta = %d, want 1", colors["beta"])
	}
}

func TestBucketize_TimeKeyAscending(t *testing.T) {
	rows := []Row{
		{Key: "2026-04-26", Model: "alpha", Cost: 10, HasCost: true},
		{Key: "2026-04-24", Model: "alpha", Cost: 5, HasCost: true},
		{Key: "2026-04-25", Model: "alpha", Cost: 7, HasCost: true},
	}
	colors := map[string]int{"alpha": 0}
	buckets := bucketize(rows, colors, "day", costMetric)
	if len(buckets) != 3 {
		t.Fatalf("len = %d, want 3", len(buckets))
	}
	if buckets[0].key != "2026-04-24" {
		t.Errorf("buckets[0] = %q, want 2026-04-24", buckets[0].key)
	}
	if buckets[2].key != "2026-04-26" {
		t.Errorf("buckets[2] = %q, want 2026-04-26", buckets[2].key)
	}
}

func TestBucketize_NonTimeKeyByCostDesc(t *testing.T) {
	rows := []Row{
		{Key: "proj-a", Model: "alpha", Cost: 5, HasCost: true},
		{Key: "proj-b", Model: "alpha", Cost: 20, HasCost: true},
		{Key: "proj-c", Model: "alpha", Cost: 10, HasCost: true},
	}
	colors := map[string]int{"alpha": 0}
	buckets := bucketize(rows, colors, "project", costMetric)
	if buckets[0].key != "proj-b" {
		t.Errorf("buckets[0] = %q, want proj-b (highest)", buckets[0].key)
	}
	if buckets[2].key != "proj-a" {
		t.Errorf("buckets[2] = %q, want proj-a (lowest)", buckets[2].key)
	}
}

func TestBucketize_SegmentsSortedByCostDesc(t *testing.T) {
	rows := []Row{
		{Key: "d1", Model: "alpha", Cost: 5, HasCost: true},
		{Key: "d1", Model: "beta", Cost: 20, HasCost: true},
		{Key: "d1", Model: "gamma", Cost: 10, HasCost: true},
	}
	colors := map[string]int{"alpha": 0, "beta": 1, "gamma": 2}
	buckets := bucketize(rows, colors, "day", costMetric)
	if len(buckets) != 1 {
		t.Fatalf("buckets = %d, want 1", len(buckets))
	}
	segs := buckets[0].segments
	if len(segs) != 3 {
		t.Fatalf("segments = %d, want 3", len(segs))
	}
	if segs[0].model != "beta" {
		t.Errorf("segs[0] = %q, want beta (highest cost)", segs[0].model)
	}
	if segs[2].model != "alpha" {
		t.Errorf("segs[2] = %q, want alpha (lowest cost)", segs[2].model)
	}
}

func TestBucketize_OtherCollapsed(t *testing.T) {
	rows := []Row{
		{Key: "d1", Model: "alpha", Cost: 50, HasCost: true},
		{Key: "d1", Model: "small1", Cost: 3, HasCost: true},
		{Key: "d1", Model: "small2", Cost: 2, HasCost: true},
	}
	colors := map[string]int{"alpha": 0, "small1": -1, "small2": -1}
	buckets := bucketize(rows, colors, "day", costMetric)
	if len(buckets[0].segments) != 2 {
		t.Fatalf("segments = %d, want 2 (alpha + other)", len(buckets[0].segments))
	}
	var otherSeg *segment
	for i := range buckets[0].segments {
		if buckets[0].segments[i].color == -1 {
			otherSeg = &buckets[0].segments[i]
		}
	}
	if otherSeg == nil {
		t.Fatal("no other segment found")
	}
	if otherSeg.cost != 5 {
		t.Errorf("other.cost = %f, want 5", otherSeg.cost)
	}
	if otherSeg.model != "other" {
		t.Errorf("other.model = %q, want \"other\"", otherSeg.model)
	}
}

func TestBucketize_BucketTotalCorrect(t *testing.T) {
	rows := []Row{
		{Key: "d1", Model: "alpha", Cost: 7, HasCost: true},
		{Key: "d1", Model: "beta", Cost: 3, HasCost: true},
	}
	colors := map[string]int{"alpha": 0, "beta": 1}
	buckets := bucketize(rows, colors, "day", costMetric)
	if buckets[0].total != 10 {
		t.Errorf("total = %f, want 10", buckets[0].total)
	}
}

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

func TestYAxisLabels(t *testing.T) {
	tests := []struct {
		name        string
		maxCost     float64
		height      int
		mustContain []string
	}{
		{
			name:        "small cost",
			maxCost:     100,
			height:      8,
			mustContain: []string{"$0", "$100"},
		},
		{
			name:        "thousands shorthand",
			maxCost:     5000,
			height:      8,
			mustContain: []string{"$0", "$5.0k"},
		},
		{
			name:        "fractional",
			maxCost:     2.5,
			height:      8,
			mustContain: []string{"$0", "$2.50"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			labels := yAxisLabels(tt.maxCost, tt.height, false)
			if len(labels) != tt.height {
				t.Fatalf("len = %d, want %d", len(labels), tt.height)
			}
			joined := strings.Join(labels, "\n")
			for _, s := range tt.mustContain {
				if !strings.Contains(joined, s) {
					t.Errorf("missing %q in:\n%s", s, joined)
				}
			}
		})
	}
}

func TestYAxisLabels_Tokens(t *testing.T) {
	labels := yAxisLabels(2_000_000, 8, true)
	joined := strings.Join(labels, "\n")
	if !strings.Contains(joined, "2.0M") {
		t.Errorf("expected 2.0M token shorthand in:\n%s", joined)
	}
	if strings.Contains(joined, "$") {
		t.Errorf("token mode should not have $ prefix:\n%s", joined)
	}
}

func TestXAxisLabels_Day(t *testing.T) {
	bs := []bucket{
		{key: "2026-04-25"},
		{key: "2026-04-26"},
		{key: "2026-04-27"},
	}
	labels := xAxisLabels(bs, "day", 2)
	if len(labels) != 3 {
		t.Fatalf("len = %d, want 3", len(labels))
	}
	if labels[0] != "25" {
		t.Errorf("[0] = %q, want 25", labels[0])
	}
	if labels[2] != "27" {
		t.Errorf("[2] = %q, want 27", labels[2])
	}
}

func TestXAxisLabels_Week(t *testing.T) {
	bs := []bucket{{key: "2026-W14"}, {key: "2026-W15"}}
	labels := xAxisLabels(bs, "week", 3)
	if labels[0] != "W14" {
		t.Errorf("[0] = %q, want W14", labels[0])
	}
}

func TestXAxisLabels_Month(t *testing.T) {
	bs := []bucket{{key: "2026-04"}, {key: "2026-05"}}
	labels := xAxisLabels(bs, "month", 3)
	if labels[0] != "Apr" {
		t.Errorf("[0] = %q, want Apr", labels[0])
	}
	if labels[1] != "May" {
		t.Errorf("[1] = %q, want May", labels[1])
	}
}

func TestXAxisLabels_TruncateLongKey(t *testing.T) {
	bs := []bucket{{key: "very-long-project-name"}}
	labels := xAxisLabels(bs, "project", 4)
	// barW=4 → label fits in barW+1=5 runes
	if utf8.RuneCountInString(labels[0]) > 5 {
		t.Errorf("label %q exceeds 5 runes (barW=4)", labels[0])
	}
	if !strings.Contains(labels[0], "…") {
		t.Errorf("label %q should contain ellipsis when truncated", labels[0])
	}
}

func TestXAxisLabels_NarrowStride(t *testing.T) {
	var bs []bucket
	for d := 1; d <= 16; d++ {
		bs = append(bs, bucket{key: fmt.Sprintf("2026-04-%02d", d)})
	}
	labels := xAxisLabels(bs, "day", 1)
	visible := 0
	for _, l := range labels {
		if strings.TrimSpace(l) != "" {
			visible++
		}
	}
	if visible < 7 || visible > 10 {
		t.Errorf("visible labels = %d, want ~8", visible)
	}
}
