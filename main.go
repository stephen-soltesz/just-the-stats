package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/m-lab/go/flagx"

	"github.com/m-lab/go/prometheusx"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/m-lab/go/rtx"
	"github.com/stephen-soltesz/pretty"
)

// TODO: define custom collector to stop reporting metrics for deleted containers.

// PodCurrentPIDs collects pid metrics for running k8s pods.
var PodCurrentPIDs = promauto.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "docker_pod_process_count",
		Help: "Current POD process count",
	},
	[]string{"namespace", "pod", "container"},
)

/*
docker run -d -p 9980:9980 -v /var/run:/var/run soltesz/pod-exporter:4  -prometheusx.listen-address :9980
cat pods.txt | awk -F_ '{printf("%-20s %-30s %s\n", $4, $3, $2)}' | sort | grep pusher
*/

var ctx = context.Background()

func main() {
	flag.Parse()
	rtx.Must(flagx.ArgsFromEnv(flag.CommandLine), "Failed to parse args")

	srv := prometheusx.MustServeMetrics()
	defer srv.Close()

	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		panic(err)
	}

	// TODO: run list in loop to find new pods.
	fmt.Println("List:")
	containers, err := cli.ContainerList(ctx, types.ContainerListOptions{})
	if err != nil {
		panic(err)
	}

	fmt.Println("Containers:")
	for _, container := range containers {
		pretty.Print(container)
	}

	fmt.Println("Stats:")
	for _, container := range containers {
		fmt.Printf("%s %q %q\n", container.Names[0], container.State, container.Status)
		// TODO: run the stats collection in parallel.
		// NOTE: each request is delayed about 1 sec to read delta usage stats.
		resp, err := cli.ContainerStats(ctx, container.ID, false)
		rtx.Must(err, "Failed to get stats for %q", container.ID)
		defer resp.Body.Close()

		// Decode the response.
		dec := json.NewDecoder(resp.Body)
		for {
			var v *types.StatsJSON
			if err := dec.Decode(&v); err != nil {
				// TODO: maybe there's a trick here.
				if err == io.EOF {
					fmt.Println("EOF", container.Names[0])
					break
				}
				fmt.Println("Try again due to:", err)
				time.Sleep(100 * time.Millisecond)
				continue
			}
			// NOTE: per cpu usage could be used to create histogram heatmap of core usage.
			// NOTE: throttling data could be used to observe cpu scheduling delays.

			// TODO: calculate more things.
			fmt.Printf("%s %d %d %d %d\n",
				v.Name,
				v.CPUStats.CPUUsage.TotalUsage-v.PreCPUStats.CPUUsage.TotalUsage,
				v.CPUStats.SystemUsage-v.PreCPUStats.SystemUsage,
				v.MemoryStats.Usage,
				v.PidsStats.Current,
			)
			f := strings.Split(v.Name, "_")
			if len(f) < 4 {
				// Ignore non-k8s pod names.
				continue
			}
			PodCurrentPIDs.WithLabelValues(f[3], f[2], f[1]).Set(float64(v.PidsStats.Current))
		}
	}
	<-ctx.Done()
}
