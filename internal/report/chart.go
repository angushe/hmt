package report

import (
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/jedib0t/go-pretty/v6/text"
	"golang.org/x/term"
)

// metricFn extracts a numeric value from a Row. Used for both color ranking
// (assignColors) and segment sizing (bucketize). The two callers always use
// the same metric, so the result is internally consistent.
type metricFn func(Row) float64

func costMetric(r Row) float64 { return r.Cost }

func tokenMetric(r Row) float64 {
	return float64(r.InputTokens + r.OutputTokens + r.CacheWriteTokens + r.CacheReadTokens)
}

// assignColors ranks models by total metric (descending; alphabetical tiebreak)
// and assigns color indices 0..topN-1. Models beyond topN map to -1 (rendered
// as "other" in gray).
func assignColors(rows []Row, topN int, metric metricFn) map[string]int {
	totals := make(map[string]float64)
	for _, r := range rows {
		totals[r.Model] += metric(r)
	}

	type modelTotal struct {
		name string
		val  float64
	}
	list := make([]modelTotal, 0, len(totals))
	for m, v := range totals {
		list = append(list, modelTotal{name: m, val: v})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].val != list[j].val {
			return list[i].val > list[j].val
		}
		return list[i].name < list[j].name
	})

	result := make(map[string]int, len(list))
	for i, mt := range list {
		if i < topN {
			result[mt.name] = i
		} else {
			result[mt.name] = -1
		}
	}
	return result
}

// segment is one colored stack inside a bucket's bar.
type segment struct {
	model string
	color int // 0..N-1 = palette index; -1 = "other" (gray)
	cost  float64
}

// bucket is one bar in the chart, with its segments stacked bottom-up by cost desc.
type bucket struct {
	key      string
	segments []segment
	total    float64
}

// bucketize groups rows by Key, sorts segments within each bucket by metric desc,
// merges all "other"-mapped models into a single segment per bucket, and orders
// the resulting slice for x-axis display.
func bucketize(rows []Row, colors map[string]int, keyName string, metric metricFn) []bucket {
	type bucketState struct {
		b        bucket
		otherIdx int // index into b.segments, or -1 if no other yet
	}
	grouped := make(map[string]*bucketState)
	var keyOrder []string

	for _, r := range rows {
		st, ok := grouped[r.Key]
		if !ok {
			st = &bucketState{b: bucket{key: r.Key}, otherIdx: -1}
			grouped[r.Key] = st
			keyOrder = append(keyOrder, r.Key)
		}
		v := metric(r)
		c := colors[r.Model]
		if c == -1 {
			if st.otherIdx == -1 {
				st.b.segments = append(st.b.segments, segment{model: "other", color: -1, cost: v})
				st.otherIdx = len(st.b.segments) - 1
			} else {
				st.b.segments[st.otherIdx].cost += v
			}
		} else {
			st.b.segments = append(st.b.segments, segment{model: r.Model, color: c, cost: v})
		}
		st.b.total += v
	}

	// Sort segments within each bucket by cost desc.
	for _, st := range grouped {
		sort.SliceStable(st.b.segments, func(i, j int) bool {
			return st.b.segments[i].cost > st.b.segments[j].cost
		})
	}

	// Order x-axis.
	switch keyName {
	case "day", "week", "month":
		sort.Strings(keyOrder) // ascending → oldest left
	default:
		sort.SliceStable(keyOrder, func(i, j int) bool {
			return grouped[keyOrder[i]].b.total > grouped[keyOrder[j]].b.total
		})
	}

	result := make([]bucket, 0, len(keyOrder))
	for _, k := range keyOrder {
		result = append(result, grouped[k].b)
	}
	return result
}

// yAxisLabels returns one label per plot row, indexed 0 (bottom) to height-1 (top).
// Empty strings indicate "no label at this row" (we only label every ~4 rows).
// useTokens=true switches to bare token shorthand instead of "$" prefixed cost.
func yAxisLabels(maxValue float64, height int, useTokens bool) []string {
	labels := make([]string, height)
	if height == 0 {
		return labels
	}
	stride := height / 4
	if stride < 1 {
		stride = 1
	}
	for r := 0; r < height; r++ {
		if r == 0 || r == height-1 || r%stride == 0 {
			val := float64(r) * maxValue / float64(height-1)
			if useTokens {
				labels[r] = formatTokenShort(val)
			} else {
				labels[r] = formatCost(val)
			}
		}
	}
	return labels
}

func formatCost(v float64) string {
	// Sign outside the symbol, matching formatChartAmount: "$-5" reads as malformed.
	if v < 0 {
		return "-" + formatCost(-v)
	}
	// Tiers beyond "k": without them $1e12 renders as "$1000000000.0k" and the
	// y-axis gutter grows to 14 columns.
	if v >= 1e9 {
		return fmt.Sprintf("$%.1fB", v/1e9)
	}
	if v >= 1e6 {
		return fmt.Sprintf("$%.1fM", v/1e6)
	}
	if v >= 1000 {
		return fmt.Sprintf("$%.1fk", v/1000)
	}
	if v == math.Trunc(v) {
		return fmt.Sprintf("$%d", int64(v))
	}
	return fmt.Sprintf("$%.2f", v)
}

func formatTokenShort(v float64) string {
	switch {
	case v >= 1_000_000:
		return fmt.Sprintf("%.1fM", v/1_000_000)
	case v >= 1_000:
		return fmt.Sprintf("%.1fk", v/1_000)
	default:
		return fmt.Sprintf("%d", int64(v))
	}
}

// xAxisLabels returns one label per bucket, formatted according to keyName.
// When barW=1, labels are emitted at a stride targeting ~8 visible labels;
// other positions get blank strings to preserve alignment.
func xAxisLabels(buckets []bucket, keyName string, barW int) []string {
	labels := make([]string, len(buckets))
	for i, b := range buckets {
		labels[i] = formatXLabel(b.key, keyName)
	}
	if barW >= 2 {
		maxLen := barW + 1
		for i, l := range labels {
			if text.StringWidth(l) > maxLen {
				labels[i] = truncateToWidth(l, maxLen)
			}
		}
	} else {
		stride := (len(buckets) + 7) / 8 // ceil(N/8)
		if stride < 1 {
			stride = 1
		}
		for i := range labels {
			if i%stride != 0 {
				labels[i] = ""
			}
		}
	}
	return labels
}

func formatXLabel(key, keyName string) string {
	switch keyName {
	case "day":
		if len(key) >= 10 {
			return key[8:10]
		}
		return key
	case "week":
		idx := strings.Index(key, "W")
		if idx >= 0 {
			return key[idx:]
		}
		return key
	case "month":
		months := []string{"", "Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}
		if len(key) == 7 {
			n := 0
			fmt.Sscanf(key[5:], "%d", &n)
			if n >= 1 && n <= 12 {
				return months[n]
			}
		}
		return key
	default:
		return key
	}
}

// truncateToWidth trims s to at most max display columns, appending an ellipsis
// when it had to cut. Width-aware so wide (CJK) glyphs are counted as the two
// columns the terminal actually advances.
func truncateToWidth(s string, max int) string {
	if text.StringWidth(s) <= max {
		return s
	}
	if max < 1 {
		return ""
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		w := text.StringWidth(string(r))
		if used+w > max-1 {
			break
		}
		b.WriteRune(r)
		used += w
	}
	return b.String() + "…"
}

// chartPalette maps color index 0..5 to ANSI color sequences.
var chartPalette = []text.Colors{
	{text.FgHiCyan},
	{text.FgHiMagenta},
	{text.FgHiYellow},
	{text.FgHiGreen},
	{text.FgHiBlue},
	{text.FgHiRed},
}

var chartOtherColor = text.Colors{text.FgHiBlack}

const chartBarRune = "█"
const chartLegendRune = "■"
const chartGutterWidth = 6 // right-aligned y-axis labels (minimum; grows via chartGutterFor)

// maxChartHeight is far above any terminal but low enough that a typo cannot
// allocate its way into a hang or a makeslice panic.
const maxChartHeight = 1000

// chartGutterFor returns the gutter width needed to fit y-axis labels.
// It's at least chartGutterWidth (the default minimum) and grows to fit
// the widest label produced by yAxisLabels.
// Uses StringWidth for one rule across the file, though y-axis labels are
// formatCost/formatTokenShort output and therefore ASCII by construction — this
// site is uniformity, not a tested behaviour.
func chartGutterFor(maxValue float64, height int, useTokens bool) int {
	w := chartGutterWidth
	for _, l := range yAxisLabels(maxValue, height, useTokens) {
		if rw := text.StringWidth(l); rw > w {
			w = rw
		}
	}
	return w
}

// gridCell values below 0 are sentinels: -2 is an empty cell, -1 is the grey
// "other" stack, and 0..len(chartPalette)-1 index the palette.
const gridEmpty = -2

// chartAccounting divides a bucket set's total into what the plot draws and what
// it cannot. The two must sum to the bucket totals — see
// TestBuildChartGrid_AccountsForEveryDollar, which is the guard against the
// recurring failure of computing a quantity at one stage and displaying it at
// another.
type chartAccounting struct {
	drawnCost float64
	undrawn   float64
	// blankBuckets counts columns that keep an x-axis label while drawing no
	// bar at all, which otherwise reads as a period with no spend.
	blankBuckets int
}

// chartGrid is buildChartGrid's output: the colour grid, the per-bucket segments
// actually used for rendering (post-fold), which of them reached a cell, the
// models whose name was lost to "other" everywhere, and the accounting.
type chartGrid struct {
	chartAccounting
	grid     [][]int
	rendered [][]segment
	drawn    map[string]bool
	merged   map[string]float64
}

func segmentKey(s segment) string { return fmt.Sprintf("%d|%s", s.color, s.model) }

// buildChartGrid maps buckets onto a height-row grid and accounts for every unit
// of the bucket totals along the way. Bars are quantized, so some spend inside
// the announced total reaches no pixel: a bucket under about half a row of the
// tallest, or a segment in a bar too short to lend it one.
func buildChartGrid(buckets []bucket, height int, maxTotal float64) chartGrid {
	g := chartGrid{
		grid:     make([][]int, len(buckets)),
		rendered: make([][]segment, len(buckets)),
		drawn:    make(map[string]bool),
		merged:   make(map[string]float64),
	}
	drawnModels := make(map[string]bool)
	// Whether each bucket's post-fold "other" stack won a row. Where it did not,
	// the folded money reached no pixel and belongs to undrawn, not to merged.
	otherDrew := make([]bool, len(buckets))

	for bi, b := range buckets {
		g.grid[bi] = make([]int, height)
		for r := range g.grid[bi] {
			g.grid[bi][r] = gridEmpty
		}
		bucketHeight := int(math.Round(b.total / maxTotal * float64(height)))
		if bucketHeight > height {
			bucketHeight = height
		}
		// <= 0, not == 0: a negative total (a negative published rate) yields a
		// negative height, which drew nothing and was counted nowhere.
		if bucketHeight <= 0 {
			g.blankBuckets++
		}
		segs, segH := foldSubThresholdSegments(b.segments, bucketHeight)
		g.rendered[bi] = segs
		// Bounded by bucketHeight, not just by the plot height: splitSegments
		// clamps its leftover but not its total, so a negative segment can make
		// the heights sum past the bar's own height and overdraw it.
		// bucketHeight is already capped at height above.
		drawable := bucketHeight
		row := 0
		for i, h := range segH {
			if h == 0 {
				g.undrawn += segs[i].cost
				continue
			}
			if segs[i].color == -1 {
				otherDrew[bi] = true
			}
			g.drawnCost += segs[i].cost
			g.drawn[segmentKey(segs[i])] = true
			drawnModels[segs[i].model] = true
			for j := 0; j < h && row < drawable; j++ {
				g.grid[bi][row] = segs[i].color
				row++
			}
		}
	}

	// A model holding a palette colour that never wins a row anywhere keeps its
	// money on the chart, inside the grey "other" stack, but loses its name.
	// Only where that stack actually drew: otherwise the money is undrawn, and
	// reporting it here would announce the same dollars twice with contradictory
	// explanations — and point at a grey stack that is not on screen.
	for bi, b := range buckets {
		if !otherDrew[bi] {
			continue
		}
		for _, s := range b.segments {
			// s.cost > 0: an unpriced model contributes exactly 0 in cost mode, so
			// it has no money inside "other" to go looking for. The separate
			// "some models have no pricing" line already describes those.
			if s.color != -1 && s.cost > 0 && !drawnModels[s.model] {
				g.merged[s.model] += s.cost
			}
		}
	}
	return g
}

// formatChartAmount renders a chart quantity exactly, in whichever unit is being
// plotted. Distinct from formatCost, whose "$1.2k" shorthand exists for y-axis
// labels: a figure printed so the reader can reconcile it against the table must
// not be rounded to three significant figures, since the rounding error can
// exceed the amount being disclosed.
func formatChartAmount(v float64, useTokens bool) string {
	if useTokens {
		return formatInt(int64(v))
	}
	// Sign outside the symbol: "$-30.00" reads as a malformed figure.
	if v < 0 {
		return fmt.Sprintf("-$%.2f", -v)
	}
	return fmt.Sprintf("$%.2f", v)
}

// plural picks a noun form without the caller repeating the count.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// reportUndrawn discloses spend counted in the title but too small to draw. It
// covers quantization only — money dropped by bucket truncation has a different
// cause and a different remedy, and is disclosed by FormatChart instead.
func reportUndrawn(acct chartAccounting, o chartOpts) {
	amount := formatChartAmount(acct.undrawn, o.useTokens)
	// Below display precision the amount is noise, but a labelled blank column
	// still reads as "no spend here", so the columns stay worth naming. A
	// negative amount is not negligible — it is a real figure the plot cannot
	// show — so only exact zero and sub-precision qualify.
	// math.Abs: a small negative formats as "-$0.00", which never equals "$0.00",
	// so comparing the strings let the exact spelling this code calls "a defect
	// rather than a disclosure" through on the negative side.
	negligible := formatChartAmount(math.Abs(acct.undrawn), o.useTokens) == formatChartAmount(0, o.useTokens)
	if negligible && acct.blankBuckets == 0 {
		return
	}

	var msg string
	switch {
	case negligible:
		msg = fmt.Sprintf("%d %s nothing large enough to plot",
			acct.blankBuckets, plural(acct.blankBuckets, "bucket has", "buckets have"))
	case acct.blankBuckets > 0:
		msg = fmt.Sprintf("%s is too small to plot, including %d empty %s",
			amount, acct.blankBuckets, plural(acct.blankBuckets, "bucket", "buckets"))
	default:
		msg = fmt.Sprintf("%s is too small to plot", amount)
	}
	// Suggest a taller plot only when it could work: not at the height ceiling,
	// and not when there is no amount to enlarge. A column of nothing but
	// unpriced usage totals exactly zero, and no height draws that — the honest
	// signal for it is the separate "some models have no pricing" line.
	if o.height < maxChartHeight && acct.undrawn > 0 {
		msg += "; raise --height for more detail"
	}
	fmt.Fprintln(os.Stderr, msg)
}

// bucketsMax returns the tallest bucket total. Zero means there is nothing to
// draw, which render treats as a silent no-op — so callers must say so first.
func bucketsMax(buckets []bucket) float64 {
	var m float64
	for _, b := range buckets {
		if b.total > m {
			m = b.total
		}
	}
	return m
}

func reportNothingToPlot() {
	fmt.Fprintln(os.Stderr, "nothing to plot: every bucket shown is zero")
}

// reportMergedModels discloses models whose money is drawn inside "other" but
// whose name appears nowhere. Separate from reportUndrawn because this spend
// *is* plotted — only the attribution is lost.
func reportMergedModels(merged map[string]float64, o chartOpts) {
	if len(merged) == 0 {
		return
	}
	var total float64
	for _, cost := range merged {
		total += cost
	}
	// Below display precision there is no attribution worth reclaiming, and a
	// note reading "($0.00)" looks like a defect rather than a disclosure.
	if formatChartAmount(math.Abs(total), o.useTokens) == formatChartAmount(0, o.useTokens) {
		return
	}
	msg := fmt.Sprintf(`%d %s merged into "other" (%s)`,
		len(merged), plural(len(merged), "model", "models"),
		formatChartAmount(total, o.useTokens))
	if o.height < maxChartHeight {
		msg += "; raise --height to name them"
	}
	fmt.Fprintln(os.Stderr, msg)
}

// chartOpts carries render's non-bucket inputs. A struct rather than positional
// parameters because three of them are bools, where a call site says nothing
// about which is which.
type chartOpts struct {
	height, width int
	keyName       string
	useTokens     bool
	// costIncomplete drives the "*" marker: some rows have no known pricing.
	costIncomplete bool
	// grandTotal is the total across every bucket, including any FormatChart
	// dropped to fit the width. The headline figure must match the table's, not
	// just the part that fit.
	grandTotal float64
}

// render draws the chart to w. Callers are responsible for ensuring color is
// usable (FormatChart handles TTY/NO_COLOR detection upstream) and for
// truncating buckets to fit the width. Returns the first write error
// encountered, if any.
func render(w io.Writer, buckets []bucket, o chartOpts) error {
	height, width, keyName := o.height, o.width, o.keyName
	useTokens := o.useTokens
	if len(buckets) == 0 || height < 1 || width < 1 {
		return nil
	}

	maxTotal := bucketsMax(buckets)
	if maxTotal <= 0 {
		return nil
	}
	grandTotal := o.grandTotal

	gutterW := chartGutterFor(maxTotal, height, useTokens)
	leftOffset := gutterW + 3

	plotWidth := width - leftOffset
	if plotWidth < 1 {
		plotWidth = 1
	}

	// Compute bar width: each bar consumes barW + 1 (1-char gap).
	barW := (plotWidth + 1) / len(buckets)
	barW--
	if barW < 1 {
		barW = 1
	}
	if barW > 4 {
		barW = 4
	}

	g := buildChartGrid(buckets, height, maxTotal)
	grid, rendered, drawn := g.grid, g.rendered, g.drawn
	reportUndrawn(g.chartAccounting, o)
	reportMergedModels(g.merged, o)

	// Title. The headline is stated exactly rather than abbreviated: it is the
	// figure a reader reconciles against the table and against the disclosure
	// lines, and "$3.0k" for $2961.81 is off by more than the amounts those
	// lines carefully account for.
	var title string
	if useTokens {
		title = fmt.Sprintf("Tokens by %s", keyName)
	} else {
		title = fmt.Sprintf("Cost by %s (USD)", keyName)
	}
	totalStr := formatChartAmount(grandTotal, useTokens)
	totalSuffix := fmt.Sprintf("Total: %s", totalStr)
	if o.costIncomplete && !useTokens {
		// Same marker the table uses, so the two formats agree in-band and not
		// only on stderr. Token mode needs none: that total is complete.
		totalSuffix += "*"
	}
	// Same note as chartGutterFor: the title is built from a validated --by
	// value and a formatted amount, both ASCII, so this is uniformity only.
	titlePad := width - text.StringWidth(title) - text.StringWidth(totalSuffix)
	if titlePad < 2 {
		titlePad = 2
	}
	if _, err := fmt.Fprintf(w, "%s%s%s\n\n", title, strings.Repeat(" ", titlePad), totalSuffix); err != nil {
		return err
	}

	// Plot rows (top → bottom).
	yLabels := yAxisLabels(maxTotal, height, useTokens)
	for r := height - 1; r >= 0; r-- {
		label := yLabels[r]
		gutter := fmt.Sprintf("%*s", gutterW, label)
		var sb strings.Builder
		sb.WriteString(gutter)
		sb.WriteString(" │ ")
		for bi := range buckets {
			c := grid[bi][r]
			if c == gridEmpty {
				sb.WriteString(strings.Repeat(" ", barW))
			} else {
				bar := strings.Repeat(chartBarRune, barW)
				if c == -1 {
					sb.WriteString(chartOtherColor.Sprint(bar))
				} else {
					sb.WriteString(chartPalette[c].Sprint(bar))
				}
			}
			if bi < len(buckets)-1 {
				sb.WriteString(" ")
			}
		}
		sb.WriteString("\n")
		if _, err := io.WriteString(w, sb.String()); err != nil {
			return err
		}
	}

	// Floor.
	floorRule := strings.Repeat("─", (barW+1)*len(buckets))
	if _, err := fmt.Fprintf(w, "%s └%s\n", strings.Repeat(" ", gutterW), floorRule); err != nil {
		return err
	}

	// X-axis labels.
	xLabels := xAxisLabels(buckets, keyName, barW)
	var xRow strings.Builder
	xRow.WriteString(strings.Repeat(" ", leftOffset))
	for bi, lbl := range xLabels {
		field := barW + 1
		if bi == len(xLabels)-1 {
			field = barW
		}
		// Display columns, not runes: a CJK project name advances the terminal
		// two columns per rune, so rune-based padding drifts the whole row right
		// of the bars it labels.
		if text.StringWidth(lbl) > field {
			lbl = truncateToWidth(lbl, field)
		}
		left := (field - text.StringWidth(lbl)) / 2
		right := field - text.StringWidth(lbl) - left
		if left < 0 {
			left = 0
		}
		if right < 0 {
			right = 0
		}
		xRow.WriteString(strings.Repeat(" ", left))
		xRow.WriteString(lbl)
		xRow.WriteString(strings.Repeat(" ", right))
	}
	xRow.WriteString("\n")
	if _, err := io.WriteString(w, xRow.String()); err != nil {
		return err
	}

	// Legend.
	type legendEntry struct {
		color int
		model string
	}
	seen := make(map[string]bool)
	var legend []legendEntry
	for _, segs := range rendered {
		for _, s := range segs {
			k := segmentKey(s)
			if !seen[k] && drawn[k] {
				seen[k] = true
				legend = append(legend, legendEntry{color: s.color, model: s.model})
			}
		}
	}
	sort.SliceStable(legend, func(i, j int) bool {
		ai, aj := legend[i].color, legend[j].color
		if ai == -1 && aj != -1 {
			return false
		}
		if aj == -1 && ai != -1 {
			return true
		}
		return ai < aj
	})

	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	var leg strings.Builder
	leg.WriteString(strings.Repeat(" ", leftOffset))
	for i, e := range legend {
		var swatch string
		if e.color == -1 {
			swatch = chartOtherColor.Sprint(chartLegendRune)
		} else {
			swatch = chartPalette[e.color].Sprint(chartLegendRune)
		}
		if i > 0 {
			leg.WriteString("  ")
		}
		leg.WriteString(swatch)
		leg.WriteString(" ")
		leg.WriteString(e.model)
	}
	leg.WriteString("\n")
	if _, err := io.WriteString(w, leg.String()); err != nil {
		return err
	}
	return nil
}

// ValidateChartOptions reports whether the chart bounds are usable. Exported so
// the CLI can reject them before scanning session logs rather than after.
func ValidateChartOptions(height, topN int) error {
	if height < 6 {
		return fmt.Errorf("--height must be at least 6")
	}
	// yAxisLabels allocates one entry per row unconditionally, so an unbounded
	// height hangs on a large value and panics in makeslice on a huge one.
	if height > maxChartHeight {
		return fmt.Errorf("--height must be at most %d", maxChartHeight)
	}
	if topN < 1 {
		return fmt.Errorf("--top must be at least 1")
	}
	// assignColors hands out indices 0..topN-1 and render indexes chartPalette
	// with them, so anything above the palette size is a panic waiting for a
	// model at that index to draw a row.
	if topN > len(chartPalette) {
		return fmt.Errorf("--top must be at most %d", len(chartPalette))
	}
	return nil
}

// FormatChart writes a vertical stacked bar chart to w. keyName matches the
// --by value (used for x-axis labeling). height is the plot height (minimum 6).
// topN is the maximum number of distinct model stacks (minimum 1).
//
// Falls back to FormatTable when:
//   - w is not a TTY (and FORCE_COLOR is not set)
//   - NO_COLOR env var is set
//   - terminal width is too narrow for a chart
func FormatChart(w io.Writer, rows []Row, keyName string, height, topN int) error {
	if err := ValidateChartOptions(height, topN); err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}

	if !chartColorAllowed(w) {
		fmt.Fprintln(os.Stderr, "chart requires a color terminal; falling back to table")
		FormatTable(w, rows, keyName)
		return nil
	}

	width := chartWidthOf(w)
	if width < 30 {
		fmt.Fprintln(os.Stderr, "terminal too narrow for chart; falling back to table")
		FormatTable(w, rows, keyName)
		return nil
	}

	var hasAnyCost, anyRowLacksCost bool
	var totalCost float64
	for _, r := range rows {
		if r.HasCost {
			hasAnyCost = true
			totalCost += r.Cost
		} else {
			anyRowLacksCost = true
		}
	}
	metric := costMetric
	useTokens := false
	switch {
	case !hasAnyCost:
		fmt.Fprintln(os.Stderr, "no pricing data for any model; charting tokens instead of cost")
		metric = tokenMetric
		useTokens = true
	case totalCost == 0:
		// Priced, but at zero — the cache carries zero-rate entries. A cost chart
		// would render nothing at all, which reads as failure rather than as $0.
		fmt.Fprintln(os.Stderr, "every priced model costs zero; charting tokens instead of cost")
		metric = tokenMetric
		useTokens = true
	case anyRowLacksCost:
		// Signalled twice on purpose: the title carries table's "*" marker for
		// anyone reading the chart, and this line survives being scrolled past.
		fmt.Fprintln(os.Stderr, "some models have no pricing; chart total excludes them")
	}

	colors := assignColors(rows, topN, metric)
	buckets := bucketize(rows, colors, keyName, metric)

	// Captured before any truncation below: the headline total reports the whole
	// report, matching the table, while the plot shows what fits.
	var maxTotal, grandTotal float64
	for _, b := range buckets {
		grandTotal += b.total
		if b.total > maxTotal {
			maxTotal = b.total
		}
	}
	if maxTotal <= 0 {
		reportNothingToPlot()
		return nil
	}

	gutterW := chartGutterFor(maxTotal, height, useTokens)
	plotWidth := width - gutterW - 3
	maxBuckets := plotWidth / 2
	if maxBuckets < 1 {
		maxBuckets = 1
	}
	if len(buckets) > maxBuckets {
		total := len(buckets)
		switch keyName {
		case "day", "week", "month":
			buckets = buckets[len(buckets)-maxBuckets:]
		default:
			buckets = buckets[:maxBuckets]
		}
		// Named, not just counted. The headline includes these buckets, so a bare
		// count leaves the reader to infer the gap — and next to the quantization
		// note, which is usually far smaller, a count reads as the lesser figure.
		var shown float64
		for _, b := range buckets {
			shown += b.total
		}
		hint := "--since/--last"
		switch keyName {
		case "project":
			hint = "--project"
		case "session":
			hint = "--project/--model"
		}
		// The amount is omitted rather than printed as "$0.00", which reads as a
		// defect; the count and the remedy still stand on their own.
		dropped := ""
		if amount := formatChartAmount(grandTotal-shown, useTokens); formatChartAmount(math.Abs(grandTotal-shown), useTokens) != formatChartAmount(0, useTokens) {
			dropped = fmt.Sprintf(" (%s not plotted)", amount)
		}
		fmt.Fprintf(os.Stderr, "showing %d of %d buckets%s; narrow with %s\n",
			maxBuckets, total, dropped, hint)
	}

	// Truncation keeps the newest buckets for time keys, so the tallest can be
	// among the ones dropped. Re-check against the survivors, or render returns
	// silently after FormatChart has already promised output on stderr.
	// Recomputed rather than passed into render: a chartOpts field would have to
	// stay in agreement with the buckets beside it, and disagreeing renders an
	// empty chart silently. One loop over at most 35 buckets is the cheaper risk.
	if bucketsMax(buckets) <= 0 {
		reportNothingToPlot()
		return nil
	}

	return render(w, buckets, chartOpts{
		height:         height,
		width:          width,
		keyName:        keyName,
		useTokens:      useTokens,
		costIncomplete: anyRowLacksCost,
		grandTotal:     grandTotal,
	})
}

// chartColorAllowed reports whether ANSI color output should be emitted.
// Honors FORCE_COLOR (always on), NO_COLOR (always off), and TTY detection.
func chartColorAllowed(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("FORCE_COLOR") != "" {
		return true
	}
	if f, ok := w.(*os.File); ok {
		return term.IsTerminal(int(f.Fd()))
	}
	return false
}

// chartWidthOf is a var so tests can drive the narrow-terminal fallback and the
// bucket clamp, which are otherwise unreachable: every test writes to a
// bytes.Buffer, for which chartTerminalWidth always answers 80.
var chartWidthOf = chartTerminalWidth

// chartTerminalWidth returns the columns reported by the OS for w if it's a
// terminal, or 80 otherwise.
func chartTerminalWidth(w io.Writer) int {
	if f, ok := w.(*os.File); ok {
		if width, _, err := term.GetSize(int(f.Fd())); err == nil && width > 0 {
			return width
		}
	}
	return 80
}

// foldSubThresholdSegments merges segments too small to draw a row into the
// bucket's "other" segment, then re-splits. Without it, sub-threshold spend
// disappears from the chart entirely — no bar and, once the legend is filtered
// to what renders, no legend entry either. Returns the segments to render and
// their row heights, which stay index-aligned.
func foldSubThresholdSegments(segments []segment, bucketHeight int) ([]segment, []int) {
	costsOf := func(segs []segment) []float64 {
		costs := make([]float64, len(segs))
		for i, s := range segs {
			costs[i] = s.cost
		}
		return costs
	}

	kept := segments
	segH := splitSegments(costsOf(kept), bucketHeight)

	// Merging raises the "other" floor, which can push a segment that drew before
	// the fold below it — leaving that cost in neither a bar of its own nor in
	// "other". So repeat until nothing new falls through. Each pass that folds
	// anything removes at least one segment, making len(segments) a sufficient
	// bound. One exception: when the foldable segments *sum* to 0 — including a
	// mix such as {+5, -5} — the loop breaks on `folded == 0` with them still
	// present. Nothing is lost from the total, but "no non-other segment survives
	// at zero rows" holds only while the foldable set does not sum to zero.
	for range len(segments) {
		var folded float64
		next := make([]segment, 0, len(kept))
		for i, s := range kept {
			if segH[i] == 0 && s.color != -1 {
				folded += s.cost
				continue
			}
			next = append(next, s)
		}
		if folded == 0 {
			break
		}
		otherIdx := -1
		for i := range next {
			if next[i].color == -1 {
				otherIdx = i
				break
			}
		}
		if otherIdx >= 0 {
			next[otherIdx].cost += folded
		} else {
			next = append(next, segment{model: "other", color: -1, cost: folded})
		}
		// Restore the bottom-up cost-desc stacking convention after the merge.
		sort.SliceStable(next, func(i, j int) bool { return next[i].cost > next[j].cost })
		kept = next
		segH = splitSegments(costsOf(kept), bucketHeight)
	}

	// A merged "other" too small to win a row would be dropped by the caller's
	// drawn filter, putting the folded spend back exactly where it started:
	// absent from both plot and legend. Borrow a row from the tallest segment so
	// the bar still accounts for every dollar in it.
	for i, s := range kept {
		if s.color != -1 || s.cost <= 0 || segH[i] != 0 {
			continue
		}
		tallest := 0
		for j, h := range segH {
			if h > segH[tallest] {
				tallest = j
			}
		}
		if segH[tallest] > 1 {
			segH[tallest]--
			segH[i] = 1
		}
		break
	}
	return kept, segH
}

// splitSegments allocates totalRows among segments using Hamilton's
// largest-remainder method. Segments whose proportional share is below 0.5
// rows are dropped (returned as 0). The sum of the result is at most totalRows
// (less when small-share segments are dropped or when total cost is zero).
func splitSegments(costs []float64, totalRows int) []int {
	result := make([]int, len(costs))
	if totalRows <= 0 || len(costs) == 0 {
		return result
	}
	var sum float64
	for _, c := range costs {
		sum += c
	}
	if sum <= 0 {
		return result
	}

	type slot struct {
		idx       int
		floor     int
		remainder float64
	}
	var slots []slot
	for i, c := range costs {
		ideal := c / sum * float64(totalRows)
		if ideal < 0.5 {
			continue
		}
		f := int(math.Floor(ideal))
		slots = append(slots, slot{idx: i, floor: f, remainder: ideal - float64(f)})
	}

	allocated := 0
	for _, s := range slots {
		result[s.idx] = s.floor
		allocated += s.floor
	}
	leftover := totalRows - allocated
	if leftover < 0 {
		leftover = 0
	}

	sort.SliceStable(slots, func(i, j int) bool {
		if slots[i].remainder != slots[j].remainder {
			return slots[i].remainder > slots[j].remainder
		}
		return slots[i].idx < slots[j].idx
	})
	for i := 0; i < leftover && i < len(slots); i++ {
		result[slots[i].idx]++
	}
	return result
}
