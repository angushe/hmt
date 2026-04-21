package parser

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseLine_AssistantWithUsage(t *testing.T) {
	line := `{"type":"assistant","sessionId":"sess-1","requestId":"req-1","parentUuid":"p-1","timestamp":"2026-04-20T10:00:00.000Z","message":{"id":"msg-1","model":"claude-opus-4-6","usage":{"input_tokens":100,"output_tokens":200,"cache_creation_input_tokens":300,"cache_read_input_tokens":400}}}`
	rec, ok := ParseLine([]byte(line))
	if !ok {
		t.Fatal("expected ok=true for assistant line")
	}
	if rec.Model != "claude-opus-4-6" {
		t.Errorf("model = %q, want claude-opus-4-6", rec.Model)
	}
	if rec.InputTokens != 100 {
		t.Errorf("input_tokens = %d, want 100", rec.InputTokens)
	}
	if rec.OutputTokens != 200 {
		t.Errorf("output_tokens = %d, want 200", rec.OutputTokens)
	}
	if rec.CacheWriteTokens != 300 {
		t.Errorf("cache_write = %d, want 300", rec.CacheWriteTokens)
	}
	if rec.CacheReadTokens != 400 {
		t.Errorf("cache_read = %d, want 400", rec.CacheReadTokens)
	}
	if rec.SessionID != "sess-1" {
		t.Errorf("sessionId = %q, want sess-1", rec.SessionID)
	}
	if rec.RequestID != "req-1" {
		t.Errorf("requestId = %q, want req-1", rec.RequestID)
	}
	if rec.MessageID != "msg-1" {
		t.Errorf("messageId = %q, want msg-1", rec.MessageID)
	}
	expectedTime := time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)
	if !rec.Timestamp.Equal(expectedTime) {
		t.Errorf("timestamp = %v, want %v", rec.Timestamp, expectedTime)
	}
}

func TestParseLine_UserLine(t *testing.T) {
	line := `{"type":"user","sessionId":"sess-1","timestamp":"2026-04-20T10:00:00.000Z","message":{"role":"user","content":"hello"}}`
	_, ok := ParseLine([]byte(line))
	if ok {
		t.Fatal("expected ok=false for user line")
	}
}

func TestParseLine_MalformedJSON(t *testing.T) {
	_, ok := ParseLine([]byte(`{bad json`))
	if ok {
		t.Fatal("expected ok=false for malformed JSON")
	}
}

func TestParseLine_FileHistorySnapshot(t *testing.T) {
	line := `{"type":"file-history-snapshot","messageId":"abc","snapshot":{}}`
	_, ok := ParseLine([]byte(line))
	if ok {
		t.Fatal("expected ok=false for file-history-snapshot")
	}
}

func TestDedup(t *testing.T) {
	// First-seen strategy: keep the first occurrence, discard later duplicates
	records := []Record{
		{MessageID: "msg-1", RequestID: "req-1", OutputTokens: 33, InputTokens: 3, CacheWriteTokens: 100, CacheReadTokens: 50},
		{MessageID: "msg-1", RequestID: "req-1", OutputTokens: 33, InputTokens: 3, CacheWriteTokens: 100, CacheReadTokens: 50},
		{MessageID: "msg-1", RequestID: "req-1", OutputTokens: 162, InputTokens: 3, CacheWriteTokens: 100, CacheReadTokens: 50},
		{MessageID: "msg-2", RequestID: "req-2", OutputTokens: 42, InputTokens: 5, CacheWriteTokens: 200, CacheReadTokens: 80},
		{MessageID: "msg-2", RequestID: "req-2", OutputTokens: 311, InputTokens: 5, CacheWriteTokens: 200, CacheReadTokens: 80},
	}
	deduped := Dedup(records)
	if len(deduped) != 2 {
		t.Fatalf("len = %d, want 2", len(deduped))
	}
	// First-seen: keeps first chunk (output=33), not last (output=162)
	if deduped[0].OutputTokens != 33 {
		t.Errorf("first output_tokens = %d, want 33", deduped[0].OutputTokens)
	}
	if deduped[1].OutputTokens != 42 {
		t.Errorf("second output_tokens = %d, want 42", deduped[1].OutputTokens)
	}
}

func TestParseLine_Synthetic(t *testing.T) {
	line := `{"type":"assistant","sessionId":"s1","requestId":"r1","timestamp":"2026-04-20T10:00:00.000Z","message":{"id":"msg-1","model":"<synthetic>","usage":{"input_tokens":0,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`
	_, ok := ParseLine([]byte(line))
	if ok {
		t.Fatal("expected ok=false for <synthetic> model")
	}
}

func TestDedup_EmptyRequestID(t *testing.T) {
	records := []Record{
		{RequestID: "", OutputTokens: 10},
		{RequestID: "", OutputTokens: 20},
	}
	deduped := Dedup(records)
	if len(deduped) != 2 {
		t.Fatalf("len = %d, want 2", len(deduped))
	}
}

func TestProjectName(t *testing.T) {
	tests := []struct {
		dir  string
		want string
	}{
		{"-Users-angus-basebit-project-nova-nova", "nova/nova"},
		{"-Users-angus-project-hmt", "project/hmt"},
		{"-Users-angus", "angus"},
		{"single", "single"},
	}
	for _, tt := range tests {
		got := ProjectName(tt.dir)
		if got != tt.want {
			t.Errorf("ProjectName(%q) = %q, want %q", tt.dir, got, tt.want)
		}
	}
}

func TestScanDir(t *testing.T) {
	tmp := t.TempDir()
	projDir := filepath.Join(tmp, "-Users-angus-project-test")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}

	lines := []string{
		`{"type":"assistant","sessionId":"s1","requestId":"r1","timestamp":"2026-04-20T10:00:00.000Z","message":{"id":"m1","model":"claude-opus-4-6","usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":200,"cache_read_input_tokens":300}}}`,
		`{"type":"assistant","sessionId":"s1","requestId":"r1","timestamp":"2026-04-20T10:00:01.000Z","message":{"id":"m1","model":"claude-opus-4-6","usage":{"input_tokens":100,"output_tokens":150,"cache_creation_input_tokens":200,"cache_read_input_tokens":300}}}`,
		`{"type":"user","sessionId":"s1","timestamp":"2026-04-20T10:00:02.000Z","message":{"role":"user","content":"hi"}}`,
		`{"type":"assistant","sessionId":"s1","requestId":"r2","timestamp":"2026-04-20T10:01:00.000Z","message":{"id":"m2","model":"claude-haiku-4-5","usage":{"input_tokens":50,"output_tokens":80,"cache_creation_input_tokens":0,"cache_read_input_tokens":100}}}`,
	}
	data := ""
	for _, l := range lines {
		data += l + "\n"
	}
	if err := os.WriteFile(filepath.Join(projDir, "session1.jsonl"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	records, err := ScanDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("len = %d, want 2", len(records))
	}
	// First-seen dedup: keeps first streaming chunk (output=50), not last (output=150)
	if records[0].OutputTokens != 50 {
		t.Errorf("first output = %d, want 50", records[0].OutputTokens)
	}
	if records[0].ProjectDir != "-Users-angus-project-test" {
		t.Errorf("projectDir = %q, want -Users-angus-project-test", records[0].ProjectDir)
	}
	if records[1].Model != "claude-haiku-4-5" {
		t.Errorf("second model = %q, want claude-haiku-4-5", records[1].Model)
	}
}

func TestScanDir_Subagents(t *testing.T) {
	tmp := t.TempDir()
	projDir := filepath.Join(tmp, "-Users-angus-project-test")

	// Top-level session JSONL
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	topLine := `{"type":"assistant","sessionId":"s1","requestId":"r1","timestamp":"2026-04-20T10:00:00.000Z","message":{"id":"m1","model":"claude-opus-4-6","usage":{"input_tokens":100,"output_tokens":200,"cache_creation_input_tokens":300,"cache_read_input_tokens":400}}}` + "\n"
	if err := os.WriteFile(filepath.Join(projDir, "session.jsonl"), []byte(topLine), 0o644); err != nil {
		t.Fatal(err)
	}

	// Nested subagent JSONL
	subDir := filepath.Join(projDir, "abc-session-uuid", "subagents")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	subLine := `{"type":"assistant","sessionId":"s2","requestId":"r2","timestamp":"2026-04-20T11:00:00.000Z","message":{"id":"m2","model":"claude-haiku-4-5","usage":{"input_tokens":50,"output_tokens":80,"cache_creation_input_tokens":0,"cache_read_input_tokens":100}}}` + "\n"
	if err := os.WriteFile(filepath.Join(subDir, "agent-xxx.jsonl"), []byte(subLine), 0o644); err != nil {
		t.Fatal(err)
	}

	records, err := ScanDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	// Should find both top-level and subagent records
	if len(records) != 2 {
		t.Fatalf("len = %d, want 2 (top-level + subagent)", len(records))
	}
}
