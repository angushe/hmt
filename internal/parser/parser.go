package parser

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Record holds the extracted fields from one deduplicated assistant message.
type Record struct {
	Model            string
	SessionID        string
	RequestID        string
	MessageID        string // API response message ID, used for dedup
	DedupKey         string // pre-computed dedup key; overrides MessageID/RequestID when set
	Timestamp        time.Time
	InputTokens      int64
	OutputTokens     int64
	CacheWriteTokens int64
	CacheReadTokens  int64
	ProjectDir       string // raw directory name, set by caller
}

// jsonLine is the subset of JSONL fields we care about.
type jsonLine struct {
	Type       string `json:"type"`
	Cwd        string `json:"cwd"`
	SessionID  string `json:"sessionId"`
	RequestID  string `json:"requestId"`
	ParentUUID string `json:"parentUuid"`
	Timestamp  string `json:"timestamp"`
	Message    struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage struct {
			InputTokens      int64 `json:"input_tokens"`
			OutputTokens     int64 `json:"output_tokens"`
			CacheWriteTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadTokens  int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// Dedup removes duplicates using a first-seen strategy.
// The dedup key is DedupKey when set (Codex records), otherwise
// "messageId:requestId" (Claude records). A nonzero Codex duplicate may fill
// an earlier zero-usage placeholder; all other duplicates are discarded.
// Records without a key are kept as-is.
func Dedup(records []Record) []Record {
	seen := make(map[string]int)
	result := make([]Record, 0, len(records))
	for _, r := range records {
		hash := dedupHash(r)
		if hash == "" {
			result = append(result, r)
			continue
		}
		if index, ok := seen[hash]; ok {
			// A deliberate hedge that has never fired: replayed 589 real rollout
			// files / 54,965 records and the zero-usage synthetic fills always
			// collide *after* the real record, where plain first-seen already
			// keeps the right one. Kept because the ordering that would invert
			// it (a fill timestamped ahead of its request) is not ruled out by
			// anything in the format.
			if r.DedupKey != "" && !hasTokenUsage(result[index]) && hasTokenUsage(r) {
				result[index] = r
			}
			continue
		}
		seen[hash] = len(result)
		result = append(result, r)
	}
	return result
}

func hasTokenUsage(r Record) bool {
	return r.InputTokens != 0 || r.OutputTokens != 0 || r.CacheWriteTokens != 0 || r.CacheReadTokens != 0
}

func dedupHash(r Record) string {
	if r.DedupKey != "" {
		return r.DedupKey
	}
	if r.MessageID == "" || r.RequestID == "" {
		return ""
	}
	return r.MessageID + ":" + r.RequestID
}

// ProjectName derives a short display name from an absolute cwd or, for older
// Claude records without cwd, a best-effort dash-encoded directory name.
// Worktrees retain repository identity ("hmt/.worktrees/branch" ->
// "hmt/branch") and treat the first segment after .worktrees as the worktree
// name. A slash in a branch-style worktree name is indistinguishable from a
// subdirectory and therefore collapses to that first segment. Other paths use
// their last two segments when there are 3+.
func ProjectName(dir string) string {
	var parts []string
	if strings.HasPrefix(dir, "/") {
		for _, p := range strings.Split(dir, "/") {
			if p != "" {
				parts = append(parts, p)
			}
		}
	} else {
		parts = splitDirName(dir)
	}
	for i := len(parts) - 2; i > 0; i-- {
		if parts[i] == ".worktrees" {
			return parts[i-1] + "/" + parts[i+1]
		}
		// Claude Code's own worktrees live under .claude/worktrees, one level
		// deeper. Without this the repository name is lost and two repos with a
		// same-named worktree collapse into one row.
		if parts[i] == "worktrees" && parts[i-1] == ".claude" && i >= 2 {
			return parts[i-2] + "/" + parts[i+1]
		}
	}
	switch {
	case len(parts) >= 3:
		return parts[len(parts)-2] + "/" + parts[len(parts)-1]
	case len(parts) >= 1:
		return parts[len(parts)-1]
	default:
		return dir
	}
}

func splitDirName(dir string) []string {
	var parts []string
	current := ""
	for _, ch := range dir {
		if ch == '-' {
			if current != "" {
				parts = append(parts, current)
			}
			current = ""
		} else {
			current += string(ch)
		}
	}
	if current != "" {
		parts = append(parts, current)
	}
	return parts
}

// maxLineBytes caps a single JSONL line for both parsers. Lines longer than
// this are skipped rather than truncating the rest of the file.
const maxLineBytes = 10 * 1024 * 1024

// forEachLine calls fn for each newline-delimited line in reader. A line longer
// than the reader's buffer is skipped with a warning and reading continues past
// it — a truncating scanner would instead drop every remaining line in the file,
// silently, which is the failure this replaced on the Claude side.
func forEachLine(reader *bufio.Reader, path string, fn func(line []byte, lineNumber int)) error {
	for lineNumber := 1; ; lineNumber++ {
		data, readErr := reader.ReadSlice('\n')
		if errors.Is(readErr, bufio.ErrBufferFull) {
			fmt.Fprintf(os.Stderr, "warning: skipping oversized line in %s at line %d (limit %d bytes)\n",
				path, lineNumber, reader.Size())
			for errors.Is(readErr, bufio.ErrBufferFull) {
				_, readErr = reader.ReadSlice('\n')
			}
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return fmt.Errorf("reading %s: %w", path, readErr)
			}
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			continue
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return fmt.Errorf("reading %s: %w", path, readErr)
		}
		if len(data) == 0 {
			return nil
		}
		fn(data, lineNumber)
	}
}

// ScanDir recursively reads all .jsonl files under baseDir/*/,
// including subagent logs in nested directories.
// It parses assistant lines, deduplicates, and returns records.
func ScanDir(baseDir string) ([]Record, error) {
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", baseDir, err)
	}

	var allRecords []Record
	// One reader for the whole scan, as ScanCodexDir does: a fresh 10 MiB buffer
	// per file is pure allocation churn across a large corpus.
	reader := bufio.NewReaderSize(bytes.NewReader(nil), maxLineBytes)
	for _, entry := range entries {
		if !entry.IsDir() {
			// os.ReadDir reports a symlink as a non-directory, so a project
			// reachable only through one would vanish silently. Skipped rather
			// than followed, matching ScanCodexDir's cycle-avoidance choice.
			if entry.Type()&os.ModeSymlink != 0 {
				path := filepath.Join(baseDir, entry.Name())
				if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
					fmt.Fprintf(os.Stderr, "warning: skipping symlinked directory %s (not scanned)\n", path)
				}
			}
			continue
		}
		projName := entry.Name()
		projPath := filepath.Join(baseDir, projName)
		// The callback reports and swallows every per-path error, so the walk
		// itself cannot fail.
		_ = filepath.WalkDir(projPath, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				// Skipping silently would drop a whole project's usage from the
				// report with nothing for the user to notice.
				fmt.Fprintf(os.Stderr, "warning: unable to access %s: %v\n", path, err)
				return nil
			}
			if d.IsDir() || filepath.Ext(path) != ".jsonl" {
				return nil
			}
			records, err := parseFile(path, projName, reader)
			allRecords = append(allRecords, records...)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: %v\n", err)
			}
			return nil
		})
	}

	return Dedup(allRecords), nil
}

// parseFile opens path and delegates. Split the same way as the Codex side so
// the parse itself can be driven by an injected reader: a test that builds its
// own loop pins the loop, not this function.
func parseFile(path string, projDir string, reader *bufio.Reader) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()
	reader.Reset(f)
	return parseBufferedReader(reader, path, projDir)
}

// parseBufferedReader parses assistant lines from reader. Records parsed before
// a read failure are returned alongside the error rather than discarded — losing
// a whole file's usage to a late I/O error is a silent under-count.
func parseBufferedReader(reader *bufio.Reader, path string, projDir string) ([]Record, error) {
	var (
		records        []Record
		fileProjectDir string
	)
	// Shares the Codex reader loop: a bufio.Scanner stopped at the first line
	// over its cap and silently dropped every assistant line after it.
	readErr := forEachLine(reader, path, func(data []byte, _ int) {
		rec, ok := ParseLine(data)
		if !ok {
			return
		}
		if fileProjectDir == "" && rec.ProjectDir != "" {
			fileProjectDir = rec.ProjectDir
		}
		records = append(records, rec)
	})
	if fileProjectDir == "" {
		fileProjectDir = projDir
	}
	for i := range records {
		records[i].ProjectDir = fileProjectDir
	}
	return records, readErr
}

// ParseLine parses a single JSONL line. Returns the record and true if it is
// a usable assistant line, or zero value and false otherwise.
// Lines with model "<synthetic>" are skipped.
func ParseLine(data []byte) (Record, bool) {
	var jl jsonLine
	if err := json.Unmarshal(data, &jl); err != nil {
		return Record{}, false
	}
	if jl.Type != "assistant" {
		return Record{}, false
	}
	if jl.Message.Model == "<synthetic>" {
		return Record{}, false
	}
	// RFC3339Nano only. The former "2006-01-02T15:04:05.000Z" fallback accepts a
	// strict subset of it, so it never matched anything this parse rejected.
	ts, err := time.Parse(time.RFC3339Nano, jl.Timestamp)
	if err != nil {
		// Dropping the line silently under-counts the day's spend. ParseLine takes
		// only the line, so the value is all the context it can give; forEachLine
		// does hand parseFile the line number, so threading it and the path
		// through is a choice left open rather than a constraint.
		fmt.Fprintf(os.Stderr, "warning: skipping assistant line with invalid timestamp: %q\n", jl.Timestamp)
		return Record{}, false
	}
	return Record{
		Model:            jl.Message.Model,
		ProjectDir:       jl.Cwd,
		SessionID:        jl.SessionID,
		RequestID:        jl.RequestID,
		MessageID:        jl.Message.ID,
		Timestamp:        ts,
		InputTokens:      jl.Message.Usage.InputTokens,
		OutputTokens:     jl.Message.Usage.OutputTokens,
		CacheWriteTokens: jl.Message.Usage.CacheWriteTokens,
		CacheReadTokens:  jl.Message.Usage.CacheReadTokens,
	}, true
}
