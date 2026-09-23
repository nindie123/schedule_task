package module

import "time"

// 最开始任务表
type Task struct {
	ID        uint      `db:"id" json:"id"`
	Name      string    `db:"name" json:"name"`
	Type      string    `db:"type" json:"type"`
	Interval  int       `db:"interval_seconds" json:"interval"`
	Status    string    `db:"status" json:"status"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
	Next_time time.Time `db:"next_run_time" json:"next_runtime"`
}

// 到达执行时间的任务表
type TaskExecution struct {
	ID           uint       `db:"id" json:"id"`
	TaskID       uint       `db:"task_id" json:"task_id"`
	Status       string     `db:"status" json:"status"`
	StartedAt    time.Time  `db:"started_at" json:"started_at"`
	FinishedAt   *time.Time `db:"finished_at" json:"finished_at"`
	ErrorMessage *string    `db:"error_message" json:"error_message"`
}
