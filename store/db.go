package store

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	// 纯 Go SQLite 驱动（免 CGO），保证 CGO_ENABLED=0 跨平台静态编译单二进制（D1-A）。
	_ "modernc.org/sqlite"
)

// writeOp 是投递给单写协程的一个写任务。
//
// 为什么用单写协程串行化所有写：SQLite 即便开 WAL，写仍是单写者语义；
// 把所有写集中到一条 goroutine 顺序执行，可彻底避免 "database is locked" 竞争，
// 并让统计批量 flush、配置 CRUD、清理删除天然串行、互不抢锁（AC-14）。
type writeOp struct {
	// fn 在单写协程内执行，入参是同一个 *sql.DB（WAL 下读可并发、写经此串行）。
	fn func(db *sql.DB) error
	// done 用于把执行结果回传给调用方（同步等待）。
	done chan error
}

// Store 封装一个 SQLite 连接与其单写协程。
//
// 读操作（各 *_repo.go 的查询）直接走 db 并发读（WAL 允许读写并发）；
// 写操作一律经 Write/WriteTx 投递到单写协程串行执行。
type Store struct {
	db      *sql.DB
	writeCh chan writeOp  // 写任务队列
	closed  chan struct{} // 关闭信号
	once    sync.Once     // 保证 Close 只执行一次
}

// Open 打开（或创建）指定路径的 SQLite 数据库：
//   - 启用 WAL 日志模式（读写并发、写性能更好）；
//   - 设置 busy_timeout 兜底（极端情况下读侧短暂等待而非立即报错）；
//   - 启用外键约束（保证关联表级联一致性）；
//   - 建表/迁移（schema.go）；
//   - 启动单写协程。
//
// path 传 ":memory:" 可用于单元测试（注意内存库需保持单连接，见下连接池设置）。
func Open(path string) (*Store, error) {
	// modernc 驱动名为 "sqlite"。
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("打开 SQLite 失败 %q: %w", path, err)
	}

	// 由于所有写经单写协程串行，且部分场景（:memory: 测试）要求同一连接，
	// 这里限制最大连接数为 1 的写语义由单写协程保证；读连接可适度放开。
	// 为简化并保证 :memory: 库一致性，统一限制为单连接（本项目 DB 不在热路径，单连接足够）。
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(0)

	// 关键 PRAGMA：WAL + busy_timeout + 外键。
	pragmas := []string{
		"PRAGMA journal_mode=WAL;",      // WAL：读写并发、崩溃安全（AC-14）
		"PRAGMA busy_timeout=5000;",     // 写忙时最多等 5s（单写协程下基本不会触发）
		"PRAGMA foreign_keys=ON;",       // 启用外键，保证关联删除一致
		"PRAGMA synchronous=NORMAL;",    // WAL 下 NORMAL 兼顾安全与性能（非热路径）
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("设置 PRAGMA 失败 %q: %w", p, err)
		}
	}

	s := &Store{
		db:      db,
		writeCh: make(chan writeOp, 256), // 带缓冲，吸收统计 flush 等突发写
		closed:  make(chan struct{}),
	}

	// 先启动单写协程：migrate 经 Write 投递到该协程串行执行，必须先有消费者，
	// 否则 Write 会永久阻塞（建表也走单写通道，保证与运行期写一致的串行语义）。
	go s.writeLoop()

	// 建表/迁移（首次创建空库时建全部表与索引）。
	if err := s.migrate(); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("建表/迁移失败: %w", err)
	}

	return s, nil
}

// writeLoop 是单写协程主体：顺序消费写任务，串行执行，避免写锁竞争。
func (s *Store) writeLoop() {
	for {
		select {
		case op := <-s.writeCh:
			op.done <- op.fn(s.db)
		case <-s.closed:
			// 关闭前排空剩余写任务，避免调用方永久阻塞在 done。
			for {
				select {
				case op := <-s.writeCh:
					op.done <- op.fn(s.db)
				default:
					return
				}
			}
		}
	}
}

// Write 把一个写函数投递到单写协程并同步等待其完成。
//
// 所有 INSERT/UPDATE/DELETE 都应经此方法执行，从而保证写操作全局串行（AC-14）。
// fn 内可直接用 db.Exec / db.QueryRow（写过程中读自身写入的行也安全）。
func (s *Store) Write(fn func(db *sql.DB) error) error {
	op := writeOp{fn: fn, done: make(chan error, 1)}
	select {
	case s.writeCh <- op:
		return <-op.done
	case <-s.closed:
		return fmt.Errorf("store 已关闭，拒绝写入")
	}
}

// WriteTx 在单写协程内开启一个事务执行 fn：fn 返回 nil 则提交，否则回滚。
//
// 用于需要原子性的多步写（如导入配置整体覆盖、含多表的 CRUD）。
func (s *Store) WriteTx(fn func(tx *sql.Tx) error) error {
	return s.Write(func(db *sql.DB) error {
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			return fmt.Errorf("开启事务失败: %w", err)
		}
		if err := fn(tx); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("提交事务失败: %w", err)
		}
		return nil
	})
}

// DB 返回底层 *sql.DB，供各 repo 做【只读查询】（WAL 下读可与写并发）。
// 写操作禁止直接用此句柄，必须经 Write/WriteTx 串行化。
func (s *Store) DB() *sql.DB {
	return s.db
}

// Close 停止单写协程并关闭数据库连接（幂等）。
func (s *Store) Close() error {
	var err error
	s.once.Do(func() {
		close(s.closed)
		// 给单写协程一点时间排空队列。
		time.Sleep(10 * time.Millisecond)
		err = s.db.Close()
	})
	return err
}
