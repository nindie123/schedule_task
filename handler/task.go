package handler

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"task/config"
	model "task/module"
	"time"

	"github.com/gin-gonic/gin"
)

type CreateTaskRequest struct {
	Name      string    `json:"name" binding:"required"`
	Type      string    `json:"type" binding:"required"`
	Interval  int       `json:"interval" binding:"required"`
	Next_time time.Time `json:"next_run_time"  binding:"required"`
}

// Post /api/tasks
func CreateTask(c *gin.Context) {
	var req CreateTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}
	// 状态先确定在本地对象上，再由对象落库
	task := model.Task{
		Name:      req.Name,
		Type:      req.Type,
		Interval:  req.Interval,
		Status:    model.TaskEnabled,
		Next_time: req.Next_time,
	}

	const sql = `INSERT INTO tasks
				(name,type,interval_seconds,status,next_run_time)
				VALUES (?,?,?,?,?)`
	result, err := config.DB.Exec(
		sql,
		task.Name,
		task.Type,
		task.Interval,
		task.Status,
		task.Next_time,
	)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	id, err := result.LastInsertId()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}
	task.ID = uint(id)

	// created_at / updated_at 由数据库生成，回读补全同一个对象
	err = config.DB.Get(
		&task,
		"SELECT * FROM tasks WHERE id = ?",
		task.ID,
	)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusCreated, task)
}
func GetTasks(c *gin.Context) {
	var tasks []model.Task

	const sql = `
		SELECT
			id,
			name,
			type,
			interval_seconds,
			status,
			created_at,
			updated_at,
			next_run_time
		FROM tasks
		ORDER BY id DESC
	`

	err := config.DB.Select(&tasks, sql)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, tasks)
}

// PUT /api/tasks/:id/disable
func DisableTask(c *gin.Context) {
	idStr := c.Param("id")

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "非法的任务ID",
		})
		return
	}

	// 1. 先把任务加载成本地对象；不存在则 404
	var task model.Task
	err = config.DB.Get(
		&task,
		"SELECT * FROM tasks WHERE id = ?",
		id,
	)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "任务不存在",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	// 2. 状态先改在本地对象上
	task.Status = model.TaskDisabled

	// 3. 再由对象落库
	const updateSQL = `
		UPDATE tasks
		SET status = ?
		WHERE id = ?
	`

	// 不能靠 RowsAffected()==0 判断记录不存在：MySQL 返回的是实际改变的行数，
	// 重复停用一个已停用的任务同样是 0 行，会被误判成 404；存在性以第 4 步的回读为准。
	_, err = config.DB.Exec(updateSQL, task.Status, task.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	// 4. updated_at 由数据库 ON UPDATE CURRENT_TIMESTAMP 维护，回读进同一个对象
	// 目前还没有实现，
	err = config.DB.Get(
		&task,
		"SELECT * FROM tasks WHERE id = ?",
		id,
	)
	if err != nil {
		// 该行可能在 UPDATE 前后被并发删除，此时回读才会看到 ErrNoRows
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "任务不存在",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, task)
}
