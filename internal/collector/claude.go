package collector

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// claudeCollector 读取 ~/.claude/projects/**/*.jsonl，按文件字节 offset 增量解析。
type claudeCollector struct {
	name string
	o    Options
}

func newClaude(name string, o Options) Collector {
	o.ProjectsDir = or(o.ProjectsDir, "projects")
	o.FileGlob = or(o.FileGlob, "*/*.jsonl")
	return &claudeCollector{name: name, o: o}
}

func (c *claudeCollector) Name() string { return c.name }

func (c *claudeCollector) Sources() []string {
	var out []string
	for _, base := range existingRoots(c.o.Paths) {
		out = append(out, globFiles(filepath.Join(base, c.o.ProjectsDir), c.o.FileGlob)...)
	}
	return out
}

// globFiles 支持 "**" 递归匹配（Go 的 filepath.Glob 不支持）
func globFiles(root, pattern string) []string {
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		return nil
	}
	var out []string
	if strings.Contains(pattern, "**") {
		suffix := pattern[strings.LastIndex(pattern, "**")+2:]
		suffix = strings.TrimLeft(suffix, "/\\")
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if ok, _ := filepath.Match(suffix, d.Name()); ok {
				out = append(out, p)
			}
			return nil
		})
	} else {
		matches, _ := filepath.Glob(filepath.Join(root, filepath.FromSlash(pattern)))
		for _, m := range matches {
			if st, err := os.Stat(m); err == nil && !st.IsDir() {
				out = append(out, m)
			}
		}
	}
	sort.Strings(out)
	return out
}

func (c *claudeCollector) Collect(cursors CursorStore, logf func(string, ...any)) []Batch {
	var batches []Batch
	for _, f := range c.Sources() {
		b, err := c.collectFile(f, cursors)
		if err != nil {
			logf("claude: failed to collect %s: %v", f, err)
			continue
		}
		if b != nil {
			batches = append(batches, *b)
		}
	}
	return batches
}

func parseTS(s string) *time.Time {
	if s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z07:00"} {
		if t, err := time.Parse(layout, s); err == nil {
			return toLocal(t)
		}
	}
	return nil
}

func userText(content any) string {
	var text string
	switch v := content.(type) {
	case string:
		text = v
	case []any:
		var parts []string
		for _, b := range v {
			if m, ok := b.(map[string]any); ok && m["type"] == "text" {
				if t, _ := m["text"].(string); t != "" {
					parts = append(parts, t)
				}
			}
		}
		text = strings.Join(parts, "\n")
	default:
		return ""
	}
	text = strings.TrimSpace(text)
	if text == "" || strings.HasPrefix(text, "<") {
		return ""
	}
	return text
}

type sessMeta struct {
	title         string
	model         string
	started, last *time.Time
}

func (c *claudeCollector) collectFile(path string, cursors CursorStore) (*Batch, error) {
	offset, _ := strconv.ParseInt(cursors.Get(c.name, path), 10, 64)
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Size() < offset { // 文件被轮转/重置
		offset = 0
	}
	if st.Size() == offset {
		return nil, nil
	}
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	if _, err := fh.Seek(offset, 0); err != nil {
		return nil, err
	}
	data, err := readAll(fh)
	if err != nil {
		return nil, err
	}
	// 只处理完整行
	if !bytes.HasSuffix(data, []byte("\n")) {
		cut := bytes.LastIndexByte(data, '\n')
		if cut < 0 {
			return nil, nil
		}
		data = data[:cut+1]
	}
	newOffset := offset + int64(len(data))

	var items []UsageItem
	sessions := map[string]*sessMeta{}
	var order []string
	seen := map[string]bool{}

	for _, raw := range bytes.Split(data, []byte("\n")) {
		line := bytes.TrimSpace(raw)
		if len(line) == 0 {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal(line, &obj); err != nil {
			continue
		}
		c.handleLine(obj, &items, sessions, &order, seen)
	}
	for _, sid := range order {
		m := sessions[sid]
		items = append(items, UsageItem{
			SessionID: sid, Model: m.model, SessionTitle: m.title,
			SessionStartedAt: m.started, SessionLastMessageAt: m.last,
		})
	}
	return &Batch{Source: path, Cursor: strconv.FormatInt(newOffset, 10), Items: items}, nil
}

func (c *claudeCollector) handleLine(obj map[string]any, items *[]UsageItem, sessions map[string]*sessMeta, order *[]string, seen map[string]bool) {
	if b, _ := obj["isMeta"].(bool); b {
		return
	}
	sid, _ := obj["sessionId"].(string)
	if sid == "" {
		sid, _ = obj["session_id"].(string)
	}
	tsStr, _ := obj["timestamp"].(string)
	ts := parseTS(tsStr)

	var meta *sessMeta
	if sid != "" {
		meta = sessions[sid]
		if meta == nil {
			meta = &sessMeta{started: ts, last: ts}
			sessions[sid] = meta
			*order = append(*order, sid)
		} else if ts != nil {
			meta.last = maxTime(meta.last, ts)
			if meta.started == nil {
				meta.started = ts
			}
		}
	}

	otype, _ := obj["type"].(string)
	msg, _ := obj["message"].(map[string]any)
	if otype == "user" && meta != nil {
		if side, _ := obj["isSidechain"].(bool); !side && meta.title == "" {
			if t := userText(msg["content"]); t != "" {
				if len(t) > 200 {
					t = t[:200]
				}
				meta.title = t
			}
		}
	}
	if otype != "assistant" || msg == nil {
		return
	}
	usage, ok := msg["usage"].(map[string]any)
	if !ok {
		return // 无真实 usage 不记录，绝不估算
	}
	mid, _ := msg["id"].(string)
	if mid == "" {
		mid, _ = obj["uuid"].(string)
	}
	if mid == "" {
		return
	}
	if seen[mid] {
		return // 同一 API 响应的多条块行只记一次
	}
	seen[mid] = true

	inp := num(usage["input_tokens"])
	out := num(usage["output_tokens"])
	cr := num(usage["cache_read_input_tokens"])
	cw := num(usage["cache_creation_input_tokens"])
	var reasoning int64
	if d, ok := usage["output_tokens_details"].(map[string]any); ok {
		reasoning = num(d["thinking_tokens"])
	}
	model, _ := msg["model"].(string)
	if meta != nil && model != "" {
		meta.model = model
	}
	it := UsageItem{
		MessageID: mid, SessionID: sid, Model: model,
		InputTokens: inp, OutputTokens: out, CacheReadTokens: cr, CacheWriteTokens: cw, ReasoningTokens: reasoning,
		TotalTokens: inp + out + cr + cw, OccurredAt: ts,
	}
	if meta != nil {
		it.SessionStartedAt, it.SessionLastMessageAt = meta.started, meta.last
	}
	*items = append(*items, it)
}

func num(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	case int64:
		return x
	case int:
		return int64(x)
	}
	return 0
}

func readAll(f *os.File) ([]byte, error) {
	var buf bytes.Buffer
	_, err := buf.ReadFrom(f)
	return buf.Bytes(), err
}
