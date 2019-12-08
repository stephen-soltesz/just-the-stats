package main

import (
	"context"
	"flag"
	"time"

	"github.com/stephen-soltesz/just-the-stats/stats"

	"github.com/m-lab/go/flagx"

	"github.com/docker/docker/client"
	"github.com/m-lab/go/prometheusx"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/m-lab/go/rtx"
)

var ctx = context.Background()

func main() {
	flag.Parse()
	rtx.Must(flagx.ArgsFromEnv(flag.CommandLine), "Failed to parse args")

	c, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	rtx.Must(err, "Failed to allocate Docker client")

	col := stats.NewCollector(c)
	prometheus.MustRegister(col)

	srv := prometheusx.MustServeMetrics()
	defer srv.Close()

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for ctx.Err() == nil {
		<-ticker.C
		col.Update(ctx)
	}
	<-ctx.Done()
}
