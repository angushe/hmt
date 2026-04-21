package parser

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Record holds the extracted fields from one deduplicated assistant message.
type Record struct {
	Model            string
	SessionID        string
	RequestID        string
	MessageID        string // API response message ID, used for dedup
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

// Dedup removes streaming duplicates using first-seen strategy.
// The dedup key is "messageId:requestId". The first occurrence is kept,
// subsequent duplicates are discarded. Records without both IDs are kept as-is.
func Dedup(records []Record) []Record {
	seen := make(map[string]struct{})
	result := make([]Record, 0, len(records))
	for _, r := range records {
		hash := dedupHash(r)
		if hash == "" {
			result = append(result, r)
			continue
		}
		if _, ok := seen[hash]; ok {
			continue
		}
		seen[hash] = struct{}{}
		result = append(result, r)
	}
	return result
}

func dedupHash(r Record) string {
	if r.MessageID == "" || r.RequestID == "" {
		return ""
	}
	return r.MessageID + ":" + r.RequestID
}

// ProjectName derives a short display name from the Claude Code project
// directory name. E.g. "-Users-angus-basebit-project-nova-nova" -> "nova/nova".
// For paths with 3+ segments, returns the last two joined with "/".
// For 1-2 segments, returns just the last segment.
func ProjectName(dir string) string {
	parts := splitDirName(dir)
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

// ScanDir recursively reads all .jsonl files under baseDir/*/,
// including subagent logs in nested directories.
// It parses assistant lines, deduplicates, and returns records.
func ScanDir(baseDir string) ([]Record, error) {
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", baseDir, err)
	}

	var allRecords []Record
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		projName := entry.Name()
		projPath := filepath.Join(baseDir, projName)
		err := filepath.WalkDir(projPath, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil // skip inaccessible paths
			}
			if d.IsDir() || filepath.Ext(path) != ".jsonl" {
				return nil
			}
			records, err := parseFile(path, projName)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: %v\n", err)
				return nil
			}
			allRecords = append(allRecords, records...)
			return nil
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: walking %s: %v\n", projPath, err)
		}
	}

	return Dedup(allRecords), nil
}

func parseFile(path string, projDir string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()

	var records []Record
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)
	for scanner.Scan() {
		rec, ok := ParseLine(scanner.Bytes())
		if !ok {
			continue
		}
		rec.ProjectDir = projDir
		records = append(records, rec)
	}
	if err := scanner.Err(); err != nil {
		return records, fmt.Errorf("scanning %s: %w", path, err)
	}
	return records, nil
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
	ts, err := time.Parse(time.RFC3339Nano, jl.Timestamp)
	if err != nil {
		ts, err = time.Parse("2006-01-02T15:04:05.000Z", jl.Timestamp)
		if err != nil {
			return Record{}, false
		}
	}
	return Record{
		Model:            jl.Message.Model,
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
