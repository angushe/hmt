package report

import (
	"math"
	"sort"
)

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
