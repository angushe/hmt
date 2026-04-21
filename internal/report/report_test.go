package report

import (
	"testing"
	"time"

	"github.com/angus/hmt/internal/parser"
	"github.com/angus/hmt/internal/pricing"
)

func makeRecords() []parser.Record {
	day1 := time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 4, 21, 14, 0, 0, 0, time.UTC)
	return []parser.Record{
		{Model: "claude-opus-4-6", SessionID: "s1", Timestamp: day1, ProjectDir: "-Users-angus-project-foo", InputTokens: 100, OutputTokens: 50, CacheWriteTokens: 200, CacheReadTokens: 300},
		{Model: "claude-opus-4-6", SessionID: "s1", Timestamp: day1, ProjectDir: "-Users-angus-project-foo", InputTokens: 200, OutputTokens: 100, CacheWriteTokens: 400, CacheReadTokens: 600},
		{Model: "claude-haiku-4-5", SessionID: "s2", Timestamp: day2, ProjectDir: "-Users-angus-project-bar", InputTokens: 50, OutputTokens: 80, CacheWriteTokens: 0, CacheReadTokens: 100},
	}
}

func TestAggregateByDay(t *testing.T) {
	rows := Aggregate(makeRecords(), ByDay)
	if len(rows) != 2 {
		t.Fatalf("len = %d, want 2", len(rows))
	}
	// Sorted descending by key, so 2026-04-21 first
	if rows[0].Key != "2026-04-21" {
		t.Errorf("row0 key = %q, want 2026-04-21", rows[0].Key)
	}
	if rows[1].Key != "2026-04-20" {
		t.Errorf("row1 key = %q, want 2026-04-20", rows[1].Key)
	}
	if rows[1].Model != "claude-opus-4-6" {
		t.Errorf("row1 model = %q, want claude-opus-4-6", rows[1].Model)
	}
	if rows[1].InputTokens != 300 {
		t.Errorf("row1 input = %d, want 300", rows[1].InputTokens)
	}
}

func TestAggregateBySession(t *testing.T) {
	rows := Aggregate(makeRecords(), BySession)
	if len(rows) != 2 {
		t.Fatalf("len = %d, want 2", len(rows))
	}
}

func TestAggregateByProject(t *testing.T) {
	rows := Aggregate(makeRecords(), ByProject)
	if len(rows) != 2 {
		t.Fatalf("len = %d, want 2", len(rows))
	}
}

func TestFilter_Since(t *testing.T) {
	since := time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)
	filtered := Filter(makeRecords(), &since, nil, "", "")
	if len(filtered) != 1 {
		t.Fatalf("len = %d, want 1", len(filtered))
	}
	if filtered[0].Model != "claude-haiku-4-5" {
		t.Errorf("model = %q, want claude-haiku-4-5", filtered[0].Model)
	}
}

func TestFilter_Model(t *testing.T) {
	filtered := Filter(makeRecords(), nil, nil, "claude-haiku-4-5", "")
	if len(filtered) != 1 {
		t.Fatalf("len = %d, want 1", len(filtered))
	}
}

func TestFilter_Project(t *testing.T) {
	filtered := Filter(makeRecords(), nil, nil, "", "foo")
	if len(filtered) != 2 {
		t.Fatalf("len = %d, want 2", len(filtered))
	}
}

func TestComputeCosts(t *testing.T) {
	table, _ := pricing.LoadFromJSON([]byte(`{
		"claude-opus-4-6": {"input_cost_per_token":5e-06,"output_cost_per_token":2.5e-05,"cache_creation_input_token_cost":6.25e-06,"cache_read_input_token_cost":5e-07}
	}`))
	rows := []Row{
		{Model: "claude-opus-4-6", InputTokens: 1000000, OutputTokens: 100000, CacheWriteTokens: 500000, CacheReadTokens: 2000000},
	}
	ComputeCosts(rows, table)
	if !rows[0].HasCost {
		t.Fatal("expected HasCost=true")
	}
	expected := 11.625
	if rows[0].Cost < expected-0.001 || rows[0].Cost > expected+0.001 {
		t.Errorf("cost = %f, want %f", rows[0].Cost, expected)
	}
}
