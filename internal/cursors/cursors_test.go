package cursors

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFlushAndReload(t *testing.T) {
	dir := t.TempDir()
	f, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	f.Set("claude", "/tmp/a.jsonl", "1024")
	f.Set("codex", "/tmp/b.jsonl", "w-5")
	if err := f.Flush(); err != nil {
		t.Fatal(err)
	}
	// 未变更时 Flush 应为空操作且不报错
	if err := f.Flush(); err != nil {
		t.Fatal(err)
	}

	f2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := f2.Get("claude", "/tmp/a.jsonl"); got != "1024" {
		t.Fatalf("claude 游标 = %q, want 1024", got)
	}
	if got := f2.Get("codex", "/tmp/b.jsonl"); got != "w-5" {
		t.Fatalf("codex 游标 = %q, want w-5", got)
	}
	if f2.Recovered {
		t.Fatal("正常文件不应置 Recovered")
	}
}

func TestOpenCorruptFileBacksUp(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "cursors.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := Open(dir)
	if err != nil {
		t.Fatalf("损坏文件不应阻断启动: %v", err)
	}
	if !f.Recovered {
		t.Fatal("应置 Recovered")
	}
	if got := f.Get("claude", "x"); got != "" {
		t.Fatalf("损坏后应从空游标开始, got %q", got)
	}
	// 坏文件应被改名保留，原路径不再存在
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "cursors.json.corrupt-") {
			found = true
		}
		if e.Name() == "cursors.json" {
			t.Fatal("损坏文件应被移走，原路径不应残留")
		}
	}
	if !found {
		t.Fatal("未找到 cursors.json.corrupt-* 备份")
	}
	// 备份后继续 Set/Flush 应能正常落盘
	f.Set("claude", "x", "1")
	if err := f.Flush(); err != nil {
		t.Fatal(err)
	}
}

func TestResetClearsAndPersists(t *testing.T) {
	dir := t.TempDir()
	f, _ := Open(dir)
	f.Set("claude", "a", "1")
	f.Set("codex", "b", "2")
	if err := f.Reset("claude"); err != nil {
		t.Fatal(err)
	}
	if f.Get("claude", "a") != "" || f.Get("codex", "b") != "2" {
		t.Fatal("单 agent Reset 未生效")
	}
	f2, _ := Open(dir)
	if f2.Get("codex", "b") != "2" {
		t.Fatal("Reset 后落盘丢失了其他 agent 的游标")
	}
	if err := f.Reset(""); err != nil {
		t.Fatal(err)
	}
	f3, _ := Open(dir)
	if f3.Get("codex", "b") != "" {
		t.Fatal("全量 Reset 未持久化")
	}
}
