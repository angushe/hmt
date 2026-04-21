package parser

import (
	"testing"
	"time"
)

func TestParseLine_AssistantWithUsage(t *testing.T) {
	line := `{"type":"assistant","sessionId":"sess-1","requestId":"req-1","parentUuid":"p-1","timestamp":"2026-04-20T10:00:00.000Z","message":{"model":"claude-opus-4-6","usage":{"input_tokens":100,"output_tokens":200,"cache_creation_input_tokens":300,"cache_read_input_tokens":400}}}`
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
