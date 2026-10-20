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
	// 先收敛已达到 max_runs 的任务，避免它们继续参与调度。
	if err := s.completeExhaustedTasks(); err != nil {
		fmt.Printf("complete exhausted tasks failed: %v\n", err)
	}

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
		if err := execuater.Exec(&task); err != nil {
			fmt.Printf("exec task id=%d failed: %v\n", task.ID, err)
		}

	}

	// 本轮执行可能刚好达到 max_runs，立即收敛，不必等到下一个 tick。
	if err := s.completeExhaustedTasks(); err != nil {
		fmt.Printf("complete exhausted tasks failed: %v\n", err)
	}
}

// findDueTasks 查询到期任务并逐条原子认领。
// 只有认领成功的任务才会返回；认领失败（被其它实例抢走或已达到 max_runs）
// 的任务会被跳过，避免同一任务重复执行。
func (s *Scheduler) findDueTasks() ([]module.Task, error) {
	var candidates []module.Task

	selectquery := `
		SELECT
			id,
			name,
			type,
			interval_seconds,
			max_runs,
			status,
			next_run_time,
			created_at,
			updated_at
		FROM tasks
		WHERE status = ?
		  AND interval_seconds > 0
		  AND next_run_time IS NOT NULL
		  AND next_run_time <= NOW()
	`

	err := config.DB.Select(&candidates, selectquery, module.TaskEnabled)
	if err != nil {
		return nil, err
	}

	claimed := make([]module.Task, 0, len(candidates))
	for i := range candidates {
		ok, err := s.claim(&candidates[i])
		if err != nil {
			fmt.Printf("claim task id=%d failed: %v\n", candidates[i].ID, err)
			continue
		}
		if ok {
			claimed = append(claimed, candidates[i])
		}
	}

	return claimed, nil
}

// claim 原子地推进到期任务的下次执行时间。
// 返回 true 表示本次认领成功，调用方可以安全执行该任务。
//
// 关键点：
//   - 条件里再次检查 next_run_time <= NOW()：并发调度器中只有一个实例
//     能把时间推进到未来，其它实例会得到 0 行。
//   - max_runs 判定放在同一条 UPDATE 里，避免"查询时未达上限、执行时已超限"。
//   - 失败不计入次数，只统计 SUCCESS 执行记录，因此这里用 COUNT 而非计数字段，
//     判定天然幂等。
//   - 从 NOW() 起算而不是从 next_run_time 累加：停机期间错过的执行会被跳过。
func (s *Scheduler) claim(task *module.Task) (bool, error) {
	const query = `
		UPDATE tasks
		SET next_run_time = DATE_ADD(NOW(), INTERVAL interval_seconds SECOND)
		WHERE id = ?
		  AND status = ?
		  AND interval_seconds > 0
		  AND next_run_time IS NOT NULL
		  AND next_run_time <= NOW()
		  AND (
		        max_runs IS NULL
		        OR max_runs > (
		            SELECT COUNT(*)
		            FROM execuate_tasks e
		            WHERE e.task_id = ? AND e.status = ?
		        )
		      )
	`

	result, err := config.DB.Exec(
		query,
		task.ID,
		module.TaskEnabled,
		task.ID,
		module.ExecutionSuccess,
	)
	if err != nil {
		return false, err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows == 1, nil
}

// completeExhaustedTasks 把成功执行次数已达到 max_runs 的启用任务置为 COMPLETED。
// 成功次数始终由执行记录聚合得出，不维护冗余计数，避免重试或重复写入造成计数漂移。
func (s *Scheduler) completeExhaustedTasks() error {
	const query = `
		UPDATE tasks
		SET status = ?, next_run_time = NULL
		WHERE status = ?
		  AND max_runs IS NOT NULL
		  AND (
		        SELECT COUNT(*)
		        FROM execuate_tasks e
		        WHERE e.task_id = tasks.id AND e.status = ?
		      ) >= max_runs
	`

	_, err := config.DB.Exec(
		query,
		module.TaskCompleted,
		module.TaskEnabled,
		module.ExecutionSuccess,
	)
	return err
}
