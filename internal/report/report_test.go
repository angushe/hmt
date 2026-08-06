package report

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/angushe/hmt/internal/parser"
	"github.com/angushe/hmt/internal/pricing"
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
	rows := Aggregate(makeRecords(), ByDay, time.UTC)
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
	rows := Aggregate(makeRecords(), BySession, nil)
	if len(rows) != 2 {
		t.Fatalf("len = %d, want 2", len(rows))
	}
}

func TestAggregateByProject(t *testing.T) {
	rows := Aggregate(makeRecords(), ByProject, nil)
	if len(rows) != 2 {
		t.Fatalf("len = %d, want 2", len(rows))
	}
}

func TestAggregateByWeek(t *testing.T) {
	rows := Aggregate(makeRecords(), ByWeek, time.UTC)
	// Both dates (Apr 20 Sun and Apr 21 Mon) are in different ISO weeks
	// Apr 20 2026 is Sunday → ISO week 16, Apr 21 is Monday → ISO week 17
	if len(rows) != 2 {
		t.Fatalf("len = %d, want 2", len(rows))
	}
	if rows[0].Key != "2026-W17" {
		t.Errorf("row0 key = %q, want 2026-W17", rows[0].Key)
	}
}

func TestAggregateByMonth(t *testing.T) {
	rows := Aggregate(makeRecords(), ByMonth, time.UTC)
	// All records are in April 2026
	if len(rows) != 2 {
		t.Fatalf("len = %d, want 2 (two models)", len(rows))
	}
	if rows[0].Key != "2026-04" {
		t.Errorf("row0 key = %q, want 2026-04", rows[0].Key)
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

func TestFilter_DateBoundsAreSinceInclusiveUntilExclusive(t *testing.T) {
	since := time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC)
	records := []parser.Record{
		{SessionID: "before", Timestamp: since.Add(-time.Nanosecond)},
		{SessionID: "at-since", Timestamp: since},
		{SessionID: "before-until", Timestamp: until.Add(-time.Nanosecond)},
		{SessionID: "at-until", Timestamp: until},
	}
	filtered := Filter(records, &since, &until, "", "")
	if len(filtered) != 2 || filtered[0].SessionID != "at-since" || filtered[1].SessionID != "before-until" {
		t.Fatalf("filtered = %+v, want since-inclusive and until-exclusive records", filtered)
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

func TestFilter_ProjectMatchesDisplayedWorktreeName(t *testing.T) {
	records := []parser.Record{
		{ProjectDir: "/Users/angus/project/hmt/.worktrees/codex-support"},
		{ProjectDir: "/Users/angus/project/other"},
	}
	filtered := Filter(records, nil, nil, "", "hmt/codex-support")
	if len(filtered) != 1 {
		t.Fatalf("len = %d, want 1", len(filtered))
	}
	if filtered[0].ProjectDir != records[0].ProjectDir {
		t.Errorf("projectDir = %q, want %q", filtered[0].ProjectDir, records[0].ProjectDir)
	}
}

func TestFilter_ProjectStillMatchesRawIdentifier(t *testing.T) {
	records := []parser.Record{{ProjectDir: "/Users/angus/project/hmt/.worktrees/codex-support"}}
	filtered := Filter(records, nil, nil, "", ".worktrees/codex")
	if len(filtered) != 1 {
		t.Fatalf("len = %d, want raw project identifier match", len(filtered))
	}
}

func TestFilter_ProjectMatchesLegacyDashEncodedIdentifier(t *testing.T) {
	records := []parser.Record{{ProjectDir: "/Users/angus/basebit/project/nova/nova"}}
	filtered := Filter(records, nil, nil, "", "project-nova")
	if len(filtered) != 1 {
		t.Fatalf("len = %d, want legacy dash-encoded project match", len(filtered))
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

// TestComputeCosts_ZeroesCostOnALookupMiss: HasCost gates the chart's headline
// while bucketize and assignColors read Cost ungated, so a stale value would be
// two predicates over one field.
func TestComputeCosts_ZeroesCostOnALookupMiss(t *testing.T) {
	table, err := pricing.LoadFromJSON([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	rows := []Row{{Model: "unpriced", Cost: 42}}
	ComputeCosts(rows, table)
	if rows[0].HasCost {
		t.Fatal("unknown model unexpectedly has a cost")
	}
	if rows[0].Cost != 0 {
		t.Errorf("cost = %v, want 0 — a stale value survives into ungated readers", rows[0].Cost)
	}
}

func sampleRows() []Row {
	return []Row{
		{Key: "2026-04-20", Model: "claude-opus-4-6", InputTokens: 1000, OutputTokens: 500, CacheWriteTokens: 200, CacheReadTokens: 300, Cost: 0.021, HasCost: true},
		{Key: "2026-04-19", Model: "claude-haiku-4-5", InputTokens: 2000, OutputTokens: 800, CacheWriteTokens: 0, CacheReadTokens: 100, Cost: 0.006, HasCost: true},
	}
}

func TestFormatTable(t *testing.T) {
	var buf bytes.Buffer
	FormatTable(&buf, sampleRows(), "day")
	out := buf.String()
	if !strings.Contains(out, "2026-04-20") {
		t.Errorf("table output missing date:\n%s", out)
	}
	if !strings.Contains(out, "claude-opus-4-6") {
		t.Errorf("table output missing model:\n%s", out)
	}
	if !strings.Contains(out, "$0.02") {
		t.Errorf("table output missing cost:\n%s", out)
	}
}

func TestFormatTable_MarksUnpricedModelsAndIncompleteTotal(t *testing.T) {
	table, err := pricing.LoadFromJSON([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	rows := []Row{{Key: "2026-04-20", Model: "codex-auto-review", InputTokens: 1000}}
	ComputeCosts(rows, table)
	if rows[0].HasCost {
		t.Fatal("unknown model unexpectedly has a cost")
	}

	var buf bytes.Buffer
	FormatTable(&buf, rows, "day")
	out := buf.String()
	if !strings.Contains(out, "N/A") {
		t.Errorf("table output missing unpriced marker:\n%s", out)
	}
	if !strings.Contains(out, "$0.00*") {
		t.Errorf("table footer missing incomplete-total marker:\n%s", out)
	}
}

func TestFormatJSON(t *testing.T) {
	var buf bytes.Buffer
	rows := append(sampleRows(), Row{Key: "2026-04-18", Model: "codex-auto-review", InputTokens: 3000})
	FormatJSON(&buf, rows, "day")
	var parsed []map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	if len(parsed) != 3 {
		t.Fatalf("len = %d, want 3", len(parsed))
	}
	if got := parsed[0]["has_cost"]; got != true {
		t.Errorf("priced has_cost = %v, want true", got)
	}
	if got := parsed[2]["has_cost"]; got != false {
		t.Errorf("unpriced has_cost = %v, want false", got)
	}
	if _, ok := parsed[2]["cost"]; ok {
		t.Errorf("unpriced row unexpectedly contains cost: %#v", parsed[2])
	}
}

// TestFormatTable_RightAlignsTheFooter pins AlignFooter. go-pretty aligns the
// footer independently of the body, so without it a narrow total sits flush left
// under right-aligned digits — visible only when a total is much shorter than
// its column, which is exactly what Codex's always-zero Cache Write produced.
func TestFormatTable_RightAlignsTheFooter(t *testing.T) {
	var buf bytes.Buffer
	FormatTable(&buf, []Row{
		{Key: "2026-04-25", Model: "alpha", InputTokens: 1234567, OutputTokens: 850, CacheWriteTokens: 0, CacheReadTokens: 400, Cost: 1, HasCost: true},
	}, "day")

	var footer string
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, "TOTAL") {
			footer = line
			break
		}
	}
	if footer == "" {
		t.Fatalf("no footer row:\n%s", buf.String())
	}
	// The decisive cells are the ones far narrower than their column: padding
	// must precede the digits, not follow them.
	for _, tc := range []struct{ aligned, misaligned string }{
		{"           0 ", " 0           "}, // Cache Write, under an 11-wide header
		{"        400 ", " 400        "},   // Cache Read, under a 10-wide header
	} {
		if strings.Contains(footer, tc.misaligned) {
			t.Errorf("footer cell is left-aligned (%q):\n%s", tc.misaligned, footer)
		}
		if !strings.Contains(footer, tc.aligned) {
			t.Errorf("footer cell is not right-aligned (want %q):\n%s", tc.aligned, footer)
		}
	}
}

func TestFormatInt_GroupsDigitsAroundTheSign(t *testing.T) {
	// The (len(s)-i)%3 test counted the "-" as a digit, so formatInt(-300)
	// rendered "-,300".
	for n, want := range map[int64]string{
		-300:     "-300",
		-1234:    "-1,234",
		-1234567: "-1,234,567",
		0:        "0",
		300:      "300",
		1234567:  "1,234,567",
	} {
		if got := formatInt(n); got != want {
			t.Errorf("formatInt(%d) = %q, want %q", n, got, want)
		}
	}
}

// TestFilter_ProjectMatchesDotEncodedLegacyName pins the inverse of Claude
// Code's directory encoding. It maps BOTH "/" and "." to "-", so reconstructing
// with only "/" produces "-.worktrees-" — a spelling no directory has — and
// --project silently returned nothing for every worktree path.
func TestFilter_ProjectMatchesDotEncodedLegacyName(t *testing.T) {
	records := []parser.Record{
		{ProjectDir: "/Users/a/programming/project/hmt/.worktrees/codex-support", InputTokens: 1},
		{ProjectDir: "/Users/a/programming/project/other", InputTokens: 2},
	}
	for name, tc := range map[string]struct {
		project string
		wantIn  int64
	}{
		// The name as it actually exists in ~/.claude/projects.
		"dot-encoded legacy name": {"-Users-a-programming-project-hmt--worktrees-codex-support", 1},
		"dot-free legacy name":    {"-Users-a-programming-project-other", 2},
		"raw path":                {"/Users/a/programming/project/hmt/.worktrees", 1},
		"rendered project name":   {"hmt/codex-support", 1},
	} {
		t.Run(name, func(t *testing.T) {
			got := Filter(records, nil, nil, "", tc.project)
			if len(got) != 1 || got[0].InputTokens != tc.wantIn {
				t.Fatalf("Filter(%q) = %+v, want the record with input %d", tc.project, got, tc.wantIn)
			}
		})
	}
}

// TestFormatJSON_EmitsZeroCostForPricedRows pins what omission means: a row
// whose price is known to be $0.00 must still carry cost, or it claims
// has_cost:true while showing no cost at all. CSV already writes 0.000000 there.
func TestFormatJSON_EmitsZeroCostForPricedRows(t *testing.T) {
	var buf bytes.Buffer
	FormatJSON(&buf, []Row{
		{Key: "2026-04-18", Model: "free-model", InputTokens: 100, Cost: 0, HasCost: true},
		{Key: "2026-04-18", Model: "unknown-model", InputTokens: 100, HasCost: false},
	}, "day")

	var parsed []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	cost, ok := parsed[0]["cost"]
	if !ok {
		t.Errorf("priced row omits cost despite has_cost=true: %#v", parsed[0])
	} else if cost != float64(0) {
		t.Errorf("cost = %v, want 0", cost)
	}
	// Unpriced stays absent: there is no cost to report, which is a different
	// fact from a cost of zero.
	if _, ok := parsed[1]["cost"]; ok {
		t.Errorf("unpriced row unexpectedly contains cost: %#v", parsed[1])
	}
}

func TestFormatCSV(t *testing.T) {
	var buf bytes.Buffer
	rows := append(sampleRows(), Row{Key: "2026-04-18", Model: "codex-auto-review", InputTokens: 3000})
	FormatCSV(&buf, rows, "day")
	records, err := csv.NewReader(strings.NewReader(buf.String())).ReadAll()
	if err != nil {
		t.Fatalf("invalid CSV: %v\n%s", err, buf.String())
	}
	if len(records) != 4 {
		t.Fatalf("records = %d, want 4", len(records))
	}
	if got := records[0]; len(got) != 8 || got[0] != "day" || got[7] != "has_cost" {
		t.Fatalf("header = %#v, want day through has_cost", got)
	}
	if got := records[1][7]; got != "true" {
		t.Errorf("priced has_cost = %q, want true", got)
	}
	if got := records[3][7]; got != "false" {
		t.Errorf("unpriced has_cost = %q, want false", got)
	}
}
