package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/m-lab/go/flagx"

	"github.com/m-lab/go/prometheusx"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/m-lab/go/rtx"
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

	c := &Collector{
		metricName: "pod_exporter_process_count",
	}
	prometheus.MustRegister(c)

	srv := prometheusx.MustServeMetrics()
	defer srv.Close()
	<-ctx.Done()
}

func collect() (map[labels]*types.StatsJSON, error) {

	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}

	// TODO: run list in loop to find new pods.
	log.Println("List:")
	containers, err := cli.ContainerList(ctx, types.ContainerListOptions{})
	if err != nil {
		return nil, err
	}

	ret := map[labels]*types.StatsJSON{}
	wg := sync.WaitGroup{}
	lock := sync.Mutex{}

	log.Println("Stats:")
	for _, container := range containers {
		// NOTE: each request is delayed about 1 sec to read delta usage stats.
		wg.Add(1)
		go func(c types.Container) {
			l, v := getStats(cli, c)
			lock.Lock()
			ret[l] = v
			lock.Unlock()
			wg.Done()
		}(container)
	}
	wg.Wait()
	return ret, nil
}

func getStats(cli *client.Client, container types.Container) (labels, *types.StatsJSON) {

	resp, err := cli.ContainerStats(ctx, container.ID, false)
	rtx.Must(err, "Failed to get stats for %q", container.ID)
	defer resp.Body.Close()

	var v *types.StatsJSON
	var t *types.StatsJSON
	var l labels
	// Decode the response.
	dec := json.NewDecoder(resp.Body)
	for {
		t = nil
		if err := dec.Decode(&t); err != nil {
			// TODO: maybe there's a trick here.
			if err == io.EOF {
				break
			}
			log.Println("Try again due to:", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		v = t
		// NOTE: per cpu usage could be used to create histogram heatmap of core usage.
		// NOTE: throttling data could be used to observe cpu scheduling delays.

		// TODO: calculate more things.
		f := strings.Split(v.Name, "_")
		if len(f) < 4 {
			// Ignore non-k8s pod names.
			// TODO: count these.
			continue
		}
		PodCurrentPIDs.WithLabelValues(f[3], f[2], f[1]).Set(float64(v.PidsStats.Current))

		l = labels{
			Container: f[1],
			Namespace: f[3],
			Pod:       f[2],
		}

		log.Printf("%v %d\n",
			l,
			v.PidsStats.Current,
		)
	}
	return l, v
}

type labels struct {
	Container string
	Namespace string
	Pod       string
}

// Collector manages a prometheus.Collector for queries performed by a QueryRunner.
type Collector struct {
	// metricName is the base name for prometheus metrics created for this query.
	metricName string

	metrics map[labels]*types.StatsJSON

	// descs maps metric suffixes to the prometheus description. These descriptions
	// are generated once and must be stable over time.
	descs map[string]*prometheus.Desc

	// mux locks access to types above.
	mux sync.Mutex
}

// NewCollector creates a new BigQuery Collector instance.
func NewCollector(metricName string) *Collector {
	return &Collector{
		metricName: metricName,
		descs:      nil,
		mux:        sync.Mutex{},
	}
}

// Describe satisfies the prometheus.Collector interface. Describe is called
// immediately after registering the collector.
func (col *Collector) Describe(ch chan<- *prometheus.Desc) {
	if col.descs == nil {
		col.descs = make(map[string]*prometheus.Desc, 1)
		err := col.Update()
		if err != nil {
			log.Println(err)
		}
		col.setDesc()
	}
	// NOTE: if Update returns no metrics, this will fail.
	for _, desc := range col.descs {
		ch <- desc
	}
}

// Collect satisfies the prometheus.Collector interface. Collect reports values
// from cached metrics.
func (col *Collector) Collect(ch chan<- prometheus.Metric) {
	col.mux.Lock()
	var err error
	col.metrics, err = collect()
	if err != nil {
		log.Println(err)
		return
	}
	// Get reference to current metrics slice to allow Update to run concurrently.
	metrics := col.metrics
	col.mux.Unlock()

	for l, stats := range metrics {
		for _, desc := range col.descs {
			ch <- prometheus.MustNewConstMetric(
				desc, prometheus.GaugeValue,
				float64(stats.PidsStats.Current),
				[]string{l.Container, l.Namespace, l.Pod}...)
		}
	}
}

// String satisfies the Stringer interface. String returns the metric name.
func (col *Collector) String() string {
	return col.metricName
}

// Update runs the collector query and atomically updates the cached metrics.
// Update is called automaticlly after the collector is registered.
func (col *Collector) Update() error {
	var err error
	col.metrics, err = collect()
	return err
}

func (col *Collector) setDesc() {
	// The query may return no results.
	if len(col.metrics) > 0 {
		// TODO: allow passing meaningful help text.
		col.descs["PidsStats.Current"] =
			prometheus.NewDesc(col.metricName, "help text", []string{"container", "namespace", "pod"}, nil)
	}
}
