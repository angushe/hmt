package parser

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// TestDedup_CacheOnlyRecordCountsAsUsage: a turn whose input was entirely cached
// has zero Input and Output but nonzero CacheRead. Classing it zero-usage would
// make it eligible for replacement by a later duplicate.
func TestDedup_CacheOnlyRecordCountsAsUsage(t *testing.T) {
	for name, r := range map[string]Record{
		"cache read only":  {DedupKey: "k", CacheReadTokens: 5000},
		"cache write only": {DedupKey: "k", CacheWriteTokens: 5000},
	} {
		t.Run(name, func(t *testing.T) {
			if !hasTokenUsage(r) {
				t.Errorf("hasTokenUsage(%+v) = false, want true", r)
			}
			// End to end: the placeholder must not overwrite it.
			got := Dedup([]Record{r, {DedupKey: "k"}})
			if len(got) != 1 || !hasTokenUsage(got[0]) {
				t.Errorf("Dedup kept %+v, want the record carrying usage", got)
			}
		})
	}
	if hasTokenUsage(Record{DedupKey: "k"}) {
		t.Error("hasTokenUsage of an all-zero record = true, want false")
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
		{"/Users/angus/basebit/project/nova/nova", "nova/nova"},
		{"/Users/angus/work/hmt", "work/hmt"},
		{"/Users/angus/project/hmt/.worktrees/codex-support", "hmt/codex-support"},
		{"/Users/angus/project/hmt/.worktrees/codex-support/internal/parser", "hmt/codex-support"},
		{"/Users/angus/project/nova/.worktrees/feat/license-system", "nova/feat"},
		{"/tmp", "tmp"},
		{"/", "/"},
	}
	for _, tt := range tests {
		got := ProjectName(tt.dir)
		if got != tt.want {
			t.Errorf("ProjectName(%q) = %q, want %q", tt.dir, got, tt.want)
		}
	}
}

func TestScanDir_UsesRecordedCwdAsProjectIdentity(t *testing.T) {
	tmp := t.TempDir()
	projDir := filepath.Join(tmp, "-Users-angus-project-nova-nova-data-import")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"assistant","cwd":"/Users/angus/project/nova/nova-data-import","sessionId":"s1","requestId":"r1","timestamp":"2026-04-20T10:00:00.000Z","message":{"id":"m1","model":"claude-opus-4-6","usage":{"input_tokens":100,"output_tokens":50}}}` + "\n"
	if err := os.WriteFile(filepath.Join(projDir, "session.jsonl"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	records, err := ScanDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("len = %d, want 1", len(records))
	}
	if records[0].ProjectDir != "/Users/angus/project/nova/nova-data-import" {
		t.Errorf("projectDir = %q, want recorded cwd", records[0].ProjectDir)
	}
}

func TestScanDir_UsesFirstRecordedCwdForWholeSessionFile(t *testing.T) {
	tmp := t.TempDir()
	projDir := filepath.Join(tmp, "-Users-angus-project-nova")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"type":"assistant","sessionId":"s1","requestId":"r1","timestamp":"2026-04-20T10:00:00.000Z","message":{"id":"m1","model":"claude-opus-4-6","usage":{"input_tokens":100,"output_tokens":50}}}`,
		`{"type":"assistant","cwd":"/Users/angus/project/nova","sessionId":"s1","requestId":"r2","timestamp":"2026-04-20T10:01:00.000Z","message":{"id":"m2","model":"claude-opus-4-6","usage":{"input_tokens":100,"output_tokens":50}}}`,
		`{"type":"assistant","cwd":"/Users/angus/project/nova/SPEC","sessionId":"s1","requestId":"r3","timestamp":"2026-04-20T10:02:00.000Z","message":{"id":"m3","model":"claude-opus-4-6","usage":{"input_tokens":100,"output_tokens":50}}}`,
	}
	if err := os.WriteFile(filepath.Join(projDir, "session.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	records, err := ScanDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 {
		t.Fatalf("len = %d, want 3", len(records))
	}
	for i, record := range records {
		if record.ProjectDir != "/Users/angus/project/nova" {
			t.Errorf("record %d projectDir = %q, want first recorded cwd", i, record.ProjectDir)
		}
	}
}

// TestScanDir_KeepsRecordsOnBothSidesOfAnOversizedLine pins the skip-and-continue
// reader shared with the Codex path. A bufio.Scanner stopped at the first line
// over its cap, so every assistant line after it was dropped — silently, since
// the warning named only the scan error.
func TestScanDir_KeepsRecordsOnBothSidesOfAnOversizedLine(t *testing.T) {
	tmp := t.TempDir()
	projDir := filepath.Join(tmp, "-Users-angus-project-test")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := func(id string, in int64) string {
		return fmt.Sprintf(`{"type":"assistant","cwd":"/Users/angus/project/test","sessionId":"s-%s","requestId":"r-%s","timestamp":"2026-04-20T10:00:00Z","message":{"id":"m-%s","model":"claude-opus-4-6","usage":{"input_tokens":%d,"output_tokens":50}}}`, id, id, id, in)
	}
	data := line("before", 100) + "\n" +
		strings.Repeat("x", 10*1024*1024+1) + "\n" +
		line("after", 200) + "\n"
	path := filepath.Join(projDir, "session.jsonl")
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	var (
		records []Record
		scanErr error
	)
	stderr := captureStderr(t, func() {
		records, scanErr = ScanDir(tmp)
	})
	if scanErr != nil {
		t.Fatal(scanErr)
	}
	if len(records) != 2 {
		t.Fatalf("records = %+v, want the usage on both sides of the oversized line", records)
	}
	if records[0].InputTokens != 100 || records[1].InputTokens != 200 {
		t.Errorf("input tokens = %d, %d; want 100, 200", records[0].InputTokens, records[1].InputTokens)
	}
	if !strings.Contains(stderr, "warning: skipping oversized line in "+path+" at line 2") {
		t.Errorf("stderr = %q, want the oversized line named", stderr)
	}
}

// TestScanDir_WarnsForInaccessibleProjectDirectory pins the last silent
// data-loss path: an unreadable project directory used to drop that project's
// entire usage with no warning and exit 0.
func TestScanDir_WarnsForInaccessibleProjectDirectory(t *testing.T) {
	tmp := t.TempDir()
	line := `{"type":"assistant","cwd":"/Users/angus/project/%s","sessionId":"s-%s","requestId":"r-%s","timestamp":"2026-04-20T10:00:00Z","message":{"id":"m-%s","model":"claude-opus-4-6","usage":{"input_tokens":100,"output_tokens":50}}}`
	for _, name := range []string{"good", "bad"} {
		dir := filepath.Join(tmp, "-Users-angus-project-"+name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		content := fmt.Sprintf(line, name, name, name, name)
		if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), []byte(content+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	badDir := filepath.Join(tmp, "-Users-angus-project-bad")
	if err := os.Chmod(badDir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(badDir, 0o755) })
	if _, err := os.ReadDir(badDir); err == nil {
		t.Skip("filesystem permissions do not make the fixture unreadable")
	}

	var (
		records []Record
		scanErr error
	)
	stderr := captureStderr(t, func() {
		records, scanErr = ScanDir(tmp)
	})
	if scanErr != nil {
		t.Fatal(scanErr)
	}
	if len(records) != 1 || records[0].SessionID != "s-good" {
		t.Fatalf("records = %+v, want the readable project's record only", records)
	}
	if !strings.Contains(stderr, "warning: unable to access "+badDir) {
		t.Errorf("stderr = %q, want inaccessible-project warning", stderr)
	}
}

// TestScanDir_WarnsForSymlinkedProjectDirectory pins the sibling case to the
// unreadable-directory warning: os.ReadDir reports a symlink as a non-directory,
// so a project reachable only through one used to vanish with no message.
func TestScanDir_WarnsForSymlinkedProjectDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	elsewhere := filepath.Join(tmp, "elsewhere")
	for _, dir := range []string{projects, elsewhere} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	line := `{"type":"assistant","cwd":"/Users/angus/project/linked","sessionId":"s-linked","requestId":"r-linked","timestamp":"2026-04-20T10:00:00Z","message":{"id":"m-linked","model":"claude-opus-4-6","usage":{"input_tokens":100,"output_tokens":50}}}`
	if err := os.WriteFile(filepath.Join(elsewhere, "session.jsonl"), []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(projects, "-Users-angus-project-linked")
	if err := os.Symlink(elsewhere, link); err != nil {
		t.Fatal(err)
	}

	var (
		records []Record
		scanErr error
	)
	stderr := captureStderr(t, func() {
		records, scanErr = ScanDir(projects)
	})
	if scanErr != nil {
		t.Fatal(scanErr)
	}
	// Skipped rather than followed, matching ScanCodexDir — but not silently.
	if len(records) != 0 {
		t.Fatalf("records = %+v, want none because symlinked projects are not followed", records)
	}
	if !strings.Contains(stderr, "warning: skipping symlinked directory "+link+" (not scanned)") {
		t.Errorf("stderr = %q, want symlinked-project warning", stderr)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, fmt.Errorf("forced read failure") }

// TestParseBufferedReader_KeepsRecordsParsedBeforeAReadError pins the ordering
// that keeps partial usage: base discarded every record when the read failed.
// Drives the production parse function through its injected reader — an earlier
// version of this test built its own loop and so pinned forEachLine instead,
// leaving both discard-everything mutants alive.
func TestParseBufferedReader_KeepsRecordsParsedBeforeAReadError(t *testing.T) {
	line := `{"type":"assistant","cwd":"/Users/angus/project/test","sessionId":"s1","requestId":"r1","timestamp":"2026-04-20T10:00:00Z","message":{"id":"m1","model":"claude-opus-4-6","usage":{"input_tokens":100,"output_tokens":50}}}` + "\n"
	reader := bufio.NewReaderSize(io.MultiReader(strings.NewReader(line), failingReader{}), maxLineBytes)

	var (
		records []Record
		readErr error
	)
	captureStderr(t, func() {
		records, readErr = parseBufferedReader(reader, "/session.jsonl", "fallback-proj")
	})
	if readErr == nil || !strings.Contains(readErr.Error(), "reading /session.jsonl: forced read failure") {
		t.Fatalf("error = %v, want a contextual read failure", readErr)
	}
	if len(records) != 1 || records[0].InputTokens != 100 {
		t.Fatalf("records = %+v, want the usage parsed before the failure", records)
	}
	// The identity pass must still have run over the partial slice.
	if records[0].ProjectDir != "/Users/angus/project/test" {
		t.Errorf("projectDir = %q, want the recorded cwd applied", records[0].ProjectDir)
	}
}

// TestScanDir_KeepsRecordsFromAFileEndingMidLine covers the end-to-end path for
// a file whose final line is unterminated and over the cap.
//
// Note on a gap this does NOT close: ScanDir appends before handling parseFile's
// error, so a file returning both records and an error keeps its partial usage.
// Gating that append leaves the suite green, and no portable fixture reaches it —
// producing (records != nil, err != nil) from a real file needs a mid-read I/O
// failure, which cannot be provoked through os.Open. The parse function itself is
// pinned by TestParseBufferedReader_KeepsRecordsParsedBeforeAReadError; the
// one-line ordering in ScanDir (and identically in ScanCodexDir) is not.
func TestScanDir_KeepsRecordsFromAFileEndingMidLine(t *testing.T) {
	tmp := t.TempDir()
	projDir := filepath.Join(tmp, "-Users-angus-project-test")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	good := `{"type":"assistant","cwd":"/Users/angus/project/test","sessionId":"s1","requestId":"r1","timestamp":"2026-04-20T10:00:00Z","message":{"id":"m1","model":"claude-opus-4-6","usage":{"input_tokens":100,"output_tokens":50}}}`
	data := good + "\n" + strings.Repeat("x", maxLineBytes+1)
	if err := os.WriteFile(filepath.Join(projDir, "session.jsonl"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	var records []Record
	captureStderr(t, func() {
		var err error
		records, err = ScanDir(tmp)
		if err != nil {
			t.Fatal(err)
		}
	})
	if len(records) != 1 || records[0].InputTokens != 100 {
		t.Fatalf("records = %+v, want the record parsed before the bad line", records)
	}
}

// TestForEachLine_DeliversAFinalLineWithoutNewline: a live session may have
// flushed the JSON object but not yet the "\n". Dropping that record is silent.
func TestForEachLine_DeliversAFinalLineWithoutNewline(t *testing.T) {
	reader := bufio.NewReaderSize(strings.NewReader("first\nlast-without-newline"), maxLineBytes)
	var got []string
	if err := forEachLine(reader, "/x.jsonl", func(data []byte, _ int) {
		got = append(got, strings.TrimRight(string(data), "\n"))
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1] != "last-without-newline" {
		t.Errorf("lines = %q, want the unterminated final line delivered", got)
	}
}

// TestForEachLine_WarningNamesTheReaderLimit: the limit reported is the reader's
// own size, not a constant that merely coincides with it at both call sites.
func TestForEachLine_WarningNamesTheReaderLimit(t *testing.T) {
	const small = 4096
	reader := bufio.NewReaderSize(strings.NewReader(strings.Repeat("x", small*2)+"\n"), small)
	stderr := captureStderr(t, func() {
		if err := forEachLine(reader, "/x.jsonl", func([]byte, int) {}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(stderr, "(limit 4096 bytes)") {
		t.Errorf("stderr = %q, want the reader's actual limit", stderr)
	}
}

func TestProjectName_ClaudeWorktrees(t *testing.T) {
	// Claude Code's own worktrees sit one level deeper than the .worktrees
	// convention, under .claude/worktrees. Without the extra case the repository
	// name is lost and two repos sharing a worktree name collapse into one row.
	for dir, want := range map[string]string{
		"/Users/a/code/enigma-xfs2/.claude/worktrees/test-refactor": "enigma-xfs2/test-refactor",
		"/Users/a/code/other/.claude/worktrees/test-refactor":       "other/test-refactor",
		"/Users/a/code/hmt/.worktrees/codex-support":                "hmt/codex-support",
		"/Users/a/code/hmt": "code/hmt",
	} {
		t.Run(dir, func(t *testing.T) {
			if got := ProjectName(dir); got != want {
				t.Errorf("ProjectName(%q) = %q, want %q", dir, got, want)
			}
		})
	}
}

func TestParseLine_WarnsOnInvalidTimestamp(t *testing.T) {
	line := `{"type":"assistant","sessionId":"s1","requestId":"r1","timestamp":"NOT-A-TIMESTAMP","message":{"id":"m1","model":"claude-opus-4-6","usage":{"input_tokens":1000000,"output_tokens":50}}}`
	var ok bool
	stderr := captureStderr(t, func() {
		_, ok = ParseLine([]byte(line))
	})
	if ok {
		t.Fatal("expected ok=false for an unparseable timestamp")
	}
	if !strings.Contains(stderr, `warning: skipping assistant line with invalid timestamp: "NOT-A-TIMESTAMP"`) {
		t.Errorf("stderr = %q, want invalid-timestamp warning naming the value", stderr)
	}
}

// Lines that are simply not assistant usage are dropped in bulk on every scan,
// so they must stay silent.
func TestParseLine_SilentForUninterestingLines(t *testing.T) {
	for name, line := range map[string]string{
		"user":      `{"type":"user","timestamp":"2026-04-20T10:00:00.000Z","message":{"role":"user"}}`,
		"malformed": `{bad json`,
		"synthetic": `{"type":"assistant","timestamp":"2026-04-20T10:00:00.000Z","message":{"id":"m1","model":"<synthetic>"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			stderr := captureStderr(t, func() { ParseLine([]byte(line)) })
			if stderr != "" {
				t.Errorf("stderr = %q, want silence", stderr)
			}
		})
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

func TestDedup_DedupKey(t *testing.T) {
	records := []Record{
		{DedupKey: "codex:s1:1:2:3:4:5:6", OutputTokens: 10},
		{DedupKey: "codex:s1:1:2:3:4:5:6", OutputTokens: 20},
		{DedupKey: "codex:s1:7:8:9:10:11:12", OutputTokens: 30},
	}
	deduped := Dedup(records)
	if len(deduped) != 2 {
		t.Fatalf("len = %d, want 2", len(deduped))
	}
	if deduped[0].OutputTokens != 10 {
		t.Errorf("first output = %d, want 10 (first-seen)", deduped[0].OutputTokens)
	}
}

func TestDedup_DedupKeyOverridesIDs(t *testing.T) {
	// MessageID/RequestID same but DedupKey differs: they are distinct records.
	records := []Record{
		{MessageID: "m1", RequestID: "r1", DedupKey: "codex:s1:a", OutputTokens: 1},
		{MessageID: "m1", RequestID: "r1", DedupKey: "codex:s1:b", OutputTokens: 2},
	}
	deduped := Dedup(records)
	if len(deduped) != 2 {
		t.Fatalf("len = %d, want 2", len(deduped))
	}
}

func TestDedup_CodexPrefersNonzeroUsageRegardlessOfTimestamp(t *testing.T) {
	earlier := time.Date(2026, 8, 4, 2, 40, 0, 0, time.UTC)
	later := earlier.Add(time.Minute)
	records := []Record{
		{DedupKey: "codex:root:1:2:3", Timestamp: earlier},
		{DedupKey: "codex:root:1:2:3", Timestamp: later, InputTokens: 200, OutputTokens: 20},
	}

	deduped := Dedup(records)
	if len(deduped) != 1 {
		t.Fatalf("len = %d, want 1", len(deduped))
	}
	if deduped[0].InputTokens != 200 || deduped[0].OutputTokens != 20 {
		t.Errorf("usage = %d/%d, want nonzero record 200/20", deduped[0].InputTokens, deduped[0].OutputTokens)
	}
}
