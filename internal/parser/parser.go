package parser

import (
	"encoding/json"
	"time"
)

// Record holds the extracted fields from one deduplicated assistant message.
type Record struct {
	Model            string
	SessionID        string
	RequestID        string
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
		Model string `json:"model"`
		Usage struct {
			InputTokens      int64 `json:"input_tokens"`
			OutputTokens     int64 `json:"output_tokens"`
			CacheWriteTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadTokens  int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// ParseLine parses a single JSONL line. Returns the record and true if it is
// a usable assistant line, or zero value and false otherwise.
func ParseLine(data []byte) (Record, bool) {
	var jl jsonLine
	if err := json.Unmarshal(data, &jl); err != nil {
		return Record{}, false
	}
	if jl.Type != "assistant" {
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
		Timestamp:        ts,
		InputTokens:      jl.Message.Usage.InputTokens,
		OutputTokens:     jl.Message.Usage.OutputTokens,
		CacheWriteTokens: jl.Message.Usage.CacheWriteTokens,
		CacheReadTokens:  jl.Message.Usage.CacheReadTokens,
	}, true
}
