package main

import (
	"task/config"
	"task/router"
)

func main() {
	config.InitDB()

	r := router.SetupRouter()

	err := r.Run(":8080")
	if err != nil {
		panic(err)
	}
}
