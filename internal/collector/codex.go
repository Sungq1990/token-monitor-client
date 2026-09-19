package collector

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

// codexCollector 读取 Codex rollout 的 token_count 事件；累计计数器取差值，绝不直接求和。
type codexCollector struct {
	name string
	o    Options
}

func newCodex(name string, o Options) Collector {
	o.ProjectsDir = or(o.ProjectsDir, "sessions")
	o.FileGlob = or(o.FileGlob, "**/*.jsonl")
	return &codexCollector{name: name, o: o}
}

func (c *codexCollector) Name() string { return c.name }

func (c *codexCollector) Sources() []string {
	seen := map[string]bool{}
	var out []string
	for _, root := range existingRoots(c.o.Paths) {
		for _, p := range globFiles(filepath.Join(root, c.o.ProjectsDir), c.o.FileGlob) {
			if abs, err := filepath.Abs(p); err == nil {
				p = abs
			}
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

var codexFields = []string{"input_tokens", "cached_input_tokens", "output_tokens", "reasoning_output_tokens", "total_tokens"}

type codexState struct {
	Offset   int64   `json:"offset"`
	Session  string  `json:"session,omitempty"`
	Previous []int64 `json:"previous,omitempty"`
	Model    string  `json:"model,omitempty"`
}

func (c *codexCollector) Collect(cursors CursorStore, logf func(string, ...any)) []Batch {
	var batches []Batch
	for _, src := range c.Sources() {
		b, err := c.collectFile(src, cursors)
		if err != nil {
			logf("codex: failed to read %s: %v", src, err)
			continue
		}
		if b != nil {
			batches = append(batches, *b)
		}
	}
	return batches
}

func (c *codexCollector) collectFile(path string, cursors CursorStore) (*Batch, error) {
	var state codexState
	if raw := cursors.Get(c.name, path); raw != "" {
		_ = json.Unmarshal([]byte(raw), &state)
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Size() < state.Offset {
		state = codexState{}
	}
	startOffset := state.Offset
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	if _, err := fh.Seek(state.Offset, 0); err != nil {
		return nil, err
	}
	rd := bufio.NewReaderSize(fh, 1<<20)
	var items []UsageItem
	pos := state.Offset
	for {
		line, err := rd.ReadBytes('\n')
		if err != nil || len(line) == 0 || line[len(line)-1] != '\n' {
			state.Offset = pos // 半行留到下次
			break
		}
		pos += int64(len(line))
		state.Offset = pos
		var event map[string]any
		if json.Unmarshal(line, &event) != nil {
			continue
		}
		payload, ok := event["payload"].(map[string]any)
		if !ok {
			continue
		}
		etype, _ := event["type"].(string)
		switch etype {
		case "session_meta":
			if sid, _ := payload["id"].(string); sid != "" && sid != state.Session {
				state.Session, state.Previous, state.Model = sid, nil, ""
			}
			continue
		case "turn_context":
			state.Model, _ = payload["model"].(string)
			continue
		case "event_msg":
		default:
			continue
		}
		if pt, _ := payload["type"].(string); pt != "token_count" {
			continue
		}
		info, _ := payload["info"].(map[string]any)
		usage, _ := info["total_token_usage"].(map[string]any)
		if usage == nil || state.Session == "" {
			continue
		}
		current := make([]int64, len(codexFields))
		valid := true
		for i, k := range codexFields {
			f, ok := usage[k].(float64)
			if !ok || f < 0 || f != float64(int64(f)) {
				valid = false
				break
			}
			current[i] = int64(f)
		}
		if !valid {
			continue
		}
		previous := state.Previous
		if len(previous) != len(codexFields) {
			previous = make([]int64, len(codexFields))
		}
		state.Previous = current
		rollback := false
		delta := make([]int64, len(codexFields))
		for i := range current {
			if current[i] < previous[i] {
				rollback = true // 回退不是新增消耗
			}
			delta[i] = current[i] - previous[i]
		}
		if rollback {
			continue
		}
		inp, cache, out, reasoning, total := delta[0], delta[1], delta[2], delta[3], delta[4]
		if total == 0 || cache > inp || reasoning > out {
			continue
		}
		tsStr, _ := event["timestamp"].(string)
		ts := parseTS(tsStr)
		identity, _ := json.Marshal([]any{state.Session, tsStr, current})
		sum := sha256.Sum256(identity)
		items = append(items, UsageItem{
			MessageID: "codex:" + hex.EncodeToString(sum[:]),
			SessionID: state.Session, Model: state.Model,
			InputTokens: inp - cache, CacheReadTokens: cache, OutputTokens: out, ReasoningTokens: reasoning,
			TotalTokens: total, OccurredAt: ts, SessionLastMessageAt: ts,
		})
	}
	if state.Offset == startOffset {
		return nil, nil
	}
	cur, _ := json.Marshal(state)
	return &Batch{Source: path, Cursor: string(cur), Items: items}, nil
}
