package module

import "time"

type TaskStatus string
type ExecutionStatus string

// 后续完善其他功能的时候在进行进一步的更新
const (
	TaskEnabled TaskStatus = "ENABLED"
	// TaskDisabled 表示任务被手动停用，不会再次调度。
	TaskDisabled TaskStatus = "DISABLED"
	// TaskCompleted 表示重复任务已达到 max_runs 的成功执行次数，正常结束。
	TaskCompleted TaskStatus = "COMPLETED"
)

const (
	ExecutionRunning ExecutionStatus = "RUNNING"
	ExecutionSuccess ExecutionStatus = "SUCCESS"
	ExecutionFailed  ExecutionStatus = "FAILED"
)

// 最开始任务表
type Task struct {
	ID       uint   `db:"id" json:"id"`
	Name     string `db:"name" json:"name"`
	Type     string `db:"type" json:"type"`
	Interval int    `db:"interval_seconds" json:"interval"`
	// MaxRuns 是最大成功执行次数，nil 表示不限次数。
	// 失败不计入次数，因此停止判定统计的是执行记录中的 SUCCESS 行数。
	MaxRuns   *int       `db:"max_runs" json:"max_runs"`
	Status    TaskStatus `db:"status" json:"status"`
	CreatedAt time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt time.Time  `db:"updated_at" json:"updated_at"`
	// Next_time 使用指针，因为任务完成后该列会被置为 NULL。
	Next_time *time.Time `db:"next_run_time" json:"next_runtime"`
}

// 到达执行时间的任务表
type TaskExecution struct {
	ID           uint            `db:"id" json:"id"`
	TaskID       uint            `db:"task_id" json:"task_id"`
	Status       ExecutionStatus `db:"status" json:"status"`
	StartedAt    time.Time       `db:"started_at" json:"started_at"`
	FinishedAt   *time.Time      `db:"finished_at" json:"finished_at"`
	ErrorMessage *string         `db:"error_message" json:"error_message"`
}
