package report

import (
	"math"
	"sort"
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
