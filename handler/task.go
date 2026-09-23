package handler

import (
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
	}
	const sql = `INSERT INTO tasks
				(name,type,interval_seconds,status,next_run_time)
				VALUES (?,?,?,?,?)`
	result, err := config.DB.Exec(
		sql,
		req.Name,
		req.Type,
		req.Interval,
		"ENABLED",
		req.Next_time,
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

	var task model.Task

	err = config.DB.Get(
		&task,
		"SELECT * FROM tasks WHERE id = ?",
		id,
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

	const updateSQL = `
		UPDATE tasks
		SET status = 'DISABLED'
		WHERE id = ?
	`

	result, err := config.DB.Exec(updateSQL, id)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	rows, err := result.RowsAffected()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	if rows == 0 {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "任务不存在",
		})
		return
	}

	var task model.Task

	err = config.DB.Get(
		&task,
		"SELECT * FROM tasks WHERE id = ?",
		id,
	)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, task)
}
