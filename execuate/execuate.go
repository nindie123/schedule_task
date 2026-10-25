package execuate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"task/module"
	"time"

	"github.com/jmoiron/sqlx"
)

// dbOpTimeout 是单次数据库尝试的超时上限。
// 注意：FinishExe 没有 ctx 形参，这里只能用 context.Background() 取超时，
// 因此无法向上传递取消，只能保证单次尝试不会永久挂起。
const dbOpTimeout = 2 * time.Second

// Executor 负责任务执行流程。
type Executor struct {
	DB *sqlx.DB
	// execute 是可替换的执行逻辑，nil 表示什么都不做（保持原行为）。
	// 抽成字段是为了测试时注入成功/失败场景；
	// 后续可以替换为按 task.Type 分发的注册表。
	execute func(*module.TaskExecution) error
}
type Execute interface {
	Exe(task *module.TaskExecution) error
}

func (this *Executor) Exe(task *module.TaskExecution) error {
	if this.execute == nil {
		return nil
	}
	return this.execute(task)
}

func NewExecutor(db *sqlx.DB) *Executor {
	return &Executor{
		DB: db,
	}
}

func (this *Executor) init(task *module.Task) (*module.TaskExecution, error) {
	exeTask := this.StartTask(task)
	err := this.InsertSql(exeTask)
	if err != nil {
		return exeTask, err
	}
	return exeTask, nil

}
func (this *Executor) Exec(task *module.Task) error {
	exeTask, initErr := this.init(task)
	if initErr != nil {
		return fmt.Errorf(
			"init task %d execution: %w",
			task.ID,
			initErr,
		)
	}
	exeErr := this.Exe(exeTask)
	if exeErr != nil {
		exeTask.Status = module.ExecutionFailed
		now := time.Now()
		exeTask.FinishedAt = &now
		msg := exeErr.Error()

		exeTask.ErrorMessage = &msg
		updateErr := this.FinishExe(exeTask)
		if updateErr != nil {
			fmtErr := fmt.Errorf(
				"exe task %d execution: %w",
				task.ID,
				exeErr,
			)
			res := errors.Join(updateErr, fmtErr)
			return res
		}
		return fmt.Errorf(
			"exe task %d execution: %w",
			task.ID,
			exeErr,
		)
	}

	now := time.Now()
	exeTask.FinishedAt = &now
	exeTask.Status = module.ExecutionSuccess
	update_err := this.FinishExe(exeTask)

	if update_err != nil {
		return fmt.Errorf(
			"update task %d execution: %w",
			task.ID,
			update_err,
		)
	}

	return nil

}

// 先把任务插入任务初始化
func (this *Executor) StartTask(task *module.Task) *module.TaskExecution {
	execution := module.TaskExecution{
		TaskID:       task.ID,
		Status:       module.ExecutionRunning,
		StartedAt:    time.Now(),
		FinishedAt:   nil,
		ErrorMessage: nil,
	}
	return &execution
}

// 把任务插入任务执行表，方便管理
// 但是这种方法在频繁访问数据库，所以感觉会很有问题
func (this *Executor) InsertSql(execution *module.TaskExecution) error {
	const sql = `INSERT INTO execuate_tasks
				(task_id,status,started_at)
				VALUES (?,?,?)`

	ctx, cancel := context.WithTimeout(
		context.Background(),
		1*time.Minute,
	)

	result, err := this.DB.ExecContext(
		ctx,
		sql,
		execution.TaskID,
		execution.Status,
		execution.StartedAt,
	)
	defer cancel()
	if err != nil {
		fmt.Printf("insert execution failed: %v\n", err)
		return err
	}
	id, err := result.LastInsertId()
	if err != nil {
		println("find id is error :", err)
		return err
	}
	execution.ID = uint(id)
	return nil

}

// 任务执行完成后的，插入数据库操作
// 更新任务执行记录
//
// 这是 UPDATE ... WHERE id = ?，写入的是绝对值，天然幂等，
// 所以可以对瞬时故障安全重试。
// （InsertSql 是 INSERT，不幂等，故刻意不加重试。）
func (this *Executor) FinishExe(task *module.TaskExecution) error {
	const query = `
        UPDATE execuate_tasks
        SET error_message = ?,
            status = ?,
            finished_at = ?
        WHERE id = ?
    `
	//防止sql插入错误信息大于255，引起数据库错误
	//没有这个if判断，会导致成功路径发生错误
	if task.ErrorMessage != nil {
		runes := []rune(*task.ErrorMessage)
		if len(runes) > 255 {
			*task.ErrorMessage = string(runes[:255])
		}
	}

	var rows int64
	err := defaultRetry.Do(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), dbOpTimeout)
		defer cancel()

		result, err := this.DB.ExecContext(
			ctx,
			query,
			task.ErrorMessage,
			task.Status,
			task.FinishedAt,
			task.ID,
		)
		if err != nil {
			return err
		}

		rows, err = result.RowsAffected()
		return err
	})

	if err != nil {
		return fmt.Errorf("update execution failed: %w", err)
	}

	if rows == 0 {
		// 如果未更新任何一行，说明没找到该任务，或该行已处于目标状态。
		// MySQL 返回的是实际改变的行数而非匹配行数：若上一次尝试其实已在
		// 服务端写入成功、只是响应丢失，这次重试写相同的值就会得到 0 行。
		// 因此回读确认，避免把一次成功的写入误报成"记录不存在"。
		applied, verr := this.isFinishApplied(task)
		if verr != nil {
			return verr
		}
		if !applied {
			return fmt.Errorf("execution record not found: id=%d", task.ID)
		}
	}

	return nil
}

// isFinishApplied 回读确认目标执行记录是否已经处于目标状态。
// 返回 (false, nil) 表示记录不存在；返回 error 表示回读本身失败。
func (this *Executor) isFinishApplied(task *module.TaskExecution) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dbOpTimeout)
	defer cancel()

	// 只取需要的两列，避免把未查询的字段一起拉进来。
	var got struct {
		Status     module.ExecutionStatus `db:"status"`
		FinishedAt *time.Time             `db:"finished_at"`
	}

	err := this.DB.GetContext(
		ctx,
		&got,
		`SELECT status, finished_at FROM execuate_tasks WHERE id = ?`,
		task.ID,
	)

	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("verify execution failed: %w", err)
	}

	return got.Status == task.Status && got.FinishedAt != nil, nil
}
