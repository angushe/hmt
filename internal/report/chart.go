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
			if len([]rune(l)) > maxLen {
				labels[i] = truncateWithEllipsis(l, maxLen)
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

func truncateWithEllipsis(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max < 1 {
		return ""
	}
	return string(r[:max-1]) + "…"
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
const chartGutterWidth = 6                   // right-aligned y-axis labels
const chartLeftOffset = chartGutterWidth + 3 // gutter + " │ "

// render draws the chart to w. Caller is responsible for ensuring color is
// usable (FormatChart handles TTY/NO_COLOR detection upstream) and for
// truncating buckets to fit width (FormatChart enforces
// len(buckets) <= (width-chartGutterWidth-3)/2 — Task 7). Returns the first
// write error encountered, if any.
func render(w io.Writer, buckets []bucket, height, width int, keyName string, useTokens bool) error {
	if len(buckets) == 0 || height < 1 || width < 1 {
		return nil
	}

	plotWidth := width - chartLeftOffset
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

	var maxTotal float64
	var grandTotal float64
	for _, b := range buckets {
		grandTotal += b.total
		if b.total > maxTotal {
			maxTotal = b.total
		}
	}
	if maxTotal <= 0 {
		return nil
	}

	// Build per-bucket per-row color grid. -2 = empty, -1 = other, 0..5 = palette.
	const empty = -2
	grid := make([][]int, len(buckets))
	for bi, b := range buckets {
		grid[bi] = make([]int, height)
		for r := range grid[bi] {
			grid[bi][r] = empty
		}
		bucketHeight := int(math.Round(b.total / maxTotal * float64(height)))
		if bucketHeight > height {
			bucketHeight = height
		}
		costs := make([]float64, len(b.segments))
		for i, s := range b.segments {
			costs[i] = s.cost
		}
		segH := splitSegments(costs, bucketHeight)
		row := 0
		for i, h := range segH {
			for j := 0; j < h && row < height; j++ {
				grid[bi][row] = b.segments[i].color
				row++
			}
		}
	}

	// Title.
	var title, totalStr string
	if useTokens {
		title = fmt.Sprintf("Tokens by %s", keyName)
		totalStr = formatTokenShort(grandTotal)
	} else {
		title = fmt.Sprintf("Cost by %s (USD)", keyName)
		totalStr = formatCost(grandTotal)
	}
	totalSuffix := fmt.Sprintf("Total: %s", totalStr)
	titlePad := width - len(title) - len(totalSuffix)
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
		gutter := fmt.Sprintf("%*s", chartGutterWidth, label)
		var sb strings.Builder
		sb.WriteString(gutter)
		sb.WriteString(" │ ")
		for bi := range buckets {
			c := grid[bi][r]
			if c == empty {
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
	if _, err := fmt.Fprintf(w, "%s └%s\n", strings.Repeat(" ", chartGutterWidth), floorRule); err != nil {
		return err
	}

	// X-axis labels.
	xLabels := xAxisLabels(buckets, keyName, barW)
	var xRow strings.Builder
	xRow.WriteString(strings.Repeat(" ", chartLeftOffset))
	for bi, lbl := range xLabels {
		field := barW + 1
		if bi == len(xLabels)-1 {
			field = barW
		}
		if len([]rune(lbl)) > field {
			lbl = truncateWithEllipsis(lbl, field)
		}
		left := (field - len([]rune(lbl))) / 2
		right := field - len([]rune(lbl)) - left
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
	for _, b := range buckets {
		for _, s := range b.segments {
			k := fmt.Sprintf("%d|%s", s.color, s.model)
			if !seen[k] {
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
	leg.WriteString(strings.Repeat(" ", chartLeftOffset))
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

// FormatChart writes a vertical stacked bar chart to w. keyName matches the
// --by value (used for x-axis labeling). height is the plot height (minimum 6).
// topN is the maximum number of distinct model stacks (minimum 1).
//
// Falls back to FormatTable when:
//   - w is not a TTY (and FORCE_COLOR is not set)
//   - NO_COLOR env var is set
//   - terminal width is too narrow for a chart
func FormatChart(w io.Writer, rows []Row, keyName string, height, topN int) error {
	if height < 6 {
		return fmt.Errorf("--height must be at least 6")
	}
	if topN < 1 {
		return fmt.Errorf("--top must be at least 1")
	}
	if len(rows) == 0 {
		return nil
	}

	if !chartColorAllowed(w) {
		fmt.Fprintln(os.Stderr, "chart requires a color terminal; falling back to table")
		FormatTable(w, rows, keyName)
		return nil
	}

	width := chartTerminalWidth(w)
	if width < 30 {
		fmt.Fprintln(os.Stderr, "terminal too narrow for chart; falling back to table")
		FormatTable(w, rows, keyName)
		return nil
	}

	var hasAnyCost bool
	for _, r := range rows {
		if r.HasCost {
			hasAnyCost = true
			break
		}
	}
	metric := costMetric
	useTokens := false
	if !hasAnyCost {
		fmt.Fprintln(os.Stderr, "no pricing data for any model; charting tokens instead of cost")
		metric = tokenMetric
		useTokens = true
	}

	colors := assignColors(rows, topN, metric)
	buckets := bucketize(rows, colors, keyName, metric)

	plotWidth := width - chartLeftOffset
	maxBuckets := plotWidth / 2
	if maxBuckets < 1 {
		maxBuckets = 1
	}
	if len(buckets) > maxBuckets {
		dropped := len(buckets) - maxBuckets
		switch keyName {
		case "day", "week", "month":
			buckets = buckets[len(buckets)-maxBuckets:]
		default:
			buckets = buckets[:maxBuckets]
		}
		hint := "--since/--last"
		switch keyName {
		case "project":
			hint = "--project"
		case "session":
			hint = "--project/--model"
		}
		fmt.Fprintf(os.Stderr, "showing %d of %d buckets; narrow with %s\n",
			maxBuckets, maxBuckets+dropped, hint)
	}

	return render(w, buckets, height, width, keyName, useTokens)
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
