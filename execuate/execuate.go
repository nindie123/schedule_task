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

func NewExecutor(db *sqlx.DB) *Executor {
	return &Executor{
		DB: db,
	}
}

func (this *Executor) Exec(task *module.Task) error {
	exe_task := this.StartTask(task)
	err := this.InsertSql(exe_task)
	if err != nil {
		return err
	}
	return nil

}

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
