package execuate

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"task/module"

	"github.com/jmoiron/sqlx"
)

// 本文件验证执行结果落库的五个场景：
//
//  1. 执行返回 nil         → status=SUCCESS、finished_at 非空、error_message=NULL
//  2. FinishExe 写入失败结果 → status=FAILED、finished_at 非空、error_message 内容正确
//  3. 错误信息超过 255 字   → status=FAILED，落库值不超过 255 个字符且是合法 UTF-8
//  4. 执行返回错误          → Exec 落库 FAILED，并把原始错误返回给调用方
//  5. 执行与落库同时失败     → 两个错误都能通过 errors.Is 识别
//
// 测试不连接真实 MySQL：用一个只记录 SQL 参数的 fake driver，
// 断言的是"准备写入数据库的值"，即最终会进入 varchar(255) 列的值。

// errorMessageMaxRunes 对应 execuate_tasks.error_message 的 varchar(255)。
// varchar 的长度按字符（runes）计算，而不是字节。
const errorMessageMaxRunes = 255

// ---------- 极简记录型 database/sql driver ----------

type recordedExec struct {
	query string
	args  []driver.NamedValue
}

type recordedDB struct {
	mu        sync.Mutex
	execs     []recordedExec
	updateErr error // 非 nil 时，UPDATE 语句直接返回该错误
}

func (r *recordedDB) setUpdateError(err error) {
	r.mu.Lock()
	r.updateErr = err
	r.mu.Unlock()
}

func (r *recordedDB) getUpdateError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.updateErr
}

func (r *recordedDB) record(query string, args []driver.NamedValue) {
	// 复制参数切片，避免 database/sql 复用底层数组影响断言。
	cp := make([]driver.NamedValue, len(args))
	copy(cp, args)

	r.mu.Lock()
	r.execs = append(r.execs, recordedExec{query: query, args: cp})
	r.mu.Unlock()
}

func (r *recordedDB) lastExec(t *testing.T) recordedExec {
	t.Helper()

	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.execs) == 0 {
		t.Fatal("没有记录到任何数据库写入")
	}
	return r.execs[len(r.execs)-1]
}

type recordingDriver struct {
	rec *recordedDB
}

func (d *recordingDriver) Open(string) (driver.Conn, error) {
	return &recordingConn{rec: d.rec}, nil
}

type recordingConn struct {
	rec *recordedDB
}

func (c *recordingConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("recording driver 只支持 ExecContext")
}

func (c *recordingConn) Close() error { return nil }

func (c *recordingConn) Begin() (driver.Tx, error) {
	return nil, errors.New("recording driver 不支持事务")
}

func (c *recordingConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	// 先记录参数，保证 UPDATE 失败时也能断言"准备写入的值"。
	c.rec.record(query, args)

	if err := c.rec.getUpdateError(); err != nil && strings.Contains(strings.ToUpper(query), "UPDATE") {
		return nil, err
	}

	// 固定返回 1 行受影响，让 FinishExe 走正常路径，不触发回读确认。
	return recordingResult{}, nil
}

type recordingResult struct{}

func (recordingResult) LastInsertId() (int64, error) { return 1, nil }
func (recordingResult) RowsAffected() (int64, error) { return 1, nil }

// recordingDriverSeq 保证每个用例注册的 driver 名唯一（sql.Register 不允许重名）。
var recordingDriverSeq atomic.Int64

func newRecordingDB(t *testing.T) (*sqlx.DB, *recordedDB) {
	t.Helper()

	rec := &recordedDB{}
	name := fmt.Sprintf("execuate-recording-%d", recordingDriverSeq.Add(1))
	sql.Register(name, &recordingDriver{rec: rec})

	db, err := sqlx.Open(name, "")
	if err != nil {
		t.Fatalf("打开 fake 数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	return db, rec
}

// updateArgs 取出 FinishExe 发出的 UPDATE 参数，顺序与 SQL 占位符一致：
// error_message、status、finished_at、id。
func updateArgs(t *testing.T, e recordedExec) (errMsg, status, finishedAt, id driver.Value) {
	t.Helper()

	if !strings.Contains(strings.ToUpper(e.query), "UPDATE") {
		t.Fatalf("最后一条 SQL 不是 UPDATE: %s", e.query)
	}
	if len(e.args) != 4 {
		t.Fatalf("UPDATE 参数个数 = %d，期望 4，参数: %v", len(e.args), e.args)
	}

	return e.args[0].Value, e.args[1].Value, e.args[2].Value, e.args[3].Value
}

// ---------- 场景 1：执行返回 nil ----------

func TestExec_SuccessResult(t *testing.T) {
	db, rec := newRecordingDB(t)
	ex := NewExecutor(db)
	if err := ex.Exec(&module.Task{ID: 42}); err != nil {
		t.Fatalf("Exec 返回错误: %v", err)
	}

	errMsg, status, finishedAt, id := updateArgs(t, rec.lastExec(t))

	if status != string(module.ExecutionSuccess) {
		t.Errorf("status = %v，期望 %q", status, module.ExecutionSuccess)
	}
	if errMsg != nil {
		t.Errorf("error_message = %v，期望 NULL", errMsg)
	}
	if finishedAt == nil {
		t.Error("finished_at = NULL，期望非空")
	} else if _, ok := finishedAt.(time.Time); !ok {
		t.Errorf("finished_at 类型 = %T，期望 time.Time", finishedAt)
	}
	// InsertSql 的 LastInsertId 由 fake driver 固定返回 1。
	if id != int64(1) {
		t.Errorf("更新条件 id = %v，期望 1", id)
	}
}

// ---------- 场景 2：FinishExe 写入失败结果 ----------

// 这里直接构造 Exec 会写入的 TaskExecution，验证 FinishExe 本身把状态和错误信息落库；
// 通过 Exec 触发失败路径的场景见场景 4。
func TestFinishExe_FailedResult(t *testing.T) {
	db, rec := newRecordingDB(t)
	ex := NewExecutor(db)

	wantMsg := "执行失败：下游接口返回 500"
	now := time.Now()
	record := &module.TaskExecution{
		ID:           7,
		TaskID:       42,
		Status:       module.ExecutionFailed,
		StartedAt:    now.Add(-time.Second),
		FinishedAt:   &now,
		ErrorMessage: &wantMsg,
	}

	if err := ex.FinishExe(record); err != nil {
		t.Fatalf("FinishExe 返回错误: %v", err)
	}

	errMsg, status, finishedAt, id := updateArgs(t, rec.lastExec(t))

	if status != string(module.ExecutionFailed) {
		t.Errorf("status = %v，期望 %q", status, module.ExecutionFailed)
	}
	if got, ok := errMsg.(string); !ok || got != wantMsg {
		t.Errorf("error_message = %v，期望 %q", errMsg, wantMsg)
	}
	if finishedAt == nil {
		t.Error("finished_at = NULL，期望非空")
	}
	if id != int64(7) {
		t.Errorf("更新条件 id = %v，期望 7", id)
	}
}

// ---------- 场景 3：超长错误信息 ----------

func TestFinishExe_LongErrorMessage(t *testing.T) {
	db, rec := newRecordingDB(t)
	ex := NewExecutor(db)

	// 300 个中文字符：按字节算是 900 字节，按字符算已超过 varchar(255)。
	longMsg := strings.Repeat("错", 300)
	now := time.Now()
	record := &module.TaskExecution{
		ID:           8,
		TaskID:       42,
		Status:       module.ExecutionFailed,
		StartedAt:    now.Add(-time.Second),
		FinishedAt:   &now,
		ErrorMessage: &longMsg,
	}

	if err := ex.FinishExe(record); err != nil {
		t.Fatalf("FinishExe 返回错误: %v", err)
	}

	errMsg, status, finishedAt, _ := updateArgs(t, rec.lastExec(t))

	if status != string(module.ExecutionFailed) {
		t.Errorf("status = %v，期望 %q", status, module.ExecutionFailed)
	}
	if finishedAt == nil {
		t.Error("finished_at = NULL，期望非空")
	}

	got, ok := errMsg.(string)
	if !ok {
		t.Fatalf("error_message = %v，期望是一个字符串（被截断后落库）", errMsg)
	}
	if !utf8.ValidString(got) {
		t.Errorf("落库的 error_message 不是合法 UTF-8: %q", got)
	}
	if n := utf8.RuneCountInString(got); n > errorMessageMaxRunes {
		t.Errorf("落库的 error_message 长度 = %d 个字符，期望不超过 %d", n, errorMessageMaxRunes)
	}
	if got == "" {
		t.Error("落库的 error_message 为空，期望保留错误信息的前缀")
	}
}

// ---------- 场景 4：执行返回错误 ----------

func TestExec_ExecutionError(t *testing.T) {
	db, rec := newRecordingDB(t)
	sentinel := errors.New("下游接口不可用")

	ex := NewExecutor(db)
	ex.execute = func(*module.TaskExecution) error { return sentinel }

	err := ex.Exec(&module.Task{ID: 42})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Exec 返回的 error 应能识别 sentinel，实际: %v", err)
	}

	errMsg, status, finishedAt, id := updateArgs(t, rec.lastExec(t))

	if status != string(module.ExecutionFailed) {
		t.Errorf("status = %v，期望 %q", status, module.ExecutionFailed)
	}
	if got, ok := errMsg.(string); !ok || got != sentinel.Error() {
		t.Errorf("error_message = %v，期望 %q", errMsg, sentinel.Error())
	}
	if finishedAt == nil {
		t.Error("finished_at = NULL，期望非空")
	}
	// InsertSql 的 LastInsertId 由 fake driver 固定返回 1。
	if id != int64(1) {
		t.Errorf("更新条件 id = %v，期望 1", id)
	}
}

// ---------- 场景 5：执行和落库同时失败 ----------

func TestExec_ExecutionAndFinishBothFail(t *testing.T) {
	db, rec := newRecordingDB(t)
	exeErr := errors.New("execution failed")
	updateErr := errors.New("update failed")
	// 普通错误不可重试，FinishExe 会立即返回，不会触发退避等待。
	rec.setUpdateError(updateErr)

	ex := NewExecutor(db)
	ex.execute = func(*module.TaskExecution) error { return exeErr }

	err := ex.Exec(&module.Task{ID: 42})
	if !errors.Is(err, exeErr) {
		t.Fatalf("Exec 返回的 error 应能识别执行错误，实际: %v", err)
	}
	if !errors.Is(err, updateErr) {
		t.Fatalf("Exec 返回的 error 应能识别落库错误，实际: %v", err)
	}

	// driver 先记录参数再返回错误，因此仍能确认写入的是 FAILED 结果。
	errMsg, status, finishedAt, _ := updateArgs(t, rec.lastExec(t))

	if status != string(module.ExecutionFailed) {
		t.Errorf("status = %v，期望 %q", status, module.ExecutionFailed)
	}
	if got, ok := errMsg.(string); !ok || got != exeErr.Error() {
		t.Errorf("error_message = %v，期望 %q", errMsg, exeErr.Error())
	}
	if finishedAt == nil {
		t.Error("finished_at = NULL，期望非空")
	}
}
