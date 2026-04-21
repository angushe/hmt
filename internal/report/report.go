package report

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/jedib0t/go-pretty/v6/text"

	"github.com/angushe/hmt/internal/parser"
	"github.com/angushe/hmt/internal/pricing"
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

// FormatTable writes a styled ASCII table to w with group separators.
func FormatTable(w io.Writer, rows []Row, keyName string) {
	t := table.NewWriter()
	t.SetOutputMirror(w)
	t.AppendHeader(table.Row{keyName, "Model", "Input", "Output", "Cache Write", "Cache Read", "Cost"})

	t.SetColumnConfigs([]table.ColumnConfig{
		{Number: 1, Align: text.AlignLeft},
		{Number: 2, Align: text.AlignLeft, Colors: text.Colors{text.FgHiBlack}},
		{Number: 3, Align: text.AlignRight},
		{Number: 4, Align: text.AlignRight},
		{Number: 5, Align: text.AlignRight},
		{Number: 6, Align: text.AlignRight},
		{Number: 7, Align: text.AlignRight, Colors: text.Colors{text.FgGreen}, ColorsFooter: text.Colors{text.Bold}},
	})

	t.SetStyle(table.StyleLight)

	var totalIn, totalOut, totalCW, totalCR int64
	var totalCost float64
	allHaveCost := true

	mergeKey := keyName == "day" || keyName == "week" || keyName == "month"
	prevKey := ""

	for _, r := range rows {
		displayKey := r.Key
		if mergeKey && r.Key == prevKey {
			displayKey = ""
		}
		prevKey = r.Key

		cost := "N/A"
		if r.HasCost {
			cost = fmt.Sprintf("$%.2f", r.Cost)
			totalCost += r.Cost
		} else {
			allHaveCost = false
		}
		t.AppendRow(table.Row{
			displayKey,
			r.Model,
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
	t.AppendFooter(table.Row{"Total", "", formatInt(totalIn), formatInt(totalOut), formatInt(totalCW), formatInt(totalCR), costStr})
	t.Render()
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
