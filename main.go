package main

import (
	"context"
	"flag"
	"time"

	"github.com/docker/docker/api/types"

	"github.com/m-lab/go/flagx"
	"github.com/m-lab/go/prometheusx"
	"github.com/m-lab/go/rtx"
	"github.com/stephen-soltesz/just-the-stats/stats"

	"github.com/docker/docker/client"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	updateDelay         time.Duration
	newCollector        = stats.NewCollector
	mainCtx, mainCancel = context.WithCancel(context.Background())
)

func init() {
	flag.DurationVar(&updateDelay, "update", time.Minute, "")
}

func main() {
	flag.Parse()
	rtx.Must(flagx.ArgsFromEnv(flag.CommandLine), "Failed to parse args")

	c, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	rtx.Must(err, "Failed to allocate Docker client")
	c.ContainerList(mainCtx, types.ContainerListOptions{})

	col := newCollector(c)
	prometheus.MustRegister(col)

	srv := prometheusx.MustServeMetrics()
	defer srv.Close()

	ticker := time.NewTicker(updateDelay)
	defer ticker.Stop()

	for mainCtx.Err() == nil {
		<-ticker.C
		col.Update(mainCtx)
	}
	<-mainCtx.Done()
}
