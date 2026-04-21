package report

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/tw"

	"github.com/angus/hmt/internal/parser"
	"github.com/angus/hmt/internal/pricing"
)

// GroupBy specifies the aggregation dimension.
type GroupBy int

const (
	ByDay GroupBy = iota
	ByWeek
	ByMonth
	BySession
	ByProject
)

// Row is one aggregated row in the report.
type Row struct {
	Key              string
	Model            string
	InputTokens      int64
	OutputTokens     int64
	CacheWriteTokens int64
	CacheReadTokens  int64
	Cost             float64
	HasCost          bool
}

// Filter returns records matching the given criteria.
// nil since/until means no bound. Empty model/project means no filter.
func Filter(records []parser.Record, since, until *time.Time, model, project string) []parser.Record {
	var result []parser.Record
	for _, r := range records {
		if since != nil && r.Timestamp.Before(*since) {
			continue
		}
		if until != nil && !r.Timestamp.Before(*until) {
			continue
		}
		if model != "" && r.Model != model {
			continue
		}
		if project != "" && !strings.Contains(r.ProjectDir, project) {
			continue
		}
		result = append(result, r)
	}
	return result
}

// Aggregate groups records by the given dimension + model and sums tokens.
// loc is used for day/week/month boundaries. If nil, time.Local is used.
func Aggregate(records []parser.Record, by GroupBy, loc *time.Location) []Row {
	if loc == nil {
		loc = time.Local
	}
	type aggKey struct {
		key   string
		model string
	}
	sums := make(map[aggKey]*Row)
	var order []aggKey

	for _, r := range records {
		var k string
		local := r.Timestamp.In(loc)
		switch by {
		case ByDay:
			k = local.Format("2006-01-02")
		case ByWeek:
			year, week := local.ISOWeek()
			k = fmt.Sprintf("%d-W%02d", year, week)
		case ByMonth:
			k = local.Format("2006-01")
		case BySession:
			k = r.SessionID
		case ByProject:
			k = parser.ProjectName(r.ProjectDir)
		}
		ak := aggKey{key: k, model: r.Model}
		row, ok := sums[ak]
		if !ok {
			row = &Row{Key: k, Model: r.Model}
			sums[ak] = row
			order = append(order, ak)
		}
		row.InputTokens += r.InputTokens
		row.OutputTokens += r.OutputTokens
		row.CacheWriteTokens += r.CacheWriteTokens
		row.CacheReadTokens += r.CacheReadTokens
	}

	// Sort: by key descending (newest first), then model ascending
	sort.Slice(order, func(i, j int) bool {
		if order[i].key != order[j].key {
			return order[i].key > order[j].key
		}
		return order[i].model < order[j].model
	})

	rows := make([]Row, len(order))
	for i, ak := range order {
		rows[i] = *sums[ak]
	}
	return rows
}

// ComputeCosts fills in the Cost and HasCost fields for each row.
func ComputeCosts(rows []Row, table *pricing.Table) {
	for i := range rows {
		p, ok := table.Lookup(rows[i].Model)
		if !ok {
			rows[i].HasCost = false
			continue
		}
		rows[i].Cost = pricing.Cost(p, rows[i].InputTokens, rows[i].OutputTokens, rows[i].CacheWriteTokens, rows[i].CacheReadTokens)
		rows[i].HasCost = true
	}
}

// colorEnabled returns true if the writer is stdout and color is not disabled.
func colorEnabled(w io.Writer) bool {
	if f, ok := w.(*os.File); ok {
		return f == os.Stdout && color.NoColor == false
	}
	return false
}

// FormatTable writes an ASCII table to w with optional color.
func FormatTable(w io.Writer, rows []Row, keyName string) {
	useColor := colorEnabled(w)
	costColor := color.New(color.FgGreen)
	dimColor := color.New(color.FgHiBlack)
	boldColor := color.New(color.Bold)

	table := tablewriter.NewTable(w,
		tablewriter.WithHeader([]string{keyName, "Model", "Input", "Output", "Cache Write", "Cache Read", "Cost"}),
		tablewriter.WithHeaderAlignment(tw.AlignLeft),
		tablewriter.WithRowAlignment(tw.AlignRight),
	)

	var totalIn, totalOut, totalCW, totalCR int64
	var totalCost float64
	allHaveCost := true

	for _, r := range rows {
		cost := "N/A"
		if r.HasCost {
			costVal := fmt.Sprintf("$%.2f", r.Cost)
			if useColor {
				cost = costColor.Sprint(costVal)
			} else {
				cost = costVal
			}
			totalCost += r.Cost
		} else {
			allHaveCost = false
			if useColor {
				cost = dimColor.Sprint("N/A")
			}
		}
		model := r.Model
		if useColor {
			model = dimColor.Sprint(model)
		}
		table.Append([]string{
			r.Key,
			model,
			formatInt(r.InputTokens),
			formatInt(r.OutputTokens),
			formatInt(r.CacheWriteTokens),
			formatInt(r.CacheReadTokens),
			cost,
		})
		totalIn += r.InputTokens
		totalOut += r.OutputTokens
		totalCW += r.CacheWriteTokens
		totalCR += r.CacheReadTokens
	}

	costStr := fmt.Sprintf("$%.2f", totalCost)
	if !allHaveCost {
		costStr += "*"
	}
	totalLabel := "Total"
	if useColor {
		costStr = boldColor.Sprint(costStr)
		totalLabel = boldColor.Sprint(totalLabel)
	}
	table.Footer([]string{totalLabel, "", formatInt(totalIn), formatInt(totalOut), formatInt(totalCW), formatInt(totalCR), costStr})
	table.Render()
}

// FormatJSON writes rows as a JSON array to w.
func FormatJSON(w io.Writer, rows []Row, keyName string) {
	type jsonRow struct {
		Key              string  `json:"key"`
		Model            string  `json:"model"`
		InputTokens      int64   `json:"input_tokens"`
		OutputTokens     int64   `json:"output_tokens"`
		CacheWriteTokens int64   `json:"cache_write_tokens"`
		CacheReadTokens  int64   `json:"cache_read_tokens"`
		Cost             float64 `json:"cost,omitempty"`
		HasCost          bool    `json:"-"`
	}
	out := make([]jsonRow, len(rows))
	for i, r := range rows {
		out[i] = jsonRow{
			Key:              r.Key,
			Model:            r.Model,
			InputTokens:      r.InputTokens,
			OutputTokens:     r.OutputTokens,
			CacheWriteTokens: r.CacheWriteTokens,
			CacheReadTokens:  r.CacheReadTokens,
		}
		if r.HasCost {
			out[i].Cost = r.Cost
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(out)
}

// FormatCSV writes rows as CSV to w.
func FormatCSV(w io.Writer, rows []Row, keyName string) {
	cw := csv.NewWriter(w)
	cw.Write([]string{keyName, "model", "input_tokens", "output_tokens", "cache_write_tokens", "cache_read_tokens", "cost"})
	for _, r := range rows {
		cost := ""
		if r.HasCost {
			cost = strconv.FormatFloat(r.Cost, 'f', 6, 64)
		}
		cw.Write([]string{
			r.Key,
			r.Model,
			strconv.FormatInt(r.InputTokens, 10),
			strconv.FormatInt(r.OutputTokens, 10),
			strconv.FormatInt(r.CacheWriteTokens, 10),
			strconv.FormatInt(r.CacheReadTokens, 10),
			cost,
		})
	}
	cw.Flush()
}

func formatInt(n int64) string {
	s := strconv.FormatInt(n, 10)
	if len(s) <= 3 {
		return s
	}
	var result []byte
	for i, ch := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			result = append(result, ',')
		}
		result = append(result, byte(ch))
	}
	return string(result)
}
