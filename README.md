# schedule_task

> 为了进一步掌握 Go 语言，开始做一个分布式任务调度系统，并慢慢迭代。
> 从最简单的"上传任务到数据库"起步，逐步补齐调度、执行、容错等能力。

当前是**单机版**，已跑通「HTTP 接口管理任务 → 调度器轮询到期任务 → 执行器执行并记录结果」这条主链路。
分布式能力（多实例协调、任务分片）尚未开始。

---

## 项目结构

```
task/
├── main.go                  程序入口：初始化 DB → 启动调度器 → 启动 HTTP 服务
├── go.mod / go.sum          模块定义与依赖锁定
│
├── config/
│   └── database.go          MySQL 连接初始化，导出全局 *sqlx.DB
│
├── module/
│   └── task.go              数据模型：Task（任务表）、TaskExecution（执行记录表）、状态常量
│
├── router/
│   └── router.go            gin 路由注册，统一挂载在 /api 下
│
├── handler/
│   └── task.go              HTTP 处理函数：创建任务、查询任务列表、停用任务
│
├── scheduler/
│   └── scheduler.go         调度器：按固定周期扫描到期任务，原子认领后交给执行器
│
├── migrations/
│   └── 001_add_max_runs.sql 数据库迁移：max_runs 列与执行记录统计索引
│
└── execuate/                执行器（目录名为历史拼写，正确拼写应为 execute）
    ├── execuate.go          执行流程：写执行记录 → 执行 → 回写结果
    ├── retry.go             数据库写入的重试策略与 MySQL 错误分类
    ├── retry_test.go        retry.go 的单元测试
    └── error_test.go        Exec/FinishExe 落库行为的单元测试（fake driver）
```

### 各层职责

| 层 | 包 | 职责 |
|---|---|---|
| 接入层 | `router` / `handler` | 解析 HTTP 请求、参数校验、返回 JSON |
| 模型层 | `module` | 表结构映射与状态常量 |
| 调度层 | `scheduler` | 周期性发现"到期该跑"的任务 |
| 执行层 | `execuate` | 记录执行开始、执行任务、回写执行结果 |
| 基础设施 | `config` | 数据库连接 |

---

## 技术栈

| 组件 | 选型 | 说明 |
|---|---|---|
| 语言 | Go 1.25 | |
| Web 框架 | gin v1.12.0 | 路由与 JSON 序列化 |
| 数据库 | MySQL 8.0 | |
| DB 访问 | sqlx v1.4.0 | 在 database/sql 上增加结构体映射 |
| 驱动 | go-sql-driver/mysql v1.10.1 | |

---

## 数据库表结构

> 目前没有版本化的建表脚本，表结构靠手工维护（见「目前存在的问题」第 12 条）。

### `tasks` —— 任务定义表

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | bigint PK AUTO_INCREMENT | 主键 |
| `name` | varchar(128) NOT NULL | 任务名 |
| `type` | varchar(64) NOT NULL | 任务类型，如 `cron`、`ICU_CHECK`（**目前未被使用**） |
| `interval_seconds` | int NOT NULL | 执行间隔（秒） |
| `max_runs` | int NULL | 最大成功执行次数；`NULL` 表示不限，`1` 表示只执行一次 |
| `status` | varchar(32) NOT NULL | `ENABLED` / `DISABLED` / `COMPLETED` |
| `created_at` | datetime NOT NULL | 创建时间，DB 自动生成 |
| `updated_at` | datetime NOT NULL | 更新时间，DB `ON UPDATE` 自动维护 |
| `next_run_time` | datetime NULL | 下次执行时间 |

### `execuate_tasks` —— 执行记录表

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | bigint PK AUTO_INCREMENT | 主键 |
| `task_id` | bigint NOT NULL | 关联 `tasks.id` |
| `status` | varchar(32) NOT NULL | `RUNNING` / `SUCCESS` / `FAILED` |
| `started_at` | datetime NOT NULL | 开始时间 |
| `finished_at` | datetime NULL | 结束时间 |
| `error_message` | varchar(255) NULL | 失败原因 |

---

## 已实现的接口

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/tasks` | 创建任务 |
| GET | `/api/tasks` | 查询任务列表（按 id 倒序） |
| PUT | `/api/tasks/:id/disable` | 停用任务 |

**创建任务请求体示例**

```json
{
  "name": "icu-data-check",
  "type": "ICU_CHECK",
  "interval": 10,
  "max_runs": 3,
  "next_run_time": "2026-10-20T10:00:00Z"
}
```

---

## 主流程

```
HTTP 请求 ──> handler ──> MySQL(tasks)
                              │
              scheduler 每 2 秒轮询 ──┐
                              │       │
                    查到到期任务 ──────┘
                              │
                    改下次执行时间
                              │
                    execuate.Executor.Exec
                              │
              ┌───────────────┼───────────────┐
        写 RUNNING 记录     执行任务      回写执行结果
        (execuate_tasks)                (status/finished_at)
```

---

## 问题清单（含已修复项）

### P1 —— 会导致数据错误或功能整体失效

1. ~~**调度器"取到期任务 + 改下次时间"不在事务内，且 UPDATE 失败只打印日志**~~
   **已于 2026-10-20 修复**：`findDueTasks` 改为逐条原子认领。`claim` 的 `UPDATE ... WHERE next_run_time <= NOW()` 在并发下只有一条能推进时间，认领失败的任务本轮跳过，不会重复执行。

2. ~~**下次执行时间硬编码 `INTERVAL 10 MINUTE`，完全忽略 `interval_seconds`**~~
   **已于 2026-10-20 修复**：改为 `DATE_ADD(NOW(), INTERVAL interval_seconds SECOND)`。基准取 `NOW()` 意味着停机期间错过的执行会被跳过，不做补跑。

3. ~~**`next_run_time` 为 NULL 会导致调度器整体停摆**~~
   **已于 2026-10-20 修复**：`Task.Next_time` 改为 `*time.Time`；调度查询增加 `next_run_time IS NOT NULL`。任务完成后该列会被置为 `NULL`。

### P2 —— 特定条件下的功能或健壮性问题

4. **调度循环被同步执行阻塞（仍未修复）**
   `scan()` 里同步调用 `Exec`。执行器目前是空实现（见第 5 条），所以暂时不会阻塞；一旦接入真实业务执行，多个到期任务会串行阻塞，`time.Ticker` 的 channel 容量为 1，阻塞期间的 tick 会被丢弃，扫描周期实际远大于设定的 2 秒。

5. **执行器仍是占位实现，`task.Type` 完全未被使用**
   `Executor` 新增了可注入的 `execute func(*module.TaskExecution) error` 字段，但 `NewExecutor` 不会设置它，包外也无法赋值，因此生产路径上 `Exe` 恒返回 `nil`：写入一条 `RUNNING` 记录后立即置为 `SUCCESS`。无论 `type` 是 `cron` 还是 `ICU_CHECK` 都走同一条路 —— 真正的任务执行逻辑还没开始写。

6. **孤儿 RUNNING 记录无回收机制**
   进程被杀或写库失败时留下的 `RUNNING` 记录永远不会收敛。当前库里已累积 **34 条**孤儿 RUNNING 记录。

7. **无优雅关闭 / 无信号处理**
   `main.go` 里 `ctx` 是 `context.Background()`，永不取消；收到 SIGINT 直接杀进程，正在执行的任务永远停在 RUNNING（与第 6 条同源）。

8. **时区语义不一致**
   连接串用 `loc=Local` 让驱动按本地时区转换 Go 的 `time.Time`，而 SQL 里的 `NOW()` 取的是 MySQL 服务器时区。两者不一致时，任务会提前或延迟数小时触发。（本机实测两者当前一致，属潜在风险。）

9. ~~**`Interval` 无下界校验**~~
   **已于 2026-10-20 修复**：创建接口改为 `binding:"required,gt=0"`，调度查询也会排除 `interval_seconds <= 0` 的行。注意：数据库层没有 `CHECK` 约束，直接写库仍可绕过。

10. **`InsertSql` 的写入失败会丢失整条执行记录**
    若 `INSERT` 服务端已提交、只是响应丢失，客户端会认为失败；若重试则会产生重复行。目前**刻意不重试**（INSERT 不幂等），这是已知取舍。

### P3 —— 可读性与工程化

11. `tasks` 表仍缺少 `(status, next_run_time)` 索引，调度查询每 2 秒全表扫描。`execuate_tasks` 的 `(task_id, status)` 索引已于 2026-10-20 补上，用于统计 `max_runs` 成功次数。
12. **没有版本化的建表脚本**，表结构靠手工维护，换环境无法复现。
13. `execuate.go` 中定义的 `Execute` 接口从未被使用。
14. 命名问题：包名 `execuate`（应为 `execute`）、`module`（通常叫 `model`）、字段 `Next_time`（应为 `NextTime`）、receiver 用 `this`（Go 惯用短名）。
15. JSON tag 不一致：入参是 `next_run_time`，出参是 `next_runtime`。
16. ~~回读用 `SELECT *`~~ **当前代码已改为显式列清单**（2026-10-23 审查确认）：handler 的四处回读与 `isFinishApplied` 都只查询所需列。
17. `GET /api/tasks` 仍无分页。无数据时返回 `null` 的问题已修复，当前返回 `[]`。
18. 数据库凭据硬编码在 `config/database.go`。
19. 日志全部使用 `fmt.Println` / `println`，无级别、无结构化。
20. ~~`Exec` 中 `update_err` 优先于 `exe_err` 返回，两者都非空时 `exe_err` 被吞掉。~~
    **已修复**：改用 `errors.Join` 同时返回两个错误，并由 `TestExec_ExecutionAndFinishBothFail` 覆盖。

### 2026-10-23 代码审查新增发现

以下问题在本次审查中发现，均**未修改代码**，仅记录：

21. **执行时长不受 interval 约束**
    认领时按 `DATE_ADD(NOW(), INTERVAL interval_seconds SECOND)` 推进下次执行时间。若单次执行超过 `interval_seconds`，执行结束后的下一轮扫描会立刻再次认领该任务：单机串行执行不会并发，但配置的间隔实际失效；多实例部署时则可能并发执行同一任务。修复方向是引入租约/心跳（见 P2-6、P2-7）。

22. **`init` 失败路径不落库**
    `InsertSql` 失败时，`Exec` 只修改内存中的 `TaskExecution` 后返回错误，不会写任何记录。若 INSERT 服务端已提交、只是响应或 `LastInsertId` 返回失败，该行会永久停在 `RUNNING`，与 P2-6 的孤儿记录同源。

23. **`execute` 字段在包外不可注入**
    `execute` 未导出，`NewExecutor` 也不给它赋值，`scheduler` 又是每个任务新建一个 `Executor`。该字段目前只服务于单元测试；后续按 `task.Type` 分发时，需要改为构造时注入或使用注册表。

24. **编译产物 `main`（约 30 MB）被提交进仓库**
    `.gitignore` 只忽略了 `.claude/` 和 `CLAUDE.md`。建议后续把 `main` 加入忽略列表并从版本库移除（需要单独授权）。

25. **测试注释过期**
    `execuate/error_test.go` 文件头仍写"三个场景"（现为 5 个）；`TestExec_SuccessResult` 的注释仍称 `Exe` 内含 5 秒 `Sleep`（现为 `nil` 立即返回）。属注释问题，未改动代码。

26. **`InitDB` 不会真正连接数据库**
    `sqlx.Open` 只校验 DSN，不会建立连接。凭据错误或 MySQL 未启动时，`InitDB` 仍返回 `nil` 并打印 "MySQL connect success"，问题要到第一次查询才暴露。另外 `InsertSql` 使用的是不带 context 的 `DB.Exec`，没有超时保护，数据库挂起时会一直阻塞调度循环。建议增加 `PingContext` 与连接池参数（需要单独授权）。

27. **`FinishExe` 的部分分支没有测试覆盖**
    fake driver 的 `RowsAffected` 固定返回 1，因此 `rows == 0 → isFinishApplied` 这条回读分支未被测试；"前两次瞬时失败、第三次成功"的 `FinishExe` 重试集成场景同样没有覆盖（`retry_test.go` 只测了策略本身）。

---

## 未来优化方向

### 第一阶段：让单机版跑稳

1. ~~**原子认领到期任务**~~ **已完成 2026-10-20**
   `findDueTasks` 查询到期任务后逐条执行 `claim`，用 `RowsAffected() == 1` 判断认领成功；`UPDATE` 的 `WHERE` 中重复检查 `next_run_time <= NOW()`，并发实例只有一个能推进时间。

2. ~~**用 `interval_seconds` 计算下次执行时间**~~ **已完成 2026-10-20**
   当前采用 `DATE_ADD(NOW(), INTERVAL interval_seconds SECOND)`，即**跳过停机期间错过的执行**。若后续需要补跑或严格保持节奏，需要再定义策略。

3. ~~**修复 `next_run_time` 的 NULL 问题**~~ **已完成 2026-10-20**
   字段改为 `*time.Time`，调度查询增加 `next_run_time IS NOT NULL`。

4. **按 `task.Type` 分发到具体执行器**（补 P2-5）
   用注册表或工厂模式，把 `cron` / `ICU_CHECK` 等类型映射到各自的执行实现。这是让项目从"调度框架"变成"能真正干活"的关键一步。

5. **并发执行 + worker pool**（修 P2-4）
   执行改为异步，并限制并发上限，避免无界 goroutine。

6. **心跳 / 租约 + 启动回收**（修 P2-6、P2-7）
   给执行记录加心跳字段，进程启动时把超时的 RUNNING 判定为宿主已死并标记 `FAILED`；同时加 `signal.NotifyContext` 做优雅关闭。

### 第二阶段：走向分布式

7. **引入 Redis 做调度协调**
   注意分工：Redis 负责**协调**，不负责**持久化执行结果**（它的持久化语义弱于 MySQL，且会引入双写一致性问题）。
   - 任务认领锁：`SET task:claim:{id} {worker} NX EX 30`
   - Worker 心跳：`SET worker:{id}:hb 1 EX 15`
   - 延迟队列：用 ZSET 替代每 2 秒轮询 MySQL，大幅降低数据库压力

8. **任务分片与多实例部署**，配合第 1 条的原子认领。

### 第三阶段：工程化

9. 补齐测试（目前只有 `execuate` 包的 `retry_test.go` 与 `error_test.go`；`scheduler` 与 `handler` 尚无测试）。
10. 配置外置：DSN 从环境变量读取，不硬编码凭据。
11. 引入结构化日志（`log/slog`）与基础指标。
12. 统一命名与目录拼写，补齐建表脚本与索引。
13. 补齐任务更新接口：允许修改 `max_runs`、恢复 `COMPLETED` 任务、重新启用已停用任务。

---

## 更新日志

### 2026-10-23
- 执行逻辑抽成可注入的 `Executor.execute` 字段；`nil` 时保持原行为（`Exe` 直接返回 `nil`），便于测试注入成功/失败场景
- `Exec` 在"执行失败 + 结果落库失败"时通过 `errors.Join` 同时返回两个错误（原 P3-20）
- 新增 `execuate/error_test.go`：覆盖成功、失败、超长错误信息、执行报错、执行与落库同时失败五个场景
- 按实际代码复核 README：更新执行器现状、P3-16/P3-17/P3-20 的修复状态，新增 7 条审查发现

### 2026-10-20
- 任务增加 `max_runs`：最大成功执行次数，`NULL` 表示不限，`1` 表示只执行一次
- 成功次数由 `COUNT(execuate_tasks WHERE status='SUCCESS')` 实时聚合，不维护冗余计数字段，避免重试造成计数漂移
- 调度器改为原子认领：`UPDATE ... WHERE next_run_time <= NOW()`，并发实例只有一个能推进时间，修复重复执行
- 下次执行时间改用 `interval_seconds` 计算，不再硬编码 10 分钟；停机期间错过的执行直接跳过
- 达到 `max_runs` 的任务自动置为 `COMPLETED`，并把 `next_run_time` 置为 `NULL`
- `Task.Next_time` 改为 `*time.Time`，修复 `next_run_time` 为 NULL 时整条查询失败的问题
- 创建任务接口增加 `max_runs` 参数，`interval` 增加 `gt=0` 校验
- 新增 `migrations/001_add_max_runs.sql`，并已在本机 `task_scheduler` 库执行
- 已知数据问题（本次未处理）：34 条孤儿 `RUNNING` 记录、12 条小写 `success` 记录（不会被 `max_runs` 统计）

### 2026-10-19
- 为执行结果的数据库写入增加重试：`execuate/retry.go` 新增可注入的重试策略与 MySQL 错误分类白名单
- `FinishExe` 遇到瞬时错误（死锁、锁等待超时、连接中断等）自动重试最多 3 次，单次尝试超时 2 秒
- `FinishExe` 在 `RowsAffected() == 0` 时增加回读校验，避免把"重试时写相同值"误报成记录不存在
- 调度器不再丢弃执行器返回的错误，改为打印
- 新增 `execuate/retry_test.go`
- 整理本 README，补充项目结构、现存问题与优化方向

### 2026-09-23
- 更新调度器逻辑，以及部分执行器逻辑
- 已知问题：调度器会重复提交同一个定时器，导致执行记录表里都是重复任务
- 性能问题：执行记录是一条一条插入，未做批处理

### 2026-09-21
- 简单实现上传任务到数据库的逻辑，初步使用 gin 框架
