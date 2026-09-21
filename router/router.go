package router

import (
	"github.com/gin-gonic/gin"

	handler "task/handler"
)

func SetupRouter() *gin.Engine {
	r := gin.Default()

	api := r.Group("/api")
	{
		api.POST("/tasks", handler.CreateTask)

		api.GET("/tasks", handler.GetTasks)

		api.PUT("/tasks/:id/disable", handler.DisableTask)
	}

	return r
}
