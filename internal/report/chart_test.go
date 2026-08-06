package report

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jedib0t/go-pretty/v6/text"
)

// forceChartColor makes colour output deterministic regardless of the ambient
// environment, so the suite passes under NO_COLOR=1 (which many CI images set)
// without an `env -u NO_COLOR` incantation. Two layers need it: chartColorAllowed
// reads NO_COLOR at call time, while go-pretty latches its own switch at package
// init, so clearing the variable alone is too late.
func forceChartColor(t *testing.T) {
	t.Helper()
	t.Setenv("NO_COLOR", "")
	t.Setenv("FORCE_COLOR", "1")
}

// go-pretty computes its global colour switch from NO_COLOR at package init,
// before any t.Setenv can run, so clearing the variable inside a test is too
// late. Enabling here makes the suite independent of the ambient environment.
// Tests that assert the *absence* of colour are unaffected: they go through
// chartColorAllowed, which reads NO_COLOR at call time.
func init() { text.EnableColors() }

// bucketsTotal mirrors what render used to compute internally, before the
// headline total had to survive bucket truncation.
func bucketsTotal(buckets []bucket) float64 {
	var total float64
	for _, b := range buckets {
		total += b.total
	}
	return total
}

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

func TestRender_StructuralBasics(t *testing.T) {
	forceChartColor(t)
	buckets := []bucket{
		{
			key:      "2026-04-25",
			total:    100,
			segments: []segment{{model: "alpha", color: 0, cost: 60}, {model: "beta", color: 1, cost: 40}},
		},
		{
			key:      "2026-04-26",
			total:    50,
			segments: []segment{{model: "alpha", color: 0, cost: 50}},
		},
	}
	var buf bytes.Buffer
	err := render(&buf, buckets, chartOpts{height: 8, width: 60, keyName: "day", useTokens: false, costIncomplete: false, grandTotal: bucketsTotal(buckets)})
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "Cost") {
		t.Errorf("missing title with 'Cost':\n%s", out)
	}
	if !strings.Contains(out, "$150") {
		t.Errorf("missing total $150 in title:\n%s", out)
	}
	if !strings.Contains(out, "$0") {
		t.Errorf("missing $0 y-axis label:\n%s", out)
	}
	if !strings.Contains(out, "│") {
		t.Errorf("missing │ divider:\n%s", out)
	}
	if !strings.Contains(out, "25") || !strings.Contains(out, "26") {
		t.Errorf("missing day labels in:\n%s", out)
	}
	if !strings.Contains(out, "█") {
		t.Errorf("missing block character █:\n%s", out)
	}
	if !strings.Contains(out, "\x1b[") {
		t.Errorf("no ANSI escape — colors not applied:\n%q", out)
	}
	if !strings.Contains(out, "alpha") {
		t.Errorf("missing legend entry 'alpha':\n%s", out)
	}
}

func TestRender_TokenMode(t *testing.T) {
	buckets := []bucket{
		{
			key:      "2026-04-25",
			total:    1_500_000,
			segments: []segment{{model: "alpha", color: 0, cost: 1_500_000}},
		},
	}
	var buf bytes.Buffer
	err := render(&buf, buckets, chartOpts{height: 8, width: 60, keyName: "day", useTokens: true, costIncomplete: false, grandTotal: bucketsTotal(buckets)})
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Tokens") {
		t.Errorf("token mode should mention 'Tokens' in title:\n%s", out)
	}
	if strings.Contains(out, "$") {
		t.Errorf("token mode should not have $ in y-axis:\n%s", out)
	}
}

func TestRender_OtherSegmentInLegend(t *testing.T) {
	buckets := []bucket{
		{
			key:      "2026-04-25",
			total:    100,
			segments: []segment{{model: "alpha", color: 0, cost: 70}, {model: "other", color: -1, cost: 30}},
		},
	}
	var buf bytes.Buffer
	if err := render(&buf, buckets, chartOpts{height: 8, width: 60, keyName: "day", useTokens: false, costIncomplete: false, grandTotal: bucketsTotal(buckets)}); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "other") {
		t.Errorf("legend should mention 'other':\n%s", out)
	}
}

func TestFormatChart_FallsBackToTableWhenNoTTY(t *testing.T) {
	rows := sampleRows()
	var buf bytes.Buffer
	err := FormatChart(&buf, rows, "day", 16, 6)
	if err != nil {
		t.Fatalf("FormatChart returned error: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "█") {
		t.Errorf("expected table fallback (no █), got chart:\n%s", out)
	}
	if !strings.Contains(out, "claude-opus-4-6") {
		t.Errorf("table fallback missing model name:\n%s", out)
	}
}

func TestFormatChart_ForceColorRenders(t *testing.T) {
	forceChartColor(t)
	rows := sampleRows()
	var buf bytes.Buffer
	if err := FormatChart(&buf, rows, "day", 8, 6); err != nil {
		t.Fatalf("FormatChart returned error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "█") {
		t.Errorf("FORCE_COLOR=1 should render chart (containing █):\n%s", out)
	}
	if !strings.Contains(out, "\x1b[") {
		t.Errorf("expected ANSI escape in chart output")
	}
}

func TestFormatChart_NoColorEnvFallsBack(t *testing.T) {
	forceChartColor(t)
	t.Setenv("NO_COLOR", "1")
	rows := sampleRows()
	var buf bytes.Buffer
	if err := FormatChart(&buf, rows, "day", 8, 6); err != nil {
		t.Fatalf("FormatChart returned error: %v", err)
	}
	if strings.Contains(buf.String(), "█") {
		t.Errorf("NO_COLOR set should fall back to table; got chart")
	}
}

func TestFormatChart_HeightValidation(t *testing.T) {
	forceChartColor(t)
	rows := sampleRows()
	var buf bytes.Buffer
	err := FormatChart(&buf, rows, "day", 5, 6)
	if err == nil {
		t.Error("expected error for height < 6")
	}
	if err != nil && !strings.Contains(err.Error(), "height") {
		t.Errorf("error message should mention height: %v", err)
	}
}

func TestFormatChart_TopValidation(t *testing.T) {
	forceChartColor(t)
	rows := sampleRows()
	var buf bytes.Buffer
	err := FormatChart(&buf, rows, "day", 8, 0)
	if err == nil {
		t.Error("expected error for topN < 1")
	}
	if err != nil && !strings.Contains(err.Error(), "top") {
		t.Errorf("error message should mention top: %v", err)
	}
}

// TestValidateChartOptions_BoundsTopToPaletteSize pins the upper bound. Without
// it, assignColors hands out an index render uses to subscript chartPalette, so
// --top 7 panics as soon as a model at index 6 draws a row.
func TestValidateChartOptions_BoundsTopToPaletteSize(t *testing.T) {
	for _, tc := range []struct {
		name     string
		topN     int
		wantErr  string
		wantPass bool
	}{
		{name: "below range", topN: 0, wantErr: "--top must be at least 1"},
		{name: "at the floor", topN: 1, wantPass: true},
		{name: "at the palette size", topN: len(chartPalette), wantPass: true},
		{name: "one past the palette", topN: len(chartPalette) + 1, wantErr: "--top must be at most 6"},
		{name: "well past the palette", topN: 10, wantErr: "--top must be at most 6"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateChartOptions(8, tc.topN)
			if tc.wantPass {
				if err != nil {
					t.Fatalf("ValidateChartOptions(8, %d) = %v, want nil", tc.topN, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ValidateChartOptions(8, %d) = %v, want %q", tc.topN, err, tc.wantErr)
			}
		})
	}
}

// TestValidateChartOptions_BoundsHeightBothWays pins the upper bound too:
// yAxisLabels allocates one entry per row, so a large height hangs and a huge
// one panics inside makeslice with a raw Go stack trace.
func TestValidateChartOptions_BoundsHeightBothWays(t *testing.T) {
	for _, tc := range []struct {
		name     string
		height   int
		wantErr  string
		wantPass bool
	}{
		{name: "below range", height: 5, wantErr: "--height must be at least 6"},
		{name: "at the floor", height: 6, wantPass: true},
		{name: "at the ceiling", height: maxChartHeight, wantPass: true},
		{name: "one past the ceiling", height: maxChartHeight + 1, wantErr: "--height must be at most 1000"},
		{name: "absurd", height: 100000, wantErr: "--height must be at most 1000"},
		{name: "max int", height: math.MaxInt, wantErr: "--height must be at most 1000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateChartOptions(tc.height, 6)
			if tc.wantPass {
				if err != nil {
					t.Fatalf("ValidateChartOptions(%d, 6) = %v, want nil", tc.height, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ValidateChartOptions(%d, 6) = %v, want %q", tc.height, err, tc.wantErr)
			}
		})
	}
}

// TestFormatChart_DoesNotPanicAtPaletteBoundary is the end-to-end half: with
// more models than the palette holds, every allowed --top must render.
func TestFormatChart_DoesNotPanicAtPaletteBoundary(t *testing.T) {
	forceChartColor(t)
	var rows []Row
	for i := range len(chartPalette) + 4 {
		rows = append(rows, Row{
			Key:     "2026-04-25",
			Model:   fmt.Sprintf("model-%02d", i),
			Cost:    float64(100 - i),
			HasCost: true,
		})
	}
	for topN := 1; topN <= len(chartPalette); topN++ {
		var buf bytes.Buffer
		if err := FormatChart(&buf, rows, "day", 16, topN); err != nil {
			t.Fatalf("--top %d: %v", topN, err)
		}
		if buf.Len() == 0 {
			t.Errorf("--top %d rendered nothing", topN)
		}
	}
	var buf bytes.Buffer
	if err := FormatChart(&buf, rows, "day", 16, len(chartPalette)+1); err == nil {
		t.Errorf("--top %d should be rejected rather than panic", len(chartPalette)+1)
	}
}

// TestRender_LegendOmitsUndrawnOtherStack is what actually exercises the drawn
// filter. Once a sub-threshold fold borrows a row, the filter's only remaining
// job is a bucket whose height rounds to zero: there is no row to borrow, so the
// merged "other" reaches the segment list without reaching the grid, and an
// unfiltered legend would advertise a grey swatch that appears nowhere.
func TestRender_LegendOmitsUndrawnOtherStack(t *testing.T) {
	buckets := []bucket{
		{key: "d1", total: 1000, segments: []segment{{model: "big", color: 0, cost: 1000}}},
		{key: "d2", total: 1, segments: []segment{{model: "tiny", color: 1, cost: 1}}},
	}
	var buf bytes.Buffer
	captureChartStderr(t, func() {
		if err := render(&buf, buckets, chartOpts{
			height: 8, width: 60, keyName: "day", grandTotal: bucketsTotal(buckets),
		}); err != nil {
			t.Fatal(err)
		}
	})
	out := buf.String()
	if !strings.Contains(out, "big") {
		t.Errorf("legend should list the model that renders:\n%s", out)
	}
	if strings.Contains(out, "other") {
		t.Errorf("legend must not show 'other' when no bucket draws one:\n%s", out)
	}
	if strings.Contains(out, "tiny") {
		t.Errorf("legend must not name a model folded away:\n%s", out)
	}
}

// TestFormatChart_ReportsWhenNothingCanBePlotted covers the token sibling of the
// zero-cost case: FormatChart announces a token fallback and then, with every
// row at zero tokens, would return having written nothing at all.
func TestFormatChart_ReportsWhenNothingCanBePlotted(t *testing.T) {
	forceChartColor(t)
	rows := []Row{
		{Key: "2026-04-25", Model: "idle", InputTokens: 0, OutputTokens: 0, HasCost: false},
		{Key: "2026-04-26", Model: "idle", InputTokens: 0, OutputTokens: 0, HasCost: false},
	}
	var buf bytes.Buffer
	stderr := captureChartStderr(t, func() {
		if err := FormatChart(&buf, rows, "day", 8, 6); err != nil {
			t.Fatalf("FormatChart: %v", err)
		}
	})
	if !strings.Contains(stderr, "nothing to plot") {
		t.Errorf("stderr = %q, want an explanation rather than a silent empty chart", stderr)
	}
}

// TestBuildChartGrid_AccountsForEveryDollar is the invariant guard against this
// codebase's most persistent failure: a quantity computed at one stage and
// displayed at another, drifting apart. Every dollar in a bucket set is either
// drawn or counted as undrawn — never neither. Five rounds of inverted fixes all
// landed in that gap, so it is asserted directly rather than per-symptom.
func TestBuildChartGrid_AccountsForEveryDollar(t *testing.T) {
	// A deterministic spread of shapes: lopsided bars, near-equal bars, slivers
	// well under the half-row floor, and heights from the minimum to the cap.
	costs := [][]float64{
		{100},
		{70, 20, 5.5, 3, 3},
		{60, 22, 9, 4.5, 4.5},
		{1000, 1},
		{99.95, 0.05},
		{50, 50},
		{33.3, 33.3, 33.4},
		{500, 250, 125, 62.5, 31.25, 15.6, 7.8, 3.9},
		{0.01, 0.01, 0.01},
		{1e6, 1, 1, 1},
	}
	for _, height := range []int{6, 8, 10, 16, 37, maxChartHeight} {
		for ci, cs := range costs {
			for _, nBuckets := range []int{1, 3, 7} {
				name := fmt.Sprintf("h=%d/costs=%d/buckets=%d", height, ci, nBuckets)
				t.Run(name, func(t *testing.T) {
					var buckets []bucket
					for b := range nBuckets {
						// Scale each bucket differently so some round to zero rows.
						scale := 1.0 / float64(b*b+1)
						var segs []segment
						var total float64
						for i, c := range cs {
							segs = append(segs, segment{
								model: fmt.Sprintf("m%d", i),
								color: i % len(chartPalette),
								cost:  c * scale,
							})
							total += c * scale
						}
						buckets = append(buckets, bucket{key: fmt.Sprintf("b%d", b), segments: segs, total: total})
					}
					var maxTotal float64
					for _, b := range buckets {
						if b.total > maxTotal {
							maxTotal = b.total
						}
					}

					g := buildChartGrid(buckets, height, maxTotal)
					want := bucketsTotal(buckets)
					got := g.drawnCost + g.undrawn
					if math.Abs(got-want) > want*1e-9+1e-9 {
						t.Errorf("drawn %v + undrawn %v = %v, want the bucket total %v",
							g.drawnCost, g.undrawn, got, want)
					}
					// A model reported as merged must genuinely draw nowhere,
					// otherwise the note names something the reader can see.
					for model := range g.merged {
						for _, segs := range g.rendered {
							for _, s := range segs {
								if s.model == model && g.drawn[segmentKey(s)] {
									t.Errorf("model %q reported as merged but is drawn", model)
								}
							}
						}
					}
					// No dollar may be announced twice. Merged money is drawn
					// inside "other"; if it exceeded drawnCost some of it would
					// also be in undrawn, which reportUndrawn already reported —
					// two figures for the same money, with contradictory causes.
					var mergedTotal float64
					for _, cost := range g.merged {
						mergedTotal += cost
					}
					if mergedTotal > g.drawnCost+1e-9 {
						t.Errorf("merged %v exceeds drawn %v: money reported as both merged and undrawn",
							mergedTotal, g.drawnCost)
					}
				})
			}
		}
	}
}

// TestFormatChart_TruncationNamesTheDroppedAmount covers the other half of the
// identity: what truncation removes before render ever sees it. The count alone
// left the reader to infer the gap, and printed above a much smaller
// quantization figure it read as the lesser of the two.
func TestFormatChart_TruncationNamesTheDroppedAmount(t *testing.T) {
	forceChartColor(t)
	// 40 buckets with costs 40..1 (total $820); 35 fit, so 5 are dropped. Which
	// five differs by grouping, and the dropped amount says which: time keys keep
	// the most recent, so they discard the 5 oldest — here the 5 most expensive
	// ($190). Everything else keeps the largest, discarding the 5 cheapest ($15).
	// Asserting the amount pins the truncation direction and the new figure at once.
	for _, tc := range []struct {
		name        string
		keyName     string
		wantHint    string
		wantDropped string
	}{
		{name: "day keeps the newest", keyName: "day", wantHint: "--since/--last", wantDropped: "$190.00"},
		{name: "project keeps the largest", keyName: "project", wantHint: "--project", wantDropped: "$15.00"},
		{name: "session keeps the largest", keyName: "session", wantHint: "--project/--model", wantDropped: "$15.00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var rows []Row
			for i := range 40 {
				key := fmt.Sprintf("proj-%03d", i)
				if tc.keyName == "day" {
					key = fmt.Sprintf("2026-%02d-%02d", 1+i/28, 1+i%28)
				}
				rows = append(rows, Row{
					Key: key, Model: "alpha",
					// Descending cost, so for non-time keys proj-000 is largest.
					Cost: float64(40 - i), HasCost: true,
				})
			}
			var buf bytes.Buffer
			stderr := captureChartStderr(t, func() {
				if err := FormatChart(&buf, rows, tc.keyName, 8, 6); err != nil {
					t.Fatal(err)
				}
			})

			want := fmt.Sprintf("showing 35 of 40 buckets (%s not plotted); narrow with %s",
				tc.wantDropped, tc.wantHint)
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr = %q,\nwant %q", stderr, want)
			}
			// The headline still reports everything, so the note is the only
			// place the gap is stated.
			if title, _, _ := strings.Cut(buf.String(), "\n"); !strings.Contains(title, "Total: $820.00") {
				t.Errorf("title = %q, want the full pre-truncation total", title)
			}
		})
	}
}

// TestRender_DisclosesUndrawnSpend covers the two ways money inside the
// announced total reaches no pixel: a whole bucket rounding to zero rows, and a
// segment in a bar with no row to lend it.
func TestRender_DisclosesUndrawnSpend(t *testing.T) {
	for _, tc := range []struct {
		name       string
		buckets    []bucket
		want       string
		notWantSub string
		silent     bool
		useTokens  bool
		skipRemedy bool
	}{
		{
			name: "a bucket too short to draw",
			buckets: []bucket{
				{key: "d1", total: 1000, segments: []segment{{model: "big", color: 0, cost: 1000}}},
				{key: "d2", total: 12, segments: []segment{{model: "small", color: 1, cost: 12}}},
			},
			want: "$12.00 is too small to plot, including 1 empty bucket",
		},
		{
			name: "several blank buckets are counted together",
			buckets: []bucket{
				{key: "d1", total: 1000, segments: []segment{{model: "big", color: 0, cost: 1000}}},
				{key: "d2", total: 10, segments: []segment{{model: "small", color: 1, cost: 10}}},
				{key: "d3", total: 5, segments: []segment{{model: "small", color: 1, cost: 5}}},
			},
			want: "$15.00 is too small to plot, including 2 empty buckets",
		},
		{
			name: "everything draws",
			buckets: []bucket{
				{key: "d1", total: 100, segments: []segment{{model: "big", color: 0, cost: 100}}},
				{key: "d2", total: 80, segments: []segment{{model: "big", color: 0, cost: 80}}},
			},
			silent: true,
		},
		{
			// Under half a cent the amount is display noise, and "$0.00 is too
			// small to plot" reads as a bug. The empty column is still real, so
			// it is reported on its own terms instead.
			name: "an amount below display precision reports columns instead",
			buckets: []bucket{
				{key: "d1", total: 1000, segments: []segment{{model: "big", color: 0, cost: 1000}}},
				{key: "d2", total: 0.001, segments: []segment{{model: "dust", color: 1, cost: 0.001}}},
			},
			want:       "1 bucket has nothing large enough to plot",
			notWantSub: "$0.00",
		},
		{
			// A day of nothing but unpriced usage totals exactly zero in cost
			// mode. It must still be disclosed — otherwise it passes as a
			// zero-spend day — but no height can draw $0, so no remedy is offered.
			name: "an all-unpriced column is disclosed without a remedy",
			buckets: []bucket{
				{key: "d1", total: 100, segments: []segment{{model: "priced", color: 0, cost: 100}}},
				{key: "d2", total: 0, segments: []segment{{model: "unpriced", color: 1, cost: 0}}},
			},
			want:       "1 bucket has nothing large enough to plot",
			notWantSub: "raise --height",
			skipRemedy: true,
		},
		{
			// The bare arm: an unrescuable fold with no blank bucket. This is the
			// phrasing a user sees whenever the fold's borrow cannot fire.
			name: "a fold with no blank bucket reports the amount alone",
			buckets: []bucket{
				{key: "d1", total: 1000, segments: []segment{{model: "big", color: 0, cost: 1000}}},
				{key: "d2", total: 250.30, segments: []segment{
					{model: "a", color: 1, cost: 125},
					{model: "b", color: 2, cost: 125},
					{model: "dust", color: 3, cost: 0.30},
				}},
			},
			want:       "$0.30 is too small to plot",
			notWantSub: "empty bucket",
		},
		{
			// A negative published rate yields a negative bucket height, which
			// drew nothing and was previously counted nowhere: no bar, no blank
			// column, and empty stderr.
			name: "a negative bucket is disclosed rather than silent",
			buckets: []bucket{
				{key: "d1", total: 100, segments: []segment{{model: "a", color: 0, cost: 100}}},
				{key: "d2", total: -30, segments: []segment{{model: "b", color: 1, cost: -30}}},
			},
			want: "-$30.00 is too small to plot, including 1 empty bucket",
			// No height draws a negative bar, so no remedy is offered.
			skipRemedy: true,
		},
		{
			// Token mode counts tokens, so the amount must not be dollar-formatted.
			name: "token mode reports token units",
			buckets: []bucket{
				{key: "d1", total: 2_000_000, segments: []segment{{model: "big", color: 0, cost: 2_000_000}}},
				{key: "d2", total: 12_000, segments: []segment{{model: "small", color: 1, cost: 12_000}}},
			},
			useTokens: true,
			want:      "12,000 is too small to plot, including 1 empty bucket",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			stderr := captureChartStderr(t, func() {
				if err := render(&buf, tc.buckets, chartOpts{
					height: 8, width: 60, keyName: "day", useTokens: tc.useTokens,
					grandTotal: bucketsTotal(tc.buckets),
				}); err != nil {
					t.Fatal(err)
				}
			})
			if tc.useTokens && strings.Contains(stderr, "$") {
				t.Errorf("stderr = %q, want token units rather than a dollar amount", stderr)
			}
			if tc.silent {
				if stderr != "" {
					t.Errorf("stderr = %q, want silence when every bucket draws", stderr)
				}
				return
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, tc.want)
			}
			if tc.notWantSub != "" && strings.Contains(stderr, tc.notWantSub) {
				t.Errorf("stderr = %q, want it to omit %q", stderr, tc.notWantSub)
			}
			if !tc.skipRemedy && !strings.Contains(stderr, "raise --height") {
				t.Errorf("stderr = %q, want the remedy named", stderr)
			}
		})
	}
}

// TestFormatChart_TruncationOmitsAZeroAmount applies round 9's negligible-amount
// rule to the truncation line, which was added alongside the other two and did
// not inherit it. Buckets were still dropped, so the count and hint stay.
func TestFormatChart_TruncationOmitsAZeroAmount(t *testing.T) {
	forceChartColor(t)
	var rows []Row
	for i := range 40 {
		// The five smallest are priced at exactly zero, and for a non-time key
		// truncation drops the smallest — so the dropped total is $0.00.
		cost := float64(40 - i)
		if i >= 35 {
			cost = 0
		}
		rows = append(rows, Row{
			Key: fmt.Sprintf("proj-%03d", i), Model: "alpha", Cost: cost, HasCost: true,
		})
	}
	var buf bytes.Buffer
	stderr := captureChartStderr(t, func() {
		if err := FormatChart(&buf, rows, "project", 8, 6); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(stderr, "showing 35 of 40 buckets; narrow with --project") {
		t.Errorf("stderr = %q, want the count and hint without an amount", stderr)
	}
	if strings.Contains(stderr, "$0.00") {
		t.Errorf("stderr = %q, want no $0.00 figure", stderr)
	}
}

// TestFormatChart_ReportsWhenTruncationLeavesNothingDrawable covers the case the
// pre-truncation guard misses: for time keys truncation keeps the *newest*
// buckets, so the tallest can be among those dropped, leaving only zero-total
// survivors. render then returns silently after FormatChart promised output.
func TestFormatChart_ReportsWhenTruncationLeavesNothingDrawable(t *testing.T) {
	forceChartColor(t)
	var rows []Row
	// 20 older priced days, then 40 newer days whose only model is unpriced.
	for i := range 20 {
		rows = append(rows, Row{
			Key: fmt.Sprintf("2026-01-%02d", i+1), Model: "priced",
			Cost: 10, HasCost: true,
		})
	}
	for i := range 40 {
		rows = append(rows, Row{
			Key: fmt.Sprintf("2026-03-%02d", i+1), Model: "unpriced",
			InputTokens: 100, HasCost: false,
		})
	}

	var buf bytes.Buffer
	stderr := captureChartStderr(t, func() {
		if err := FormatChart(&buf, rows, "day", 8, 6); err != nil {
			t.Fatal(err)
		}
	})
	if buf.Len() > 0 {
		t.Fatalf("expected no chart, got %d bytes:\n%s", buf.Len(), buf.String())
	}
	if !strings.Contains(stderr, "nothing to plot") {
		t.Errorf("stderr = %q, want an explanation rather than 0 bytes and exit 0", stderr)
	}
}

// TestTruncateToWidth_CountsDisplayColumns pins the CJK rewrite. Reverting
// truncateToWidth to rune arithmetic previously left the suite green, so the
// largest behavioural change of its round was one revert from undone.
func TestTruncateToWidth_CountsDisplayColumns(t *testing.T) {
	for name, tc := range map[string]struct {
		s    string
		max  int
		want string
	}{
		// Two columns per glyph: 5 columns holds two of them plus the ellipsis.
		"cjk truncates by column":  {"项目一二三", 5, "项目…"},
		"cjk that already fits":    {"项目", 4, "项目"},
		"cjk one column too wide":  {"项目一", 5, "项目…"},
		"ascii is unchanged":       {"abcdefgh", 5, "abcd…"},
		"ascii that already fits":  {"abcd", 4, "abcd"},
		"fullwidth latin":          {"ＡＢＣ", 4, "Ａ…"},
		"no room for an ellipsis":  {"项目一", 0, ""},
		"never cuts mid-codepoint": {"项目一", 3, "项…"},
	} {
		t.Run(name, func(t *testing.T) {
			got := truncateToWidth(tc.s, tc.max)
			if got != tc.want {
				t.Errorf("truncateToWidth(%q, %d) = %q, want %q", tc.s, tc.max, got, tc.want)
			}
			if w := text.StringWidth(got); w > tc.max {
				t.Errorf("result %q is %d columns wide, want at most %d", got, w, tc.max)
			}
		})
	}
}

// TestRender_XAxisAlignsWideLabels pins the other half of the CJK rewrite: the
// padding either side of a label. Measured in runes, a two-column glyph pushes
// the whole row right of the bars it labels.
func TestRender_XAxisAlignsWideLabels(t *testing.T) {
	buckets := []bucket{
		{key: "项目一", total: 100, segments: []segment{{model: "a", color: 0, cost: 100}}},
		{key: "项目二", total: 80, segments: []segment{{model: "a", color: 0, cost: 80}}},
	}
	var buf bytes.Buffer
	if err := render(&buf, buckets, chartOpts{
		height: 8, width: 60, keyName: "project", grandTotal: bucketsTotal(buckets),
	}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(buf.String(), "\n")
	var floor, xaxis string
	for i, l := range lines {
		if strings.Contains(l, "└") {
			floor = l
			if i+1 < len(lines) {
				xaxis = lines[i+1]
			}
			break
		}
	}
	if floor == "" || xaxis == "" {
		t.Fatalf("could not locate the axis rows:\n%s", buf.String())
	}
	// The label row must occupy the same number of display columns as the floor,
	// which is what keeps each label under its own bar.
	if got, want := text.StringWidth(strings.TrimRight(xaxis, " ")), text.StringWidth(strings.TrimRight(floor, " ")); got > want {
		t.Errorf("x-axis row is %d columns, floor is %d — wide labels overflow:\n%s", got, want, buf.String())
	}
}

// TestRender_NegativeSegmentDoesNotOverdrawItsBucket: splitSegments clamps its
// leftover but not its total, so with a negative cost the heights can sum past
// the bar's own height — drawing a bar taller than the quantity it represents
// while the disclosure simultaneously calls that money undrawn.
func TestRender_NegativeSegmentDoesNotOverdrawItsBucket(t *testing.T) {
	buckets := []bucket{
		{key: "d1", total: 1000, segments: []segment{{model: "big", color: 0, cost: 1000}}},
		{key: "d2", total: 100, segments: []segment{
			{model: "pos", color: 1, cost: 200},
			{model: "neg", color: 2, cost: -100},
		}},
	}
	var buf bytes.Buffer
	captureChartStderr(t, func() {
		if err := render(&buf, buckets, chartOpts{
			height: 8, width: 60, keyName: "day", grandTotal: bucketsTotal(buckets),
		}); err != nil {
			t.Fatal(err)
		}
	})
	// d2 is a tenth of d1, so at height 8 it may draw at most one row. Count the
	// plot rows in which the second bar has ink.
	var d2Rows int
	for _, line := range strings.Split(buf.String(), "\n") {
		if !strings.Contains(line, "│") {
			continue
		}
		_, bars, _ := strings.Cut(line, "│")
		// Two bars per row; the second is whatever follows the first gap.
		if fields := strings.Fields(bars); len(fields) >= 2 && strings.Contains(fields[1], chartBarRune) {
			d2Rows++
		}
	}
	if d2Rows > 1 {
		t.Errorf("second bar drew %d rows, want at most its bucketHeight of 1:\n%s", d2Rows, buf.String())
	}
}

// TestNegligibleGuards_TreatSmallNegativesAsZero: all three disclosure guards
// compared formatted strings, and "-$0.00" never equals "$0.00", so the exact
// spelling this code calls a defect escaped through every one of them.
func TestNegligibleGuards_TreatSmallNegativesAsZero(t *testing.T) {
	t.Run("undrawn", func(t *testing.T) {
		buckets := []bucket{
			{key: "d1", total: 1000, segments: []segment{{model: "big", color: 0, cost: 1000}}},
			{key: "d2", total: -0.001, segments: []segment{{model: "dust", color: 1, cost: -0.001}}},
		}
		var buf bytes.Buffer
		stderr := captureChartStderr(t, func() {
			if err := render(&buf, buckets, chartOpts{
				height: 8, width: 60, keyName: "day", grandTotal: bucketsTotal(buckets),
			}); err != nil {
				t.Fatal(err)
			}
		})
		if strings.Contains(stderr, "$0.00") {
			t.Errorf("stderr = %q, want no zero-looking figure", stderr)
		}
		if !strings.Contains(stderr, "nothing large enough to plot") {
			t.Errorf("stderr = %q, want the column-count wording", stderr)
		}
	})

	t.Run("merged models", func(t *testing.T) {
		stderr := captureChartStderr(t, func() {
			reportMergedModels(map[string]float64{"m": -0.001}, chartOpts{height: 8})
		})
		if stderr != "" {
			t.Errorf("stderr = %q, want silence for a sub-precision negative", stderr)
		}
	})

	t.Run("truncation", func(t *testing.T) {
		forceChartColor(t)
		orig := chartWidthOf
		chartWidthOf = func(io.Writer) int { return 30 }
		t.Cleanup(func() { chartWidthOf = orig })
		var rows []Row
		for i := range 20 {
			// Day keys keep the NEWEST buckets, so the sub-cent negatives have to
			// sit at the start to end up in the dropped set.
			cost := float64(i)
			if i < 10 {
				cost = -0.0001
			}
			rows = append(rows, Row{Key: fmt.Sprintf("2026-04-%02d", i+1), Model: "a", Cost: cost, HasCost: true})
		}
		var buf bytes.Buffer
		stderr := captureChartStderr(t, func() {
			if err := FormatChart(&buf, rows, "day", 8, 6); err != nil {
				t.Fatal(err)
			}
		})
		if strings.Contains(stderr, "$0.00 not plotted") {
			t.Errorf("stderr = %q, want no zero-looking dropped amount", stderr)
		}
	})
}

func TestFormatCost_NegativeSignOutsideTheSymbol(t *testing.T) {
	// The spelling this cycle rejected in formatChartAmount: "$-5" reads as a
	// malformed figure, and two formatters disagreeing is how the next negative
	// path gets it wrong.
	for v, want := range map[float64]string{
		-5:    "-$5",
		-1500: "-$1.5k",
		-0.25: "-$0.25",
		5:     "$5",
	} {
		if got := formatCost(v); got != want {
			t.Errorf("formatCost(%g) = %q, want %q", v, got, want)
		}
	}
}

func TestFormatCost_LargeMagnitudeTiers(t *testing.T) {
	for _, tc := range []struct {
		v    float64
		want string
	}{
		{999, "$999"},
		{1000, "$1.0k"},
		{999_999, "$1000.0k"},
		{1e6, "$1.0M"},
		{1e9, "$1.0B"},
		// Without the tiers this read "$1000000000.0k" and widened the gutter to
		// 14 columns.
		{1e12, "$1000.0B"},
	} {
		if got := formatCost(tc.v); got != tc.want {
			t.Errorf("formatCost(%g) = %q, want %q", tc.v, got, tc.want)
		}
	}
}

// TestReportMergedModels_SkipsANegligibleTotal drives the guard directly. It is
// unreachable through render — buildChartGrid's `s.cost > 0` filter keeps
// zero-cost models out of the map entirely — so a fixture-driven test cannot
// exercise it, which is how it was previously recorded as covered when it was not.
func TestReportMergedModels_SkipsANegligibleTotal(t *testing.T) {
	for name, tc := range map[string]struct {
		merged map[string]float64
		want   string
	}{
		"below display precision": {map[string]float64{"m": 0.001}, ""},
		"exactly zero":            {map[string]float64{"m": 0}, ""},
		"empty":                   {map[string]float64{}, ""},
		"real money":              {map[string]float64{"m": 5}, `1 model merged into "other" ($5.00)`},
	} {
		t.Run(name, func(t *testing.T) {
			stderr := captureChartStderr(t, func() {
				reportMergedModels(tc.merged, chartOpts{height: 8})
			})
			if tc.want == "" {
				if stderr != "" {
					t.Errorf("stderr = %q, want silence rather than a $0.00 note", stderr)
				}
				return
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr = %q, want %q", stderr, tc.want)
			}
		})
	}
}

// TestFormatChart_NarrowTerminalBranches covers the two width-driven paths that
// were unreachable while width came straight from the OS: every test writes to a
// bytes.Buffer, for which chartTerminalWidth always answers 80.
func TestFormatChart_NarrowTerminalBranches(t *testing.T) {
	forceChartColor(t)
	rows := []Row{
		{Key: "2026-04-25", Model: "alpha", Cost: 10, HasCost: true},
		{Key: "2026-04-26", Model: "alpha", Cost: 20, HasCost: true},
	}
	withWidth := func(t *testing.T, w int) (string, string) {
		t.Helper()
		orig := chartWidthOf
		chartWidthOf = func(io.Writer) int { return w }
		t.Cleanup(func() { chartWidthOf = orig })
		var buf bytes.Buffer
		stderr := captureChartStderr(t, func() {
			if err := FormatChart(&buf, rows, "day", 8, 6); err != nil {
				t.Fatal(err)
			}
		})
		return buf.String(), stderr
	}

	t.Run("too narrow falls back to a table", func(t *testing.T) {
		out, stderr := withWidth(t, 29)
		if strings.Contains(out, chartBarRune) {
			t.Errorf("want a table, got a chart:\n%s", out)
		}
		if !strings.Contains(stderr, "terminal too narrow for chart") {
			t.Errorf("stderr = %q, want the fallback explained", stderr)
		}
	})

	t.Run("just wide enough still charts", func(t *testing.T) {
		out, _ := withWidth(t, 30)
		if !strings.Contains(out, chartBarRune) {
			t.Errorf("want a chart at the minimum width:\n%s", out)
		}
	})

	// How many buckets fit is a function of width, which every other truncation
	// test leaves implicit at 80 — the "35 of 40" in those assertions is a magic
	// number until something states where it comes from.
	t.Run("width determines how many buckets fit", func(t *testing.T) {
		// 40 buckets, so both widths truncate — at 20 the width-80 case fit
		// entirely and the subtest asserted nothing at all.
		const total = 40
		var many []Row
		for i := range total {
			many = append(many, Row{
				Key: fmt.Sprintf("2026-%02d-%02d", 1+i/28, 1+i%28), Model: "alpha",
				Cost: float64(total - i), HasCost: true,
			})
		}
		// Expectations derived by hand, not recomputed from the implementation's
		// own formula: that is what makes them an independent check rather than
		// a restatement.
		for _, tc := range []struct{ width, wantShown int }{
			{30, 10},
			{80, 35},
		} {
			t.Run(fmt.Sprintf("width=%d", tc.width), func(t *testing.T) {
				orig := chartWidthOf
				chartWidthOf = func(io.Writer) int { return tc.width }
				t.Cleanup(func() { chartWidthOf = orig })
				var buf bytes.Buffer
				stderr := captureChartStderr(t, func() {
					if err := FormatChart(&buf, many, "day", 8, 6); err != nil {
						t.Fatal(err)
					}
				})
				want := fmt.Sprintf("showing %d of %d buckets", tc.wantShown, total)
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr = %q, want %q", stderr, want)
				}
			})
		}
	})
}

// TestFormatChart_HeadlineMatchesTableTotal pins the equality all three
// disclosure mechanisms exist to protect, and which was previously verified only
// by inspection.
func TestFormatChart_HeadlineMatchesTableTotal(t *testing.T) {
	forceChartColor(t)
	rows := []Row{
		{Key: "2026-04-25", Model: "alpha", InputTokens: 100, Cost: 1234.5678, HasCost: true},
		{Key: "2026-04-26", Model: "beta", InputTokens: 200, Cost: 0.0021, HasCost: true},
		{Key: "2026-04-26", Model: "unpriced", InputTokens: 300, HasCost: false},
	}
	var chartBuf, tableBuf bytes.Buffer
	captureChartStderr(t, func() {
		if err := FormatChart(&chartBuf, rows, "day", 8, 6); err != nil {
			t.Fatal(err)
		}
	})
	FormatTable(&tableBuf, rows, "day")

	title, _, _ := strings.Cut(chartBuf.String(), "\n")
	// The table renders the same figure with thousands separators and the same
	// unpriced marker.
	if !strings.Contains(title, "Total: $1234.57*") {
		t.Errorf("chart title = %q, want the exact total with the unpriced marker", title)
	}
	if !strings.Contains(tableBuf.String(), "$1234.57*") {
		t.Errorf("table footer does not carry $1234.57*:\n%s", tableBuf.String())
	}
}

// TestRender_DisclosesMergedModelNames covers the attribution gap, which is a
// different loss from the undrawn one: this money *is* plotted, inside the grey
// "other" stack, but the model's name appears nowhere. reportUndrawn cannot
// cover it, because undrawn is exactly zero in this case.
func TestRender_DisclosesMergedModelNames(t *testing.T) {
	render8 := func(t *testing.T, segs []segment) (string, string) {
		t.Helper()
		var total float64
		for _, s := range segs {
			total += s.cost
		}
		buckets := []bucket{{key: "2026-04-25", total: total, segments: segs}}
		var buf bytes.Buffer
		stderr := captureChartStderr(t, func() {
			if err := render(&buf, buckets, chartOpts{
				height: 8, width: 60, keyName: "day", grandTotal: total,
			}); err != nil {
				t.Fatal(err)
			}
		})
		return buf.String(), stderr
	}

	t.Run("a model that never draws is named as merged", func(t *testing.T) {
		out, stderr := render8(t, []segment{
			{model: "visible-model", color: 0, cost: 95},
			{model: "sliver-model", color: 1, cost: 5},
		})
		want := `1 model merged into "other" ($5.00); raise --height to name them`
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
		// The note exists precisely because the legend cannot say this.
		if strings.Contains(out, "sliver-model") {
			t.Errorf("legend should not name the merged model:\n%s", out)
		}
	})

	t.Run("silent when every model draws", func(t *testing.T) {
		_, stderr := render8(t, []segment{
			{model: "a", color: 0, cost: 60},
			{model: "b", color: 1, cost: 40},
		})
		if strings.Contains(stderr, "merged into") {
			t.Errorf("stderr = %q, want no merge note when every model draws", stderr)
		}
	})

	// The money in a bucket that draws no bar at all belongs to reportUndrawn.
	// Claiming it here too announces the same dollars twice with contradictory
	// explanations, and sends the reader after a grey stack that is not on screen.
	t.Run("silent when the other stack itself never draws", func(t *testing.T) {
		buckets := []bucket{
			{key: "d1", total: 330, segments: []segment{{model: "big", color: 0, cost: 330}}},
			{key: "d2", total: 0.52, segments: []segment{{model: "minor", color: 1, cost: 0.52}}},
		}
		var buf bytes.Buffer
		stderr := captureChartStderr(t, func() {
			if err := render(&buf, buckets, chartOpts{
				height: 8, width: 60, keyName: "day", grandTotal: bucketsTotal(buckets),
			}); err != nil {
				t.Fatal(err)
			}
		})
		if !strings.Contains(stderr, "$0.52 is too small to plot") {
			t.Errorf("stderr = %q, want the undrawn note to own this money", stderr)
		}
		if strings.Contains(stderr, "merged into") {
			t.Errorf("stderr = %q, want no merge note for money that reaches no pixel", stderr)
		}
		if strings.Contains(buf.String(), "other") {
			t.Errorf("no grey stack is drawn, so nothing may point at one:\n%s", buf.String())
		}
	})

	// An unpriced model contributes exactly 0 in cost mode, so it has no money
	// inside "other" to go looking for. Counting it inflated the count and sent
	// the reader after models the "no pricing" line had already explained.
	t.Run("unpriced zero-cost models are not counted as merged", func(t *testing.T) {
		_, stderr := render8(t, []segment{
			{model: "dominant", color: 0, cost: 95},
			{model: "real-sliver", color: 1, cost: 5},
			{model: "unpriced-a", color: 2, cost: 0},
			{model: "unpriced-b", color: 3, cost: 0},
		})
		want := `1 model merged into "other" ($5.00)`
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want %q — only the model carrying money", stderr, want)
		}
		for _, m := range []string{"unpriced-a", "unpriced-b"} {
			if strings.Contains(stderr, m) {
				t.Errorf("stderr = %q, want it to omit the zero-cost model %q", stderr, m)
			}
		}
	})

	// TestReportMergedModels' zero-total early return: with every merged model at
	// zero the note must not appear at all, rather than reading `0 models … $0.00`.
	t.Run("silent when every merged model is zero-cost", func(t *testing.T) {
		_, stderr := render8(t, []segment{
			{model: "dominant", color: 0, cost: 100},
			{model: "unpriced", color: 1, cost: 0},
		})
		if strings.Contains(stderr, "merged into") {
			t.Errorf("stderr = %q, want no merge note when no merged model carries money", stderr)
		}
	})

	// The remedy clause needs its own fixture at the ceiling: a bucket where the
	// fold's borrow fires, so "other" genuinely draws and the note fires for the
	// right reason.
	t.Run("no remedy at the height ceiling", func(t *testing.T) {
		buckets := []bucket{{
			key:   "d1",
			total: 1_000_001,
			segments: []segment{
				{model: "dominant", color: 0, cost: 1_000_000},
				{model: "sliver", color: 1, cost: 1},
			},
		}}
		var buf bytes.Buffer
		stderr := captureChartStderr(t, func() {
			if err := render(&buf, buckets, chartOpts{
				height: maxChartHeight, width: 60, keyName: "day", grandTotal: bucketsTotal(buckets),
			}); err != nil {
				t.Fatal(err)
			}
		})
		if !strings.Contains(stderr, `1 model merged into "other"`) {
			t.Fatalf("stderr = %q, want the merge note to fire at the ceiling", stderr)
		}
		if strings.Contains(stderr, "raise --height") {
			t.Errorf("stderr = %q, want no remedy that cannot be taken", stderr)
		}
	})
}

// TestReportUndrawn_DropsRemedyAtTheHeightCeiling: --height is capped, so at the
// cap "raise --height" names a remedy that no longer exists.
func TestReportUndrawn_DropsRemedyAtTheHeightCeiling(t *testing.T) {
	// The second bucket is a millionth of the first, so it rounds to zero rows
	// at any height the chart permits.
	buckets := []bucket{
		{key: "d1", total: 1_000_000, segments: []segment{{model: "big", color: 0, cost: 1_000_000}}},
		{key: "d2", total: 1, segments: []segment{{model: "small", color: 0, cost: 1}}},
	}
	render := func(t *testing.T, height int) string {
		t.Helper()
		var buf bytes.Buffer
		return captureChartStderr(t, func() {
			if err := render(&buf, buckets, chartOpts{
				height: height, width: 60, keyName: "day", grandTotal: bucketsTotal(buckets),
			}); err != nil {
				t.Fatal(err)
			}
		})
	}

	below := render(t, 16)
	if !strings.Contains(below, "raise --height") {
		t.Errorf("stderr at height 16 = %q, want the remedy while it is still reachable", below)
	}
	atCap := render(t, maxChartHeight)
	if !strings.Contains(atCap, "is too small to plot") {
		t.Errorf("stderr at the cap = %q, want the disclosure to remain", atCap)
	}
	if strings.Contains(atCap, "raise --height") {
		t.Errorf("stderr at the cap = %q, want no remedy that cannot be taken", atCap)
	}
}

// TestRender_TotalCountsTruncatedBuckets keeps the headline figure equal to the
// table's. render sees only the buckets that fit, so a total derived from them
// understates the report — by 42% on --by session in the reported case.
func TestRender_TotalCountsTruncatedBuckets(t *testing.T) {
	shown := []bucket{
		{key: "s1", total: 30, segments: []segment{{model: "alpha", color: 0, cost: 30}}},
		{key: "s2", total: 20, segments: []segment{{model: "alpha", color: 0, cost: 20}}},
	}
	var buf bytes.Buffer
	// $850 more sits in buckets that did not fit; the two shown hold $50.
	if err := render(&buf, shown, chartOpts{height: 8, width: 60, keyName: "session", grandTotal: 900}); err != nil {
		t.Fatal(err)
	}
	title, _, _ := strings.Cut(buf.String(), "\n")
	if !strings.Contains(title, "Total: $900.00") {
		t.Errorf("title = %q, want the pre-truncation total $900.00, not the $50 that fit", title)
	}
}

// TestFormatChart_ChartsTokensWhenEveryCostIsZero covers priced-at-zero rows:
// the pricing cache carries zero-rate entries, and a cost chart of them renders
// nothing at all, which reads as failure rather than as $0.
func TestFormatChart_ChartsTokensWhenEveryCostIsZero(t *testing.T) {
	forceChartColor(t)
	rows := []Row{
		{Key: "2026-04-25", Model: "free-model", InputTokens: 1000, OutputTokens: 500, Cost: 0, HasCost: true},
		{Key: "2026-04-26", Model: "free-model", InputTokens: 800, OutputTokens: 200, Cost: 0, HasCost: true},
	}
	var buf bytes.Buffer
	stderr := captureChartStderr(t, func() {
		if err := FormatChart(&buf, rows, "day", 8, 6); err != nil {
			t.Fatalf("FormatChart: %v", err)
		}
	})
	out := buf.String()
	if out == "" {
		t.Fatal("rendered nothing for priced-but-zero-cost rows")
	}
	if !strings.Contains(out, "Tokens") {
		t.Errorf("expected token-mode title, got:\n%s", out)
	}
	if !strings.Contains(stderr, "every priced model costs zero") {
		t.Errorf("stderr = %q, want the zero-cost fallback note", stderr)
	}
}

func TestFormatChart_TokenFallbackWhenNoCost(t *testing.T) {
	forceChartColor(t)
	rows := []Row{
		{Key: "2026-04-25", Model: "alpha", InputTokens: 1000, OutputTokens: 500, HasCost: false},
		{Key: "2026-04-26", Model: "alpha", InputTokens: 800, OutputTokens: 200, HasCost: false},
	}
	var buf bytes.Buffer
	if err := FormatChart(&buf, rows, "day", 8, 6); err != nil {
		t.Fatalf("FormatChart returned error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Tokens") {
		t.Errorf("expected token-mode title (with 'Tokens'); got:\n%s", out)
	}
}

// TestFormatChart_SignalsPartialPricing pins the unpriced-model contract that
// table carries as "*" and JSON/CSV as has_cost. The chart's total silently
// omits unpriced models, so the mixed case has to say so.
func TestFormatChart_SignalsPartialPricing(t *testing.T) {
	forceChartColor(t)
	priced := Row{Key: "2026-04-25", Model: "alpha", InputTokens: 1000, OutputTokens: 500, Cost: 12, HasCost: true}
	unpriced := Row{Key: "2026-04-25", Model: "codex-auto-review", InputTokens: 900, OutputTokens: 100, HasCost: false}

	const partialNote = "some models have no pricing; chart total excludes them"
	const noneNote = "no pricing data for any model"

	for _, tc := range []struct {
		name       string
		rows       []Row
		want       string
		notWant    string
		wantMarker bool
	}{
		{name: "mixed", rows: []Row{priced, unpriced}, want: partialNote, notWant: noneNote, wantMarker: true},
		{name: "all priced", rows: []Row{priced}, notWant: partialNote},
		// Token mode charts a complete token total, so it carries no marker.
		{name: "none priced", rows: []Row{unpriced}, want: noneNote, notWant: partialNote},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			stderr := captureChartStderr(t, func() {
				if err := FormatChart(&buf, tc.rows, "day", 8, 6); err != nil {
					t.Fatalf("FormatChart: %v", err)
				}
			})
			if tc.want != "" && !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, tc.want)
			}
			if tc.notWant != "" && strings.Contains(stderr, tc.notWant) {
				t.Errorf("stderr = %q, want it to omit %q", stderr, tc.notWant)
			}
			title, _, _ := strings.Cut(buf.String(), "\n")
			if got := strings.Contains(title, "*"); got != tc.wantMarker {
				t.Errorf("title = %q, has marker = %v, want %v", title, got, tc.wantMarker)
			}
		})
	}
}

// TestRender_MarksIncompleteCostTotal pins the in-band marker so the chart's
// total agrees with the table's `$2811.88*` rather than only warning on stderr.
func TestRender_MarksIncompleteCostTotal(t *testing.T) {
	buckets := []bucket{{
		key:      "2026-04-25",
		total:    12,
		segments: []segment{{model: "alpha", color: 0, cost: 12}},
	}}
	titleOf := func(useTokens, costIncomplete bool) string {
		t.Helper()
		var buf bytes.Buffer
		if err := render(&buf, buckets, chartOpts{height: 8, width: 60, keyName: "day", useTokens: useTokens, costIncomplete: costIncomplete, grandTotal: bucketsTotal(buckets)}); err != nil {
			t.Fatal(err)
		}
		title, _, _ := strings.Cut(buf.String(), "\n")
		return title
	}

	if got := titleOf(false, true); !strings.Contains(got, "Total: $12.00*") {
		t.Errorf("title = %q, want an incomplete-total marker", got)
	}
	if got := titleOf(false, false); strings.Contains(got, "*") {
		t.Errorf("title = %q, want no marker when every row is priced", got)
	}
	if got := titleOf(true, true); strings.Contains(got, "*") {
		t.Errorf("title = %q, want no marker in token mode", got)
	}
}

// TestRender_FoldsSubThresholdSegmentsIntoOther pins the two halves together:
// spend too small to draw its own bar must still reach the chart as part of
// "other", and the legend must name only what is actually drawn. Filtering the
// legend alone would trade a cluttered legend for silently omitted spend.
func TestRender_FoldsSubThresholdSegmentsIntoOther(t *testing.T) {
	render8 := func(t *testing.T, segs []segment) string {
		t.Helper()
		var total float64
		for _, s := range segs {
			total += s.cost
		}
		var buf bytes.Buffer
		if err := render(&buf, []bucket{{key: "2026-04-25", total: total, segments: segs}}, chartOpts{height: 8, width: 60, keyName: "day", grandTotal: total}); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}

	t.Run("folded spend becomes a visible other stack", func(t *testing.T) {
		// Individually 5% each, below the half-row threshold at height 8;
		// merged they are 10% and draw one row.
		out := render8(t, []segment{
			{model: "visible-model", color: 0, cost: 90},
			{model: "sliver-a", color: 1, cost: 5},
			{model: "sliver-b", color: 2, cost: 5},
		})
		if !strings.Contains(out, "visible-model") {
			t.Errorf("legend should list the model that renders:\n%s", out)
		}
		if !strings.Contains(out, "other") {
			t.Errorf("sub-threshold spend should surface as 'other':\n%s", out)
		}
		for _, hidden := range []string{"sliver-a", "sliver-b"} {
			if strings.Contains(out, hidden) {
				t.Errorf("legend must not name %q, which draws no bar of its own:\n%s", hidden, out)
			}
		}
	})

	// Previously this asserted the opposite — that a fold too small to win a row
	// stays off the legend. That left the spend absent from both plot and legend,
	// which is the failure folding exists to prevent, so the merged "other" now
	// borrows a row instead. Quantization overstates a sliver; omission hides it.
	t.Run("a fold too small to win a row borrows one", func(t *testing.T) {
		out := render8(t, []segment{
			{model: "visible-model", color: 0, cost: 99.95},
			{model: "sliver-model", color: 1, cost: 0.05},
		})
		if !strings.Contains(out, "visible-model") {
			t.Errorf("legend should list the model that renders:\n%s", out)
		}
		if strings.Contains(out, "sliver-model") {
			t.Errorf("legend must not name a model folded into other:\n%s", out)
		}
		if !strings.Contains(out, "other") {
			t.Errorf("folded spend must reach the chart even when sub-threshold:\n%s", out)
		}
		// Borrowed from the tallest, not conjured: the bar is still 8 rows.
		var plotted int
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, chartBarRune) {
				plotted++
			}
		}
		if plotted != 8 {
			t.Errorf("plotted rows = %d, want the bar height unchanged at 8:\n%s", plotted, out)
		}
	})

	// Folding raises the "other" floor, which shrinks the leftover pool small
	// segments win rows from — so one pass can zero out a segment that drew
	// before the fold, dropping its cost from the bar's composition entirely.
	t.Run("converges when folding zeroes a previously drawn segment", func(t *testing.T) {
		// Tuned against splitSegments' largest-remainder rule at height 10:
		// "small" (ideal 0.55) wins a leftover row before the fold, then loses it
		// to the merged "other" (ideal 0.60) afterwards. A single pass leaves it
		// at zero rows and unfolded — drawn nowhere, counted nowhere.
		segs := []segment{
			{model: "big", color: 0, cost: 70},
			{model: "mid", color: 1, cost: 18.5},
			{model: "small", color: 2, cost: 5.5},
			{model: "tiny-a", color: 3, cost: 3},
			{model: "tiny-b", color: 4, cost: 3},
		}
		kept, segH := foldSubThresholdSegments(segs, 10)

		drawnCost, foldedCost := 0.0, 0.0
		for i, s := range kept {
			if segH[i] > 0 {
				drawnCost += s.cost
			} else {
				foldedCost += s.cost
			}
		}
		// Every dollar must end up either in a drawn segment or in one the caller
		// will fold again — never silently absent from both.
		if total := drawnCost + foldedCost; math.Abs(total-100) > 1e-9 {
			t.Errorf("accounted cost = %v, want the full 100", total)
		}
		// No segment may survive with zero rows unless it is "other": that is the
		// non-converged state, where a colour is neither drawn nor merged.
		for i, s := range kept {
			if segH[i] == 0 && s.color != -1 {
				t.Errorf("segment %q kept at zero rows; fold did not converge (heights %v)", s.model, segH)
			}
		}
	})

	t.Run("existing other segment absorbs the fold", func(t *testing.T) {
		out := render8(t, []segment{
			{model: "visible-model", color: 0, cost: 90},
			{model: "other", color: -1, cost: 5},
			{model: "sliver-model", color: 1, cost: 5},
		})
		if strings.Count(out, "other") != 1 {
			t.Errorf("want exactly one 'other' legend entry, got:\n%s", out)
		}
		if strings.Contains(out, "sliver-model") {
			t.Errorf("legend must not name the folded model:\n%s", out)
		}
	})
}

func captureChartStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	// Restored on return, not via t.Cleanup: leaving a closed pipe as os.Stderr
	// for the rest of the test would silently swallow any later diagnostic.
	defer func() { os.Stderr = old }()

	// Drain concurrently: reading only after fn() returns would deadlock rather
	// than fail once fn() writes past the ~64 KiB pipe buffer.
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out := <-done
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestFormatChart_EmptyRows(t *testing.T) {
	forceChartColor(t)
	var buf bytes.Buffer
	err := FormatChart(&buf, nil, "day", 8, 6)
	if err != nil {
		t.Errorf("expected nil for empty rows; got %v", err)
	}
}

// TestRender_GutterAlignsWithWideLabels verifies that the y-axis │ divider
// appears at the same column on every row, even when some labels are wider
// than the default gutter (e.g., "$216.89" is 7 chars vs the default 6).
func TestRender_GutterAlignsWithWideLabels(t *testing.T) {
	buckets := []bucket{
		{key: "2026-04-25", total: 216.89, segments: []segment{{model: "alpha", color: 0, cost: 216.89}}},
		{key: "2026-04-26", total: 100.00, segments: []segment{{model: "alpha", color: 0, cost: 100.00}}},
	}
	var buf bytes.Buffer
	if err := render(&buf, buckets, chartOpts{height: 16, width: 80, keyName: "day", useTokens: false, costIncomplete: false, grandTotal: bucketsTotal(buckets)}); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()

	ansi := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	var dividerCols []int
	for _, line := range strings.Split(out, "\n") {
		plain := ansi.ReplaceAllString(line, "")
		idx := strings.Index(plain, "│")
		if idx < 0 {
			continue
		}
		col := utf8.RuneCountInString(plain[:idx])
		dividerCols = append(dividerCols, col)
	}
	if len(dividerCols) < 2 {
		t.Fatalf("expected multiple │ rows; got %d. Output:\n%s", len(dividerCols), out)
	}
	for i, c := range dividerCols[1:] {
		if c != dividerCols[0] {
			t.Errorf("│ misaligned: row 0 at col %d, row %d at col %d.\nOutput:\n%s",
				dividerCols[0], i+1, c, out)
		}
	}
}

// TestRender_FloorAlignsWithBars verifies that the └ corner of the floor line
// sits exactly under the │ divider above (i.e., same column).
func TestRender_FloorAlignsWithBars(t *testing.T) {
	buckets := []bucket{
		{key: "2026-04-25", total: 216.89, segments: []segment{{model: "alpha", color: 0, cost: 216.89}}},
	}
	var buf bytes.Buffer
	if err := render(&buf, buckets, chartOpts{height: 16, width: 80, keyName: "day", useTokens: false, costIncomplete: false, grandTotal: bucketsTotal(buckets)}); err != nil {
		t.Fatalf("render: %v", err)
	}
	ansi := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	var dividerCol, cornerCol int = -1, -1
	for _, line := range strings.Split(buf.String(), "\n") {
		plain := ansi.ReplaceAllString(line, "")
		if dividerCol == -1 {
			if i := strings.Index(plain, "│"); i >= 0 {
				dividerCol = utf8.RuneCountInString(plain[:i])
			}
		}
		if i := strings.Index(plain, "└"); i >= 0 {
			cornerCol = utf8.RuneCountInString(plain[:i])
		}
	}
	if dividerCol == -1 || cornerCol == -1 {
		t.Fatalf("missing │ or └ in output:\n%s", buf.String())
	}
	if cornerCol != dividerCol {
		t.Errorf("│ at col %d but └ at col %d (should match)", dividerCol, cornerCol)
	}
}
