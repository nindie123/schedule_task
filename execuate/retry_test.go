package execuate

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

// noSleep 让重试不产生真实等待，使测试不依赖退避时长。
func noSleep(time.Duration) {}

// fakeNetError 模拟网络层超时错误。
type fakeNetError struct{}

func (fakeNetError) Error() string   { return "i/o timeout" }
func (fakeNetError) Timeout() bool   { return true }
func (fakeNetError) Temporary() bool { return true }

func TestRetryPolicy_FirstAttemptSucceeds(t *testing.T) {
	calls := 0
	p := RetryPolicy{MaxAttempts: 3, Sleep: noSleep}

	err := p.Do(func() error {
		calls++
		return nil
	})

	if err != nil {
		t.Fatalf("期望成功，实际 err=%v", err)
	}
	if calls != 1 {
		t.Fatalf("op 调用次数 = %d，期望 1", calls)
	}
}

func TestRetryPolicy_SucceedsAfterRetries(t *testing.T) {
	calls := 0
	p := RetryPolicy{MaxAttempts: 3, Sleep: noSleep}

	err := p.Do(func() error {
		calls++
		if calls < 3 {
			return errors.New("临时故障")
		}
		return nil
	})

	if err != nil {
		t.Fatalf("期望最终成功，实际 err=%v", err)
	}
	if calls != 3 {
		t.Fatalf("op 调用次数 = %d，期望 3", calls)
	}
}

func TestRetryPolicy_ExhaustsAttempts(t *testing.T) {
	calls := 0
	p := RetryPolicy{MaxAttempts: 3, Sleep: noSleep}

	err := p.Do(func() error {
		calls++
		return fmt.Errorf("第 %d 次失败", calls)
	})

	if err == nil {
		t.Fatal("期望返回错误，实际为 nil")
	}
	if calls != 3 {
		t.Fatalf("op 调用次数 = %d，期望 3", calls)
	}
	// 耗尽后应返回最后一次的错误，而不是第一次的。
	if got, want := err.Error(), "第 3 次失败"; got != want {
		t.Fatalf("应返回最后一次错误 %q，实际 %q", want, got)
	}
}

func TestRetryPolicy_NonRetryableStopsImmediately(t *testing.T) {
	calls := 0
	sentinel := errors.New("语法错误")
	p := RetryPolicy{
		MaxAttempts: 3,
		IsRetryable: func(error) bool { return false },
		Sleep: func(time.Duration) {
			t.Fatal("不可重试的错误不应触发退避等待")
		},
	}

	err := p.Do(func() error {
		calls++
		return sentinel
	})

	if !errors.Is(err, sentinel) {
		t.Fatalf("期望返回 sentinel，实际 err=%v", err)
	}
	if calls != 1 {
		t.Fatalf("op 调用次数 = %d，期望 1（不可重试应立即返回）", calls)
	}
}

func TestRetryPolicy_OnRetryCallback(t *testing.T) {
	var attempts []int
	p := RetryPolicy{
		MaxAttempts: 3,
		BaseDelay:   10 * time.Millisecond,
		MaxDelay:    100 * time.Millisecond,
		Sleep:       noSleep,
		OnRetry: func(attempt int, err error, delay time.Duration) {
			attempts = append(attempts, attempt)
		},
	}

	_ = p.Do(func() error { return errors.New("失败") })

	// 3 次尝试 = 2 次重试，attempt 应为 1、2。
	if len(attempts) != 2 {
		t.Fatalf("OnRetry 调用次数 = %d，期望 2", len(attempts))
	}
	if attempts[0] != 1 || attempts[1] != 2 {
		t.Fatalf("OnRetry attempt 序列 = %v，期望 [1 2]", attempts)
	}
}

func TestRetryPolicy_MaxAttemptsBelowOne(t *testing.T) {
	calls := 0
	p := RetryPolicy{MaxAttempts: 0, Sleep: noSleep}

	err := p.Do(func() error {
		calls++
		return errors.New("失败")
	})

	if err == nil {
		t.Fatal("期望返回错误")
	}
	if calls != 1 {
		t.Fatalf("op 调用次数 = %d，期望 1（MaxAttempts<1 应按 1 次处理）", calls)
	}
}

func TestRetryPolicy_BackoffGrowsAndCaps(t *testing.T) {
	p := RetryPolicy{BaseDelay: 50 * time.Millisecond, MaxDelay: 500 * time.Millisecond}

	cases := []struct {
		attempt int
		cap     time.Duration
	}{
		{1, 50 * time.Millisecond},
		{2, 100 * time.Millisecond},
		{3, 200 * time.Millisecond},
		{4, 400 * time.Millisecond},
		{5, 500 * time.Millisecond}, // 800ms 被 MaxDelay 截断
		{20, 500 * time.Millisecond},
	}

	for _, c := range cases {
		// full jitter 使结果落在 [0, cap]，多采样几次确认不越界。
		for i := 0; i < 200; i++ {
			got := p.backoff(c.attempt)
			if got < 0 || got > c.cap {
				t.Fatalf("backoff(%d) = %s，期望落在 [0, %s]", c.attempt, got, c.cap)
			}
		}
	}
}

func TestRetryPolicy_BackoffZeroBase(t *testing.T) {
	p := RetryPolicy{BaseDelay: 0}

	if got := p.backoff(1); got != 0 {
		t.Fatalf("BaseDelay=0 时 backoff = %s，期望 0", got)
	}
}

func TestIsRetryableDBError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"死锁 1213", &mysql.MySQLError{Number: 1213}, true},
		{"锁等待超时 1205", &mysql.MySQLError{Number: 1205}, true},
		{"连接数满 1040", &mysql.MySQLError{Number: 1040}, true},
		{"唯一键冲突 1062", &mysql.MySQLError{Number: 1062}, false},
		{"语法错误 1064", &mysql.MySQLError{Number: 1064}, false},
		{"字段超长 1406", &mysql.MySQLError{Number: 1406}, false},
		{"ErrBadConn", driver.ErrBadConn, true},
		{"网络超时", fakeNetError{}, true},
		{"io.EOF", io.EOF, true},
		{
			"被 %w 包装的死锁",
			fmt.Errorf("update execution failed: %w", &mysql.MySQLError{Number: 1213}),
			true,
		},
		{
			"被 %w 包装的语法错误",
			fmt.Errorf("update execution failed: %w", &mysql.MySQLError{Number: 1064}),
			false,
		},
		{"自定义业务错误", errors.New("execution record not found: id=1"), false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isRetryableDBError(c.err); got != c.want {
				t.Fatalf("isRetryableDBError(%v) = %v，期望 %v", c.err, got, c.want)
			}
		})
	}
}
