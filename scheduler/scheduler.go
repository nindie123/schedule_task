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
		execuater.Exec(&task)

	}
}

// findDueTasks 从 MySQL 查询所有已经到执行时间的任务。
func (s *Scheduler) findDueTasks() ([]module.Task, error) {
	var tasks []module.Task

	selectquery := `
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
		WHERE status = ?
		  AND next_run_time <= NOW()
	`

	err := config.DB.Select(&tasks, selectquery, module.TaskEnabled)
	if err != nil {
		return nil, err
	}
	//下面增加的逻辑是修改到期任务下次到期时间，方式后续同一个任务重复进入执行任务表格
	updatequery := `UPDATE tasks SET next_run_time = DATE_ADD(NOW(),INTERVAL 10 MINUTE) WHERE ID = ?`
	for _, task := range tasks {
		_, err := config.DB.Exec(updatequery, task.ID)
		if err != nil {
			fmt.Println("修改下一次到期时间错误：", err)
		}
	} //暂时没有经过测试

	return tasks, nil
}
