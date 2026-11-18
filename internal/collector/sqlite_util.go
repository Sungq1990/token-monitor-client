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

// ---- SQLite 只读打开：优先 immutable=1 直开原路径（零复制、零额外磁盘）；失败才回退本地快照 ----
//
// 性能要点（\\wsl.localhost 等 9P 路径整库拷贝很慢）：
//   - immutable=1 跳过加锁与 WAL，直接读主库文件；9P/UNC 上不复制、不留副本；
//   - 代价：读不到 WAL 中尚未 checkpoint 的最新数据，由采集器的 RescanWindowSeconds 重扫窗口兜底；
//   - 快照仅作兜底（immutable 直开失败时），按主库/-wal 分开记签名：主库没变时只补拷 wal。

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
	// dsn 构造 SQLite URI。immutable=1 跳过加锁与 WAL，直接读主库文件；
	// UNC 需 file:////host/share/...（4 斜杠），Windows 盘符需 file:///C:/...。
	dsn := func(p string, immutable bool) string {
		sl := filepath.ToSlash(p)
		var u string
		switch {
		case isUNC(p):
			u = "file://" + sl
		case runtime.GOOS == "windows" && len(p) > 1 && p[1] == ':':
			u = "file:///" + sl
		default:
			u = "file:" + sl
		}
		if immutable {
			return u + "?immutable=1&mode=ro&_pragma=query_only(1)"
		}
		return u + "?mode=ro&_pragma=busy_timeout(5000)&_pragma=query_only(1)"
	}
	// 优先 immutable 直开：零复制、不留副本，UNC/9P 路径也能读。
	// 验证查询失败才继续走下面的快照兜底。
	if con, err := sql.Open("sqlite", dsn(db, true)); err == nil {
		var n int
		if err := con.QueryRow("select count(*) from sqlite_master").Scan(&n); err == nil {
			return con, nil
		}
		con.Close()
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
			return sql.Open("sqlite", dsn(target, false))
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
	con, err := sql.Open("sqlite", dsn(target, false))
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
