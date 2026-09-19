// Package cursors 本地游标持久化（JSON 文件），上报成功后才提交。
package cursors

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

type File struct {
	mu   sync.Mutex
	path string
	data map[string]map[string]string // agent -> source -> cursor
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
		// 游标损坏只会导致重扫，不是致命错误
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
}

// Reset 清空某 agent（或全部）的游标，下次全量重扫
func (f *File) Reset(agent string) error {
	f.mu.Lock()
	if agent == "" {
		f.data = map[string]map[string]string{}
	} else {
		delete(f.data, agent)
	}
	f.mu.Unlock()
	return f.Flush()
}

func (f *File) Flush() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(f.data, "", " ")
	if err != nil {
		return err
	}
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.path)
}
