// Package cursors 本地游标持久化（JSON 文件），上报成功后才提交。
package cursors

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type File struct {
	mu    sync.Mutex
	path  string
	data  map[string]map[string]string // agent -> source -> cursor
	dirty bool
	// Recovered 启动时发现游标文件损坏（已备份为 cursors.json.corrupt-*，从空游标继续）
	Recovered bool
}

func Open(dir string) (*File, error) {
	f := &File{path: filepath.Join(dir, "cursors.json"), data: map[string]map[string]string{}}
	raw, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &f.data); err != nil {
		// 游标损坏只会导致重扫，不是致命错误；但保留现场，避免静默吞掉用户数据
		backup := f.path + ".corrupt-" + time.Now().Format("20060102-150405")
		if rerr := os.Rename(f.path, backup); rerr == nil {
			f.Recovered = true
		}
		f.data = map[string]map[string]string{}
	}
	return f, nil
}

func (f *File) Get(agent, source string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.data[agent][source]
}

func (f *File) Set(agent, source, cursor string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.data[agent] == nil {
		f.data[agent] = map[string]string{}
	}
	f.data[agent][source] = cursor
	f.dirty = true
}

// Reset 清空某 agent（或全部）的游标，下次全量重扫
func (f *File) Reset(agent string) error {
	f.mu.Lock()
	if agent == "" {
		f.data = map[string]map[string]string{}
	} else {
		delete(f.data, agent)
	}
	f.dirty = true
	f.mu.Unlock()
	return f.Flush()
}

// Flush 原子落盘：写临时文件并 fsync 后再 rename。断电/强杀最多丢最后一次 Flush，
// 不会留下写了一半的 cursors.json。无变更时空操作。
func (f *File) Flush() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.dirty {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(f.data, "", " ")
	if err != nil {
		return err
	}
	tmp := f.path + ".tmp"
	w, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := w.Write(raw); err != nil {
		w.Close()
		return err
	}
	// fsync 失败直接报错、保住旧文件，不走到 rename
	if err := w.Sync(); err != nil {
		w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, f.path); err != nil {
		return err
	}
	f.dirty = false
	return nil
}
