package main

import (
	"context"
	"task/config"
	"task/router"
	"task/scheduler"
	"time"
)

func main() {
	initerr := config.InitDB()
	if initerr != nil {
		panic(initerr)
	}

	s := scheduler.NewScheduler(2 * time.Second)
	ctx := context.Background()
	go s.Start(ctx)
	r := router.SetupRouter()

	err := r.Run(":8080")
	if err != nil {
		panic(err)
	}
}
