package execuate

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"time"

	"github.com/go-sql-driver/mysql"
)

// RetryPolicy 描述一次"可重试操作"的策略。
// Sleep、IsRetryable、OnRetry 都做成可注入的，便于单元测试。
type RetryPolicy struct {
	// MaxAttempts 是总尝试次数（含第一次）。小于 1 时按 1 处理。
	MaxAttempts int
	// BaseDelay 是第一次重试前的等待时长，之后按指数增长。
	BaseDelay time.Duration
	// MaxDelay 是单次退避的上限，0 表示不设上限。
	MaxDelay time.Duration
	// IsRetryable 判断错误是否值得重试；nil 表示所有错误都重试。
	IsRetryable func(error) bool
	// Sleep 默认 time.Sleep；测试注入 no-op 可避免真实等待。
	Sleep func(time.Duration)
	// OnRetry 在每次重试前调用，用于记录日志。
	OnRetry func(attempt int, err error, delay time.Duration)
}

// Do 执行 op，失败时按策略重试。
// 成功立即返回 nil；遇到不可重试的错误立即返回；
// 尝试次数耗尽后返回最后一次的错误。
func (p RetryPolicy) Do(op func() error) error {
	attempts := p.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}

	sleep := p.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}

	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		err = op()
		if err == nil {
			return nil
		}
		// 最后一次尝试失败后不再退避，直接返回。
		if attempt == attempts {
			break
		}
		if p.IsRetryable != nil && !p.IsRetryable(err) {
			return err
		}

		delay := p.backoff(attempt)
		if p.OnRetry != nil {
			p.OnRetry(attempt, err, delay)
		}
		sleep(delay)
	}

	return err
}

// backoff 返回第 attempt 次重试前的等待时长（attempt 从 1 开始）。
// 先按 BaseDelay 指数增长、用 MaxDelay 截断，再取 [0, d] 的随机抖动。
// full jitter 是为了避免多个任务同时重试造成惊群。
func (p RetryPolicy) backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}

	d := p.BaseDelay
	for i := 1; i < attempt; i++ {
		// 提前用 MaxDelay 截断，顺带避免位移溢出。
		if p.MaxDelay > 0 && d >= p.MaxDelay {
			break
		}
		d *= 2
	}
	if p.MaxDelay > 0 && d > p.MaxDelay {
		d = p.MaxDelay
	}
	if d <= 0 {
		return 0
	}

	return time.Duration(rand.Int63n(int64(d) + 1))
}

// retryableMySQLCodes 是"瞬时故障、可安全重试"的 MySQL 错误码白名单。
// go-sql-driver 不导出 ER_* 常量，只能按数字比对。
var retryableMySQLCodes = map[uint16]struct{}{
	1040: {}, // ER_CON_COUNT_ERROR     连接数已满
	1053: {}, // ER_SERVER_SHUTDOWN     服务端正在关闭
	1152: {}, // ER_ABORTING_CONNECTION 连接被中止
	1205: {}, // ER_LOCK_WAIT_TIMEOUT   锁等待超时
	1213: {}, // ER_LOCK_DEADLOCK       死锁，MySQL 保证已回滚
	1317: {}, // ER_QUERY_INTERRUPTED   查询被中断
	1927: {}, // ER_CONNECTION_KILLED   连接被 kill
}

// isRetryableDBError 判断数据库错误是否值得重试。
// 采用白名单：只有明确瞬时且安全重试的错误才返回 true，
// 未知错误（语法错误、约束冲突等）一律不重试。
func isRetryableDBError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, driver.ErrBadConn) {
		return true
	}

	// errors.As 可穿透 fmt.Errorf("%w") 的包装。
	var myErr *mysql.MySQLError
	if errors.As(err, &myErr) {
		_, ok := retryableMySQLCodes[myErr.Number]
		return ok
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	if errors.Is(err, io.EOF) {
		return true
	}

	return false
}

// defaultRetry 是数据库写入使用的默认重试策略：
// 3 次尝试，50ms 起指数退避、封顶 500ms，单次尝试的超时由调用方控制。
var defaultRetry = RetryPolicy{
	MaxAttempts: 3,
	BaseDelay:   50 * time.Millisecond,
	MaxDelay:    500 * time.Millisecond,
	IsRetryable: isRetryableDBError,
	OnRetry: func(attempt int, err error, delay time.Duration) {
		fmt.Printf("db write attempt %d failed: %v; retry in %s\n", attempt, err, delay)
	},
}
