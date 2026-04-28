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
