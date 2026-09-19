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

	rows, err := con.Query("SELECT id, title, model, time_created, time_updated FROM session WHERE time_updated >= ?", windowStart)
	if err != nil {
		return nil, err
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
		items = append(items, UsageItem{
			SessionID: sid, Model: model.String, SessionTitle: t,
			SessionStartedAt: msToTime(tc.Int64), SessionLastMessageAt: msToTime(tu.Int64),
		})
	}
	rows.Close()

	rows, err = con.Query("SELECT id, session_id, time_created, data FROM message WHERE time_created >= ?", windowStart)
	if err != nil {
		return nil, err
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
		if tc.Int64 > maxTC {
			maxTC = tc.Int64
		}
		if raw.String == "" {
			continue
		}
		var data struct {
			Role       string `json:"role"`
			ModelID    string `json:"modelID"`
			ProviderID string `json:"providerID"`
			Cost       *float64
			Tokens     struct {
				Input     int64 `json:"input"`
				Output    int64 `json:"output"`
				Reasoning int64 `json:"reasoning"`
				Total     int64 `json:"total"`
				Cache     struct {
					Read  int64 `json:"read"`
					Write int64 `json:"write"`
				} `json:"cache"`
			} `json:"tokens"`
		}
		if json.Unmarshal([]byte(raw.String), &data) != nil || data.Role != "assistant" {
			continue
		}
		tk := data.Tokens
		if tk.Input == 0 && tk.Output == 0 && tk.Cache.Read == 0 && tk.Cache.Write == 0 && tk.Reasoning == 0 {
			continue // 没有真实 usage（进行中/失败），不估算
		}
		total := tk.Total
		if total == 0 {
			total = tk.Input + tk.Output + tk.Cache.Read + tk.Cache.Write
		}
		items = append(items, UsageItem{
			MessageID: mid, SessionID: sid.String, Model: data.ModelID, Provider: data.ProviderID,
			InputTokens: tk.Input, OutputTokens: tk.Output, CacheReadTokens: tk.Cache.Read, CacheWriteTokens: tk.Cache.Write,
			ReasoningTokens: tk.Reasoning, TotalTokens: total, Cost: data.Cost, OccurredAt: msToTime(tc.Int64),
		})
	}
	return &Batch{Source: db, Cursor: strconv.FormatInt(maxTC, 10), Items: items}, nil
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
