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

	"github.com/m-lab/go/logx"
	"github.com/stephen-soltesz/pretty"

	"github.com/m-lab/go/flagx"

	"github.com/m-lab/go/prometheusx"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/m-lab/go/rtx"
)

// NonStandardContainers counts the number of docker containers that do not match the k8s naming pattern.
var NonStandardContainers = promauto.NewGauge(
	prometheus.GaugeOpts{
		Name: "docker_stats_nonstandard_containers",
		Help: "Number of containers that did not match the k8s name pattern",
	},
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
		metricPrefix: "docker_stats",
	}
	prometheus.MustRegister(c)
	srv := prometheusx.MustServeMetrics()
	defer srv.Close()

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for ctx.Err() == nil {
		<-ticker.C
		c.Update()
	}
	<-ctx.Done()
}

func filterK8SNames(containers []types.Container) ([]types.Container, int) {
	var nonstandard int
	var filtered []types.Container
	for i := range containers {
		c := containers[i]
		f := strings.Split(c.Names[0], "_")
		if len(f) < 4 || f[0] != "/k8s" {
			// Count docker container names that do not match k8s pattern.
			log.Println(f)
			nonstandard++
			continue
		}
		filtered = append(filtered, c)
	}
	return filtered, nonstandard
}

func collect() (map[*labels]*types.StatsJSON, error) {

	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}

	containers, err := cli.ContainerList(ctx, types.ContainerListOptions{})
	if err != nil {
		return nil, err
	}

	filtered, nonstandard := filterK8SNames(containers)
	NonStandardContainers.Set(float64(nonstandard))

	ret := map[*labels]*types.StatsJSON{}
	wg := sync.WaitGroup{}
	lock := sync.Mutex{}

	log.Println("Collecting stats:")
	for _, container := range filtered {
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

func getStats(cli *client.Client, container types.Container) (*labels, *types.StatsJSON) {
	// Read container stats from docker api.
	resp, err := cli.ContainerStats(ctx, container.ID, false)
	rtx.Must(err, "Failed to get stats for %q", container.ID)
	defer resp.Body.Close()

	var v *types.StatsJSON
	// Decode the response.
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&v); err != nil {
		if err == io.EOF {
			log.Printf("Error EOF before decoding response: %q", err)
			return nil, nil
		}
		log.Printf("Error decoding response: %q", err)
		return nil, nil
	}

	// Only filtered k8s names should have been passed here.
	f := strings.Split(v.Name, "_")
	if len(f) < 4 || f[0] != "/k8s" {
		log.Printf("ERROR: container name does not match k8s naming pattern: %q", v.Name)
		return nil, nil
	}
	l := &labels{
		Container: f[1],
		Namespace: f[3],
		Pod:       f[2],
	}

	logx.Debug.Println(pretty.Sprint(v))
	log.Printf("%v %d %d\n", *l, v.PidsStats.Current, v.MemoryStats.Stats["total_rss"])
	return l, v
}

type labels struct {
	Container string
	Namespace string
	Pod       string
}

// Collector manages a prometheus.Collector for queries performed by a QueryRunner.
type Collector struct {
	// metricPrefix is the base name for prometheus metrics created for this query.
	metricPrefix string

	metrics map[*labels]*types.StatsJSON

	// descs maps metric suffixes to the prometheus description. These descriptions
	// are generated once and must be stable over time.
	descs map[string]*prometheus.Desc

	// mux locks access to types above.
	mux sync.Mutex
}

// NewCollector creates a new BigQuery Collector instance.
func NewCollector(metricPrefix string) *Collector {
	return &Collector{
		metricPrefix: metricPrefix,
	}
}

// Describe satisfies the prometheus.Collector interface. Describe is called
// immediately after registering the collector.
func (col *Collector) Describe(ch chan<- *prometheus.Desc) {
	if col.descs == nil {
		// col.descs = make([]*prometheus.Desc)
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
	// Get reference to current metrics slice to allow Update to run concurrently.
	metrics := col.metrics
	col.mux.Unlock()

	for l, stats := range metrics {
		// NOTE: per cpu usage could be used to create histogram heatmap of core usage.
		// NOTE: throttling data could be used to observe cpu saturation.
		ch <- prometheus.MustNewConstMetric(
			col.descs["pidstats_current"], prometheus.GaugeValue,
			float64(stats.PidsStats.Current),
			[]string{l.Container, l.Namespace, l.Pod}...)
		ch <- prometheus.MustNewConstMetric(
			col.descs["memorystats_total_rss"], prometheus.GaugeValue,
			float64(stats.MemoryStats.Stats["total_rss"]),
			[]string{l.Container, l.Namespace, l.Pod}...)

	}
}

// String satisfies the Stringer interface. String returns the metric name.
func (col *Collector) String() string {
	return col.metricPrefix
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
		col.descs = map[string]*prometheus.Desc{
			"pidstats_current":      prometheus.NewDesc(col.metricPrefix+"_pidstats_current", "Docker PidStats.Current", []string{"container", "namespace", "pod"}, nil),
			"memorystats_total_rss": prometheus.NewDesc(col.metricPrefix+"_memorystats_total_rss", "Docker MemoryStats.Stats['total_rss']", []string{"container", "namespace", "pod"}, nil),
		}
	}
}
