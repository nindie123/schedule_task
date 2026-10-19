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
│   └── scheduler.go         调度器：按固定周期扫描到期任务，改下次执行时间，交给执行器
│
└── execuate/                执行器（目录名为历史拼写，正确拼写应为 execute）
    ├── execuate.go          执行流程：写执行记录 → 执行 → 回写结果
    └── retry.go             数据库写入的重试策略与 MySQL 错误分类
    └── retry_test.go        retry.go 的单元测试
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
| `status` | varchar(32) NOT NULL | `ENABLED` / `DISABLED` |
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

## 目前存在的问题

### P1 —— 会导致数据错误或功能整体失效

1. **调度器"取到期任务 + 改下次时间"不在事务内，且 UPDATE 失败只打印日志**
   `scheduler.go` 的 `findDueTasks` 先 SELECT 再逐行 UPDATE，中间无事务；UPDATE 失败只 `fmt.Println`，函数仍正常返回任务列表。
   → 一旦 UPDATE 失败或进程在两步之间被杀，同一任务会在下一个 tick 被**重复选中执行**。这正是执行记录表里出现大量重复任务的原因。

2. **下次执行时间硬编码 `INTERVAL 10 MINUTE`，完全忽略 `interval_seconds`**
   `scheduler.go:99` 写死 10 分钟。一个 `interval_seconds = 10` 的任务实际要 10 分钟才跑一次；且基准取 `NOW()` 而非上次的 `next_run_time`，会累积执行耗时造成的漂移。

3. **`next_run_time` 为 NULL 会导致调度器整体停摆**
   该列定义为 `DEFAULT NULL`，而 Go 结构体字段是 `time.Time`（非指针）。任何一行取到 NULL 都会让**整条查询报错**，`GET /api/tasks` 返回 500、调度器 `findDueTasks` 直接失败返回 —— 不是跳过那一行，而是全部停摆。

### P2 —— 特定条件下的功能或健壮性问题

4. **调度循环被同步执行阻塞**
   `scan()` 里同步调用 `Exec`，而 `Exe` 内含 `Sleep(5s)`。有多个到期任务时会串行阻塞；`time.Ticker` 的 channel 容量为 1，阻塞期间的 tick 会被丢弃，扫描周期实际远大于设定的 2 秒。

5. **执行器是占位实现，`task.Type` 完全未被使用**
   `Exe` 目前只是 `Sleep(5s)` 然后置为成功。无论 `type` 是 `cron` 还是 `ICU_CHECK` 都走同一条路 —— 真正的任务执行逻辑还没开始写。

6. **孤儿 RUNNING 记录无回收机制**
   进程被杀或写库失败时留下的 `RUNNING` 记录永远不会收敛。当前库里已累积 **34 条**孤儿 RUNNING 记录。

7. **无优雅关闭 / 无信号处理**
   `main.go` 里 `ctx` 是 `context.Background()`，永不取消；收到 SIGINT 直接杀进程，正在执行的任务永远停在 RUNNING（与第 6 条同源）。

8. **时区语义不一致**
   连接串用 `loc=Local` 让驱动按本地时区转换 Go 的 `time.Time`，而 SQL 里的 `NOW()` 取的是 MySQL 服务器时区。两者不一致时，任务会提前或延迟数小时触发。（本机实测两者当前一致，属潜在风险。）

9. **`Interval` 无下界校验**
   `binding:"required"` 不拦 0 和负数。等第 2 条修好后，`interval = 0` 会变成"永不停歇地重复执行"。

10. **`InsertSql` 的写入失败会丢失整条执行记录**
    若 `INSERT` 服务端已提交、只是响应丢失，客户端会认为失败；若重试则会产生重复行。目前**刻意不重试**（INSERT 不幂等），这是已知取舍。

### P3 —— 可读性与工程化

11. `tasks` 表缺少 `(status, next_run_time)` 索引，`execuate_tasks` 缺少 `task_id` 索引 —— 每 2 秒全表扫描。
12. **没有版本化的建表脚本**，表结构靠手工维护，换环境无法复现。
13. `execuate.go` 中定义的 `Execute` 接口从未被使用。
14. 命名问题：包名 `execuate`（应为 `execute`）、`module`（通常叫 `model`）、字段 `Next_time`（应为 `NextTime`）、receiver 用 `this`（Go 惯用短名）。
15. JSON tag 不一致：入参是 `next_run_time`，出参是 `next_runtime`。
16. 回读用 `SELECT *`，与其它查询的显式列风格不统一，且对加列脆弱。
17. `GET /api/tasks` 无分页；无数据时返回 `null` 而非 `[]`。
18. 数据库凭据硬编码在 `config/database.go`。
19. 日志全部使用 `fmt.Println` / `println`，无级别、无结构化。
20. `Exec` 中 `update_err` 优先于 `exe_err` 返回，两者都非空时 `exe_err` 被吞掉。

---

## 未来优化方向

### 第一阶段：让单机版跑稳

1. **原子认领到期任务**（解决 P1-1 的根因）
   把"查询到期任务"和"推进下次执行时间"合并成一次原子操作，用 `RowsAffected()` 判断是否认领成功：
   ```sql
   UPDATE tasks
   SET next_run_time = DATE_ADD(next_run_time, INTERVAL interval_seconds SECOND)
   WHERE id = ? AND status = 'ENABLED' AND next_run_time = ?
   ```
   MySQL 8.0 也支持 `SELECT ... FOR UPDATE SKIP LOCKED`，可在不加新依赖的前提下支持多实例。

2. **用 `interval_seconds` 计算下次执行时间**（修 P1-2）
   基于 `next_run_time` 累加而非基于 `NOW()`，避免漂移。需要一并定义**停机期间错过的执行如何处理**：跳过、补跑一次、还是全部补跑。

3. **修复 `next_run_time` 的 NULL 问题**（修 P1-3）
   字段改为 `*time.Time`，或把列改为 `NOT NULL`。

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

9. 补齐测试（目前仅 `retry_test.go` 一个测试文件）。
10. 配置外置：DSN 从环境变量读取，不硬编码凭据。
11. 引入结构化日志（`log/slog`）与基础指标。
12. 统一命名与目录拼写，补齐建表脚本与索引。

---

## 更新日志

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
