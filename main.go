// Copyright © 2019 prometheus-dockerstats-exporter Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package main starts docker stats collection and prometheus exporter.
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
}
