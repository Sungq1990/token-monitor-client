package collector

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

// ---- SQLite 只读打开：原生路径直接 mode=ro；打不开（如 WAL + 网络盘/UNC）时用「源未变化则复用」的本地快照 ----
//
// 性能要点（\\wsl.localhost 等 9P 路径整库拷贝很慢）：
//   - UNC 路径（\\开头）跳过直开尝试（WAL 库在 9P 上必失败，且 busy_timeout 会白等）；
//   - 快照按「主库文件」和「-wal」分开记签名：主库没变（没 checkpoint）时只补拷 wal，避免每轮整库重拷。

type snapshot struct {
	dir     string
	mainSig string
	walSig  string
}

var (
	snapMu    sync.Mutex
	snapshots = map[string]*snapshot{}
)

func sigOne(path string) string {
	if st, err := os.Stat(path); err == nil {
		return fmt.Sprintf("%d:%d", st.Size(), st.ModTime().UnixNano())
	}
	return ""
}

func isUNC(p string) bool {
	return runtime.GOOS == "windows" && strings.HasPrefix(p, `\\`)
}

func openSQLiteRO(db string) (*sql.DB, error) {
	dsn := func(p string) string {
		// SQLite URI：Windows 绝对路径需要 file:///C:/...，其余平台 file:/abs/path
		u := "file:" + filepath.ToSlash(p)
		if runtime.GOOS == "windows" && len(p) > 1 && p[1] == ':' {
			u = "file:///" + filepath.ToSlash(p)
		}
		return u + "?mode=ro&_pragma=busy_timeout(5000)&_pragma=query_only(1)"
	}
	// UNC/网络路径上的 WAL 库直开必然失败且开销大，直接走快照
	if !isUNC(db) {
		if con, err := sql.Open("sqlite", dsn(db)); err == nil {
			var n int
			if err := con.QueryRow("select count(*) from sqlite_master").Scan(&n); err == nil {
				return con, nil
			}
			con.Close()
		}
	}

	base := filepath.Base(db)
	snapMu.Lock()
	defer snapMu.Unlock()

	mainSig, walSig := sigOne(db), sigOne(db+"-wal")
	cached := snapshots[db]
	if cached != nil && cached.mainSig == mainSig {
		target := filepath.Join(cached.dir, base)
		if _, err := os.Stat(target); err == nil {
			if walSig != cached.walSig { // 主库没 checkpoint，只是 wal 增长：补拷小文件即可
				if walSig != "" {
					if err := copyFile(db+"-wal", target+"-wal"); err != nil {
						return nil, err
					}
					cached.walSig = walSig
				}
			}
			return sql.Open("sqlite", dsn(target))
		}
	}

	if cached != nil {
		os.RemoveAll(cached.dir)
	}
	dir, err := os.MkdirTemp("", "tokenmon_db_")
	if err != nil {
		return nil, err
	}
	target := filepath.Join(dir, base)
	if err := copyFile(db, target); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	if _, err := os.Stat(db + "-wal"); err == nil {
		_ = copyFile(db+"-wal", target+"-wal")
	}
	if _, err := os.Stat(db + "-shm"); err == nil {
		_ = copyFile(db+"-shm", target+"-shm")
	}
	snapshots[db] = &snapshot{dir: dir, mainSig: mainSig, walSig: walSig}
	con, err := sql.Open("sqlite", dsn(target))
	if err != nil {
		return nil, err
	}
	var n int
	if err := con.QueryRow("select count(*) from sqlite_master").Scan(&n); err != nil {
		con.Close()
		return nil, err
	}
	return con, nil
}

// CleanupSnapshots 进程退出时删除本进程创建的快照目录
func CleanupSnapshots() {
	snapMu.Lock()
	defer snapMu.Unlock()
	for k, s := range snapshots {
		os.RemoveAll(s.dir)
		delete(snapshots, k)
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	// 1MB 缓冲：网络盘（9P/SMB）上比默认 32KB 快数倍
	if _, err := io.CopyBuffer(out, in, make([]byte, 1<<20)); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
