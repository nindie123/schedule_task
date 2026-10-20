-- 2026-10-20
-- 为任务增加最大成功执行次数。
-- max_runs 为 NULL 表示不限次数；否则成功执行 max_runs 次后自动置为 COMPLETED。
-- 注意：本脚本只能执行一次，MySQL 8.0 的 ADD COLUMN 不支持 IF NOT EXISTS。

ALTER TABLE tasks
    ADD COLUMN max_runs INT NULL COMMENT '最大成功执行次数，NULL 表示不限';

-- 支撑按 task_id + status 统计成功执行次数（max_runs 停止判定）。
CREATE INDEX idx_exec_task_status ON execuate_tasks (task_id, status);
