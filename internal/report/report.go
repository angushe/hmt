package report

import (
	"sort"
	"strings"
	"time"

	"github.com/angus/hmt/internal/parser"
	"github.com/angus/hmt/internal/pricing"
)

// GroupBy specifies the aggregation dimension.
type GroupBy int

const (
	ByDay GroupBy = iota
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
func Aggregate(records []parser.Record, by GroupBy) []Row {
	type aggKey struct {
		key   string
		model string
	}
	sums := make(map[aggKey]*Row)
	var order []aggKey

	for _, r := range records {
		var k string
		switch by {
		case ByDay:
			k = r.Timestamp.Format("2006-01-02")
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
