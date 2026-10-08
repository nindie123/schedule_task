package execuate

import (
	"fmt"
	"task/module"
	"time"

	"github.com/jmoiron/sqlx"
)

// Executor 负责任务执行流程。
type Executor struct {
	DB *sqlx.DB
}
type Execute interface {
	Exe(task *module.Task)
}

func (this *Executor) Exe(task *module.TaskExecution) error {
	time.Sleep(5 * time.Second)
	now := time.Now()
	task.FinishedAt = &now
	task.Status = "success"

	return nil

}

func NewExecutor(db *sqlx.DB) *Executor {
	return &Executor{
		DB: db,
	}
}

func (this *Executor) init(task *module.Task) *module.TaskExecution {
	exe_task := this.StartTask(task)
	err := this.InsertSql(exe_task)
	if err != nil {
		fmt.Println("init error : ", err)
	}
	return exe_task

}
func (this *Executor) Exec(task *module.Task) error {
	exe_task := this.init(task)
	exe_err := this.Exe(exe_task)

	update_err := this.FinishExe(exe_task)
	if update_err != nil {
		return update_err
	}

	if exe_err != nil {
		return exe_err
	}

	return nil

}

// 先把任务插入任务初始化
func (this *Executor) StartTask(task *module.Task) *module.TaskExecution {
	execution := module.TaskExecution{
		TaskID:       task.ID,
		Status:       "RUNNING",
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
	result, err := this.DB.Exec(
		sql,
		execution.TaskID,
		execution.Status,
		execution.StartedAt,
	)
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
func (this *Executor) FinishExe(task *module.TaskExecution) error {
	const query = `
        UPDATE execuate_tasks
        SET error_message = ?,
            status = ?,
            finished_at = ?
        WHERE id = ?
    `

	result, err := this.DB.Exec(
		query,
		task.ErrorMessage,
		task.Status,
		task.FinishedAt,
		task.ID,
	)

	if err != nil {
		return fmt.Errorf("update execution failed: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("get affected rows failed: %w", err)
	}

	if rows == 0 {
		return fmt.Errorf("execution record not found: id=%d", task.ID)
	}

	return nil
}
