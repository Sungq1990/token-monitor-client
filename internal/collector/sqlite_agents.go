package collector

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
)

// ---- OpenCode：opencode.db 的 session 表 + message.data(JSON) ----

type openCodeCollector struct {
	name string
	o    Options
}

func newOpenCode(name string, o Options) Collector {
	o.DBFile = or(o.DBFile, "opencode.db")
	return &openCodeCollector{name: name, o: o}
}

func (c *openCodeCollector) Name() string { return c.name }

func (c *openCodeCollector) Sources() []string {
	var out []string
	for _, base := range existingRoots(c.o.Paths) {
		db := filepath.Join(base, filepath.FromSlash(c.o.DBFile))
		if st, err := os.Stat(db); err == nil && !st.IsDir() {
			out = append(out, db)
		}
	}
	return out
}

func (c *openCodeCollector) Collect(cursors CursorStore, logf func(string, ...any)) []Batch {
	var batches []Batch
	for _, db := range c.Sources() {
		b, err := c.collectDB(db, cursors)
		if err != nil {
			logf("opencode: failed to collect %s: %v", db, err)
			continue
		}
		batches = append(batches, *b)
	}
	return batches
}

func cursorMS(s string) int64 {
	f, _ := strconv.ParseFloat(s, 64)
	return int64(f)
}

// ocTokens 兼容新旧版 message.data 里的 token 结构（两版 tokens 对象字段一致，外层不同）。
type ocTokens struct {
	Input     int64 `json:"input"`
	Output    int64 `json:"output"`
	Reasoning int64 `json:"reasoning"`
	Cache     struct {
		Read  int64 `json:"read"`
		Write int64 `json:"write"`
	} `json:"cache"`
}

func (t ocTokens) nonzero() bool {
	return t.Input != 0 || t.Output != 0 || t.Cache.Read != 0 || t.Cache.Write != 0 || t.Reasoning != 0
}

func (t ocTokens) total() int64 {
	return t.Input + t.Output + t.Cache.Read + t.Cache.Write
}

func tableExists(con *sql.DB, name string) bool {
	var n int
	err := con.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&n)
	return err == nil && n > 0
}

func (c *openCodeCollector) collectDB(db string, cursors CursorStore) (*Batch, error) {
	cursor := cursorMS(cursors.Get(c.name, db))
	windowStart := cursor - int64(c.o.WindowSeconds)*1000
	if windowStart < 0 {
		windowStart = 0
	}
	con, err := openSQLiteRO(db)
	if err != nil {
		return nil, err
	}
	defer con.Close()

	var items []UsageItem
	maxTC := cursor

	// opencode v2.3+ 把新会话写进 session_v2 / session_message；旧表 session / message 不再增长。
	// 两版都采，游标取最大值，互不影响（服务端按 message_id / session_id 去重）。
	v2 := tableExists(con, "session_v2") && tableExists(con, "session_message")
	if v2 {
		c.collectV2(con, windowStart, &items, &maxTC)
	}
	c.collectV1(con, windowStart, &items, &maxTC)

	return &Batch{Source: db, Cursor: strconv.FormatInt(maxTC, 10), Items: items}, nil
}

// collectV1 旧版：session + message 表。
func (c *openCodeCollector) collectV1(con *sql.DB, windowStart int64, items *[]UsageItem, maxTC *int64) {
	rows, err := con.Query("SELECT id, title, model, time_created, time_updated FROM session WHERE time_updated >= ?", windowStart)
	if err != nil {
		return
	}
	for rows.Next() {
		var sid string
		var title, model sql.NullString
		var tc, tu sql.NullInt64
		if err := rows.Scan(&sid, &title, &model, &tc, &tu); err != nil {
			continue
		}
		t := title.String
		if len(t) > 200 {
			t = t[:200]
		}
		*items = append(*items, UsageItem{
			SessionID: sid, Model: model.String, SessionTitle: t,
			SessionStartedAt: msToTime(tc.Int64), SessionLastMessageAt: msToTime(tu.Int64),
		})
	}
	rows.Close()

	rows, err = con.Query("SELECT id, session_id, time_created, data FROM message WHERE time_created >= ?", windowStart)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var mid string
		var sid sql.NullString
		var tc sql.NullInt64
		var raw sql.NullString
		if err := rows.Scan(&mid, &sid, &tc, &raw); err != nil {
			continue
		}
		if tc.Int64 > *maxTC {
			*maxTC = tc.Int64
		}
		if raw.String == "" {
			continue
		}
		var data struct {
			Role       string `json:"role"`
			ModelID    string `json:"modelID"`
			ProviderID string `json:"providerID"`
			Cost       *float64
			Tokens     ocTokens `json:"tokens"`
		}
		if json.Unmarshal([]byte(raw.String), &data) != nil || data.Role != "assistant" {
			continue
		}
		if !data.Tokens.nonzero() {
			continue
		}
		*items = append(*items, UsageItem{
			MessageID: mid, SessionID: sid.String, Model: data.ModelID, Provider: data.ProviderID,
			InputTokens: data.Tokens.Input, OutputTokens: data.Tokens.Output, CacheReadTokens: data.Tokens.Cache.Read, CacheWriteTokens: data.Tokens.Cache.Write,
			ReasoningTokens: data.Tokens.Reasoning, TotalTokens: data.Tokens.total(), Cost: data.Cost, OccurredAt: msToTime(tc.Int64),
		})
	}
}

// collectV2 opencode v2.3+：session_v2 + session_message 表。
func (c *openCodeCollector) collectV2(con *sql.DB, windowStart int64, items *[]UsageItem, maxTC *int64) {
	// 会话元信息：model 是 JSON 字符串 {"id":...,"providerID":...}
	rows, err := con.Query("SELECT id, title, model, time_created, time_updated FROM session_v2 WHERE time_updated >= ?", windowStart)
	if err != nil {
		return
	}
	for rows.Next() {
		var sid string
		var title, modelRaw sql.NullString
		var tc, tu sql.NullInt64
		if err := rows.Scan(&sid, &title, &modelRaw, &tc, &tu); err != nil {
			continue
		}
		t := title.String
		if len(t) > 200 {
			t = t[:200]
		}
		var model, provider string
		if modelRaw.String != "" {
			var m struct {
				ID         string `json:"id"`
				ProviderID string `json:"providerID"`
			}
			if json.Unmarshal([]byte(modelRaw.String), &m) == nil {
				model, provider = m.ID, m.ProviderID
			}
		}
		*items = append(*items, UsageItem{
			SessionID: sid, Model: model, Provider: provider, SessionTitle: t,
			SessionStartedAt: msToTime(tc.Int64), SessionLastMessageAt: msToTime(tu.Int64),
		})
	}
	rows.Close()

	// usage：type='assistant'，tokens 在 data 顶层（无 role 字段，用表列 type 判断）
	rows, err = con.Query("SELECT id, session_id, time_created, data FROM session_message WHERE time_created >= ? AND type='assistant'", windowStart)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var mid string
		var sid sql.NullString
		var tc sql.NullInt64
		var raw sql.NullString
		if err := rows.Scan(&mid, &sid, &tc, &raw); err != nil {
			continue
		}
		if tc.Int64 > *maxTC {
			*maxTC = tc.Int64
		}
		if raw.String == "" {
			continue
		}
		var data struct {
			Cost   *float64 `json:"cost"`
			Model  struct {
				ID         string `json:"id"`
				ProviderID string `json:"providerID"`
			} `json:"model"`
			Tokens ocTokens `json:"tokens"`
		}
		if json.Unmarshal([]byte(raw.String), &data) != nil {
			continue
		}
		if !data.Tokens.nonzero() {
			continue
		}
		*items = append(*items, UsageItem{
			MessageID: mid, SessionID: sid.String, Model: data.Model.ID, Provider: data.Model.ProviderID,
			InputTokens: data.Tokens.Input, OutputTokens: data.Tokens.Output, CacheReadTokens: data.Tokens.Cache.Read, CacheWriteTokens: data.Tokens.Cache.Write,
			ReasoningTokens: data.Tokens.Reasoning, TotalTokens: data.Tokens.total(), Cost: data.Cost, OccurredAt: msToTime(tc.Int64),
		})
	}
}

// ---- ZCode：cli/db/db.sqlite 的 session 表 + model_usage 表 ----

type zcodeCollector struct {
	name string
	o    Options
}

func newZCode(name string, o Options) Collector {
	o.DBFile = or(o.DBFile, "cli/db/db.sqlite")
	return &zcodeCollector{name: name, o: o}
}

func (c *zcodeCollector) Name() string { return c.name }

func (c *zcodeCollector) Sources() []string {
	var out []string
	for _, base := range existingRoots(c.o.Paths) {
		db := filepath.Join(base, filepath.FromSlash(c.o.DBFile))
		if st, err := os.Stat(db); err == nil && !st.IsDir() {
			out = append(out, db)
		}
	}
	return out
}

func (c *zcodeCollector) Collect(cursors CursorStore, logf func(string, ...any)) []Batch {
	var batches []Batch
	for _, db := range c.Sources() {
		b, err := c.collectDB(db, cursors)
		if err != nil {
			logf("zcode: failed to collect %s: %v", db, err)
			continue
		}
		batches = append(batches, *b)
	}
	return batches
}

func (c *zcodeCollector) collectDB(db string, cursors CursorStore) (*Batch, error) {
	cursor := cursorMS(cursors.Get(c.name, db))
	windowStart := cursor - int64(c.o.WindowSeconds)*1000
	if windowStart < 0 {
		windowStart = 0
	}
	con, err := openSQLiteRO(db)
	if err != nil {
		return nil, err
	}
	defer con.Close()

	var items []UsageItem
	maxStarted := cursor

	rows, err := con.Query("SELECT id, title, time_created, time_updated FROM session WHERE time_updated >= ?", windowStart)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var sid string
		var title sql.NullString
		var tc, tu sql.NullInt64
		if err := rows.Scan(&sid, &title, &tc, &tu); err != nil {
			continue
		}
		t := title.String
		if len(t) > 200 {
			t = t[:200]
		}
		items = append(items, UsageItem{SessionID: sid, SessionTitle: t, SessionStartedAt: msToTime(tc.Int64), SessionLastMessageAt: msToTime(tu.Int64)})
	}
	rows.Close()

	rows, err = con.Query(`SELECT id, session_id, provider_id, model_id, started_at, input_tokens, output_tokens,
		reasoning_tokens, cache_creation_input_tokens, cache_read_input_tokens, computed_total_tokens
		FROM model_usage WHERE started_at >= ?`, windowStart)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var mid string
		var sid, provider, model sql.NullString
		var started, inp, out, reasoning, cw, cr, computed sql.NullInt64
		if err := rows.Scan(&mid, &sid, &provider, &model, &started, &inp, &out, &reasoning, &cw, &cr, &computed); err != nil {
			continue
		}
		if started.Int64 > maxStarted {
			maxStarted = started.Int64
		}
		if inp.Int64 == 0 && out.Int64 == 0 && cr.Int64 == 0 && cw.Int64 == 0 && reasoning.Int64 == 0 {
			continue // error/cancelled 没有真实 usage
		}
		total := computed.Int64
		if total == 0 {
			total = inp.Int64 + out.Int64 + cr.Int64 + cw.Int64
		}
		items = append(items, UsageItem{
			MessageID: mid, SessionID: sid.String, Model: model.String, Provider: provider.String,
			InputTokens: inp.Int64, OutputTokens: out.Int64, CacheReadTokens: cr.Int64, CacheWriteTokens: cw.Int64,
			ReasoningTokens: reasoning.Int64, TotalTokens: total, OccurredAt: msToTime(started.Int64),
		})
	}
	return &Batch{Source: db, Cursor: strconv.FormatInt(maxStarted, 10), Items: items}, nil
}
