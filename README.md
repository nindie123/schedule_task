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
│   ├── database.go          MySQL 连接初始化（Ping、连接池），导出全局 *sqlx.DB
│   └── database_test.go     初始化成功与连接失败路径的测试
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

> 目前没有版本化的建表脚本，表结构靠手工维护（见「问题清单」第 12 条）。

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

## 问题清单

### P2 —— 特定条件下的功能或健壮性问题

1. **调度循环被同步执行阻塞**
   `scan()` 里同步调用 `Exec`。执行器目前是空实现（见第 2 条），所以暂时不会阻塞；一旦接入真实业务执行，多个到期任务会串行阻塞，`time.Ticker` 的 channel 容量为 1，阻塞期间的 tick 会被丢弃，扫描周期实际远大于设定的 2 秒。

2. **执行器仍是占位实现，`task.Type` 完全未被使用**
   `Executor` 有可注入的 `execute func(*module.TaskExecution) error` 字段，但 `NewExecutor` 不会设置它，包外也无法赋值，因此生产路径上 `Exe` 恒返回 `nil`：写入一条 `RUNNING` 记录后立即置为 `SUCCESS`。无论 `type` 是 `cron` 还是 `ICU_CHECK` 都走同一条路 —— 真正的任务执行逻辑还没开始写。

3. **孤儿 RUNNING 记录无回收机制**
   进程被杀或写库失败时留下的 `RUNNING` 记录永远不会收敛。当前库里已累积 **34 条**孤儿 RUNNING 记录。

4. **无优雅关闭 / 无信号处理**
   `main.go` 里 `ctx` 是 `context.Background()`，永不取消；收到 SIGINT 直接杀进程，正在执行的任务永远停在 RUNNING（与第 3 条同源）。即使以后把取消信号传进来，Executor 的数据库操作也不会响应（见第 9 条）。

5. **时区语义不一致**
   连接串用 `loc=Local` 让驱动按本地时区转换 Go 的 `time.Time`，而 SQL 里的 `NOW()` 取的是 MySQL 服务器时区。两者不一致时，任务会提前或延迟数小时触发。（本机实测两者当前一致，属潜在风险。）

6. **`InsertSql` 的写入失败会丢失整条执行记录**
   `INSERT` 不幂等，因此刻意不重试。但 `InsertSql` 现在带 2 秒超时：若服务端已提交、只是响应超时或丢失，客户端会认为失败，该行却永久停在 `RUNNING`（与第 3 条同源）。这是当前明确的取舍。

7. **执行时长不受 interval 约束**
   认领时按 `DATE_ADD(NOW(), INTERVAL interval_seconds SECOND)` 推进下次执行时间。若单次执行超过 `interval_seconds`，执行结束后的下一轮扫描会立刻再次认领该任务：单机串行执行不会并发，但配置的间隔实际失效；多实例部署时则可能并发执行同一任务。修复方向是引入租约/心跳（见第 3、4 条）。

8. **`init` 失败路径不落库**
   `InsertSql` 返回错误时，`Exec` 直接返回错误，不会写任何记录。若 INSERT 服务端已提交、只是响应或 `LastInsertId` 失败，该行会永久停在 `RUNNING`（与第 6 条是同一问题的两个入口）。

9. **数据库操作无法响应关闭信号**
   `InsertSql`、`FinishExe`、`isFinishApplied` 都从 `context.Background()` 派生超时，只能保证单次操作不会永久挂起，无法响应调度器关闭信号；`Executor` 也没有接收 `ctx` 的入口。若后续实现优雅关闭，这部分需要一并改造。

10. **`InsertSql` 的超时常量重复硬编码**
    `InsertSql` 写死 `2*time.Second`，与 `execuate.go` 中的 `dbOpTimeout` 数值重复。调整超时需要改两处，容易不一致；应复用同一个常量，或为 INSERT 定义语义明确的独立常量。

### P3 —— 可读性与工程化

11. `tasks` 表仍缺少 `(status, next_run_time)` 索引，调度查询每 2 秒全表扫描。`execuate_tasks` 的 `(task_id, status)` 索引已于 2026-10-20 补上，用于统计 `max_runs` 成功次数。
12. **没有版本化的建表脚本**，表结构靠手工维护，换环境无法复现。
13. `execuate.go` 中定义的 `Execute` 接口从未被使用。
14. 命名问题：包名 `execuate`（应为 `execute`）、`module`（通常叫 `model`）、字段 `Next_time`（应为 `NextTime`）、receiver 用 `this`（Go 惯用短名）。
15. JSON tag 不一致：入参是 `next_run_time`，出参是 `next_runtime`。
16. `GET /api/tasks` 无分页。
17. 数据库凭据硬编码在 `config/database.go` 的 `defaultDSN` 中，应改为从环境变量读取。
18. 日志全部使用 `fmt.Println` / `fmt.Printf` / `println`，无级别、无结构化。
19. `execute` 字段在包外不可注入；`scheduler` 又是每个任务新建一个 `Executor`，后续按类型分发时无法复用配置或注册表。
20. 编译产物 `main`（约 30 MB）被提交进仓库，`.gitignore` 只忽略了 `.claude/` 与 `CLAUDE.md`。
21. `FinishExe` 的部分分支没有测试覆盖：fake driver 的 `RowsAffected` 固定返回 1，`rows == 0 → isFinishApplied` 的回读分支未被测试；"前两次瞬时失败、第三次成功"的重试集成场景也没有覆盖。
22. **`main.go` 启动失败直接 `panic`**：`config.InitDB()` 与 `r.Run()` 失败时输出完整的运行时堆栈，而不是一行清晰的错误，进程退出码为 2。后续可改为记录日志后 `os.Exit(1)`。

---

## 未来优化方向

### 第一阶段：让单机版跑稳

1. **按 `task.Type` 分发到具体执行器**（修问题 2）
   用注册表或工厂模式，把 `cron` / `ICU_CHECK` 等类型映射到各自的执行实现。这是让项目从"调度框架"变成"能真正干活"的关键一步。

2. **并发执行 + worker pool**（修问题 1）
   执行改为异步，并限制并发上限，避免无界 goroutine。

3. **心跳 / 租约 + 启动回收**（修问题 3、4、7）
   给执行记录加心跳字段，进程启动时把超时的 RUNNING 判定为宿主已死并标记 `FAILED`；同时用 `signal.NotifyContext` 做优雅关闭。

4. **给 Executor 传入 `ctx`，统一数据库超时**（修问题 4、9、10）
   让 `Exec` / `InsertSql` / `FinishExe` 接收调度器传来的 `ctx`，用 `signal.NotifyContext` 取消；同时复用同一个超时常量，避免 `InsertSql` 与 `dbOpTimeout` 各自硬编码。

5. **启动失败改为日志 + `os.Exit(1)`**（修问题 22）
   把 `main.go` 的两个 `panic` 改为输出一行错误并以非零码退出，便于服务托管脚本识别。

### 第二阶段：走向分布式

6. **引入 Redis 做调度协调**
   注意分工：Redis 负责**协调**，不负责**持久化执行结果**（它的持久化语义弱于 MySQL，且会引入双写一致性问题）。
   - 任务认领锁：`SET task:claim:{id} {worker} NX EX 30`
   - Worker 心跳：`SET worker:{id}:hb 1 EX 15`
   - 延迟队列：用 ZSET 替代每 2 秒轮询 MySQL，大幅降低数据库压力

7. **任务分片与多实例部署**，配合现有的原子认领。

### 第三阶段：工程化

8. 补齐测试（目前只有 `execuate` 包的 `retry_test.go`、`error_test.go` 与 `config` 包的 `database_test.go`；`scheduler` 与 `handler` 尚无测试）。
9. 配置外置：DSN 从环境变量读取，替换 `defaultDSN`（修问题 17）。
10. 引入结构化日志（`log/slog`）与基础指标（修问题 18）。
11. 统一命名与目录拼写，补齐建表脚本与索引（修问题 11–15）。
12. 补齐分页查询与任务更新接口：分页（修问题 16）、允许修改 `max_runs`、恢复 `COMPLETED` 任务、重新启用已停用任务。

---

## 更新日志

### 2026-10-25
- `InitDB` 增加 `PingContext`（10 秒超时）与连接池参数，启动时即可发现凭据或连接问题
- `InsertSql` 改用带 2 秒超时的 `ExecContext`，避免数据库挂起时无限阻塞调度循环
- 修正 `sqlx.Open` 失败日志措辞：`Open` 只打开驱动并校验 DSN，不建立连接
- 新增 `config/database_test.go`，覆盖启动成功与连接失败两条路径
- 再次审查代码：问题清单删除全部已修复项并重新编号，补充启动 `panic`、数据库操作无法响应关闭信号、`InsertSql` 超时常量重复等发现
- 修正 `execuate/error_test.go` 中过期的场景说明（场景数、场景 2 的定位）

### 2026-10-23
- 执行逻辑抽成可注入的 `Executor.execute` 字段；`nil` 时保持原行为（`Exe` 直接返回 `nil`），便于测试注入成功/失败场景
- `Exec` 在"执行失败 + 结果落库失败"时通过 `errors.Join` 同时返回两个错误
- 新增 `execuate/error_test.go`：覆盖成功、失败、超长错误信息、执行报错、执行与落库同时失败五个场景
- 按实际代码复核 README：更新执行器现状，补充 7 条审查发现

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
