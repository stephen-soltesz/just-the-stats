package stats

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"strings"
	"sync"

	"github.com/m-lab/go/logx"
	"github.com/stephen-soltesz/pretty"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/docker/docker/api/types"
	"github.com/m-lab/go/rtx"
)

// NonStandardContainers counts the number of docker containers that do not match the k8s naming pattern.
var NonStandardContainers = promauto.NewGauge(
	prometheus.GaugeOpts{
		Name: "docker_stats_nonstandard_containers",
		Help: "Number of containers that did not match the k8s name pattern",
	},
)

// Client defines the interface used to collect container usage statistics.
type Client interface {
	ContainerList(ctx context.Context, options types.ContainerListOptions) ([]types.Container, error)
	ContainerStats(ctx context.Context, containerID string, stream bool) (types.ContainerStats, error)
}

// Collector manages a prometheus.Collector for queries performed by a QueryRunner.
type Collector struct {
	// metricPrefix is the base name for prometheus metrics created for this query.
	metricPrefix string

	// metrics caches the docker stats between calls to Update.
	metrics map[*labels]*types.StatsJSON

	// client is used to read container stats. A docker *client.Client satisfies this interface.
	client Client

	// descs maps metric suffixes to the prometheus description. These descriptions
	// are generated once and must be stable over time.
	descs map[string]*prometheus.Desc

	// mux locks access to types above.
	mux sync.Mutex
}

// NewCollector creates a new BigQuery Collector instance.
func NewCollector(client Client) *Collector {
	return &Collector{
		metricPrefix: "docker_stats",
		client:       client,
	}
}

// Describe satisfies the prometheus.Collector interface. Describe is called
// immediately after registering the collector.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	if c.descs == nil {
		// TODO: provide update timeout.
		ctx := context.Background()
		c.Update(ctx)
		c.setDesc()
	}
	// NOTE: if Update returns no metrics, this will fail.
	for _, desc := range c.descs {
		ch <- desc
	}
}

// Collect satisfies the prometheus.Collector interface. Collect reports values
// from cached metrics.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	c.mux.Lock()
	defer c.mux.Unlock()

	for l, stats := range c.metrics {
		// TODO: per cpu usage could be used to create histogram heatmap of core usage.
		// TODO: throttling data could be used to observe cpu saturation.
		ch <- prometheus.MustNewConstMetric(
			c.descs["pidstats_current"], prometheus.GaugeValue,
			float64(stats.PidsStats.Current),
			[]string{l.Container, l.Namespace, l.Pod}...)
		ch <- prometheus.MustNewConstMetric(
			c.descs["memorystats_total_rss"], prometheus.GaugeValue,
			float64(stats.MemoryStats.Stats["total_rss"]),
			[]string{l.Container, l.Namespace, l.Pod}...)
	}
}

// Update runs the collector query and atomically updates the cached metrics.
// Update is called automaticlly after the collector is registered.
func (c *Collector) Update(ctx context.Context) {
	metrics := c.collect(ctx)
	c.mux.Lock()
	c.metrics = metrics
	c.mux.Unlock()
}

// String satisfies the Stringer interface. String returns the metric name.
func (c *Collector) String() string {
	return c.metricPrefix
}

type labels struct {
	Container string
	Namespace string
	Pod       string
}

func (c *Collector) setDesc() {
	// The query may return no results.
	if len(c.metrics) > 0 {
		c.descs = map[string]*prometheus.Desc{
			"pidstats_current":      prometheus.NewDesc(c.metricPrefix+"_pidstats_current", "Docker PidStats.Current", []string{"container", "namespace", "pod"}, nil),
			"memorystats_total_rss": prometheus.NewDesc(c.metricPrefix+"_memorystats_total_rss", "Docker MemoryStats.Stats['total_rss']", []string{"container", "namespace", "pod"}, nil),
		}
	}
}

func filterK8SNames(containers []types.Container) ([]types.Container, int) {
	var nonstandard int
	var filtered []types.Container
	for i := range containers {
		c := containers[i]
		f := strings.Split(c.Names[0], "_")
		if !isK8sName(f) {
			// Count docker container names that do not match k8s pattern.
			log.Println("Bad name:", f)
			nonstandard++
			continue
		}
		filtered = append(filtered, c)
	}
	return filtered, nonstandard
}

func (c *Collector) collect(ctx context.Context) map[*labels]*types.StatsJSON {
	containers, err := c.client.ContainerList(ctx, types.ContainerListOptions{})
	rtx.Must(err, "Failed to list containers")

	filtered, nonstandard := filterK8SNames(containers)
	NonStandardContainers.Set(float64(nonstandard))

	ret := map[*labels]*types.StatsJSON{}
	wg := sync.WaitGroup{}
	lock := sync.Mutex{}

	log.Println("Collecting stats:")
	for _, container := range filtered {
		// NOTE: each request is delayed about 1 sec to read delta usage stats.
		wg.Add(1)
		go func(cont types.Container) {
			defer wg.Done()
			l, v := getStats(ctx, c.client, cont)
			if l == nil || v == nil {
				return
			}
			lock.Lock()
			ret[l] = v
			lock.Unlock()
		}(container)
	}
	wg.Wait()
	return ret
}

func isK8sName(f []string) bool {
	return len(f) >= 4 && f[0] == "/k8s"
}

func getStats(ctx context.Context, client Client, container types.Container) (*labels, *types.StatsJSON) {
	// Read container stats from docker api.
	resp, err := client.ContainerStats(ctx, container.ID, false)
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

	// Only filtered k8s names should reach this point.
	f := strings.Split(v.Name, "_")
	l := &labels{
		Container: f[1],
		Namespace: f[3],
		Pod:       f[2],
	}

	logx.Debug.Println(pretty.Sprint(v))
	log.Printf("%v %d %d\n", *l, v.PidsStats.Current, v.MemoryStats.Stats["total_rss"])
	return l, v
}
