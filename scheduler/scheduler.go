package scheduler

import (
	"context"
	"fmt"
	"time"

	"task/config"
	"task/execuate"
	"task/module"
)

// Scheduler 负责发现到期任务。
// 它不负责具体的业务执行。
type Scheduler struct {
	interval time.Duration
}

// NewScheduler 创建调度器。
// interval 表示 Scheduler 多久扫描一次数据库。
func NewScheduler(interval time.Duration) *Scheduler {
	return &Scheduler{
		interval: interval,
	}
}

// Start 启动 Scheduler。
func (s *Scheduler) Start(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	fmt.Println("scheduler started")

	// 启动后立即扫描一次，而不是必须等第一个 ticker。
	s.scan()

	for {
		select {
		case <-ticker.C:
			s.scan()

		case <-ctx.Done():
			fmt.Println("scheduler stopped")
			return
		}
	}
}

// scan 查询当前已经到期的任务。
func (s *Scheduler) scan() {
	tasks, err := s.findDueTasks()
	if err != nil {
		fmt.Printf("find due tasks failed: %v\n", err)
		return
	}

	for _, task := range tasks {
		fmt.Printf(
			"find due task: id=%d name=%s next_run_time=%v\n",
			task.ID,
			task.Name,
			task.Next_time,
		)

		// V1 暂时只打印。
		// 下一步这里再交给 TaskExecutor：
		//
		// s.executor.Execute(task)

		execuater := execuate.NewExecutor(config.DB)
		for _, task := range tasks {
			execuater.Exec(&task)
		} //这里一条一条插入，性能有点差

	}
}

// findDueTasks 从 MySQL 查询所有已经到执行时间的任务。
func (s *Scheduler) findDueTasks() ([]module.Task, error) {
	var tasks []module.Task

	query := `
		SELECT
			id,
			name,
			type,
			interval_seconds,
			status,
			next_run_time,
			created_at,
			updated_at
		FROM tasks
		WHERE status = 'ENABLED'
		  AND next_run_time <= NOW()
	`

	err := config.DB.Select(&tasks, query)
	if err != nil {
		return nil, err
	}

	return tasks, nil
}
