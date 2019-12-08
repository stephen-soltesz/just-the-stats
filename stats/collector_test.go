package stats

import (
	"bytes"
	"context"
	"encoding/json"
	"io/ioutil"
	"log"
	"testing"
	"time"

	"github.com/m-lab/go/rtx"

	"github.com/docker/docker/api/types"
	"github.com/prometheus/client_golang/prometheus"
)

func init() {
	// log.SetFlags(log.Lshortfile | log.LUTC | log.Ltime)
	log.SetOutput(ioutil.Discard)
}

type fakeClient struct {
	containers []types.Container
	stats      map[string]types.ContainerStats
}

func (f *fakeClient) ContainerList(ctx context.Context, options types.ContainerListOptions) ([]types.Container, error) {
	return f.containers, nil
}

func (f *fakeClient) ContainerStats(ctx context.Context, containerID string, stream bool) (types.ContainerStats, error) {
	return f.stats[containerID], nil
}

var brokenJSON = `{invalid-json`

func newStatsResponse(id, name string, pids, rss uint64) []byte {
	resp := types.StatsJSON{
		Name: name,
		ID:   id,
		Stats: types.Stats{
			Read: time.Date(2019, time.December, 07, 23, 51, 46, 0, time.UTC),
			PidsStats: types.PidsStats{
				Current: pids,
			},
			MemoryStats: types.MemoryStats{
				Stats: map[string]uint64{
					"total_rss": rss,
				},
			},
		},
	}
	b, err := json.Marshal(&resp)
	rtx.Must(err, "Failed to marshal response")
	return b
}

func TestNewCollector(t *testing.T) {
	tests := []struct {
		name       string
		containers []types.Container
		stats      map[string]types.ContainerStats
		wantSize   int
	}{
		{
			name: "success",
			containers: []types.Container{
				{ID: "fakeid1", Names: []string{"/k8s_a_b_c_d"}},
			},
			stats: map[string]types.ContainerStats{
				"fakeid1": types.ContainerStats{
					Body: ioutil.NopCloser(bytes.NewReader(
						newStatsResponse("fakeid1", "/k8s_a_b_c_d", 5, 123456))),
				},
			},
			wantSize: 2,
		},
		{
			name: "bad-json-response",
			containers: []types.Container{
				{ID: "fakeid1", Names: []string{"/k8s_a_b_c_d"}},
			},
			stats: map[string]types.ContainerStats{
				"fakeid1": types.ContainerStats{
					Body: ioutil.NopCloser(bytes.NewReader([]byte("invalid-json}"))),
				},
			},
			wantSize: 0,
		},
		{
			name: "bad-name",
			containers: []types.Container{
				{ID: "fakeid1", Names: []string{"/this-is-not-a-valid-name"}},
			},
			stats: map[string]types.ContainerStats{
				"fakeid1": types.ContainerStats{
					Body: ioutil.NopCloser(bytes.NewReader(
						newStatsResponse("fakeid1", "/this-is-not-a-valid-name", 5, 123456))),
				},
			},
			wantSize: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := &fakeClient{
				containers: tt.containers,
				stats:      tt.stats,
			}
			c := NewCollector(fc)
			dch := make(chan<- *prometheus.Desc, 2)
			c.Describe(dch)
			if len(dch) != tt.wantSize {
				t.Errorf("Describe() got %v, want %d", len(dch), tt.wantSize)
			}

			cch := make(chan<- prometheus.Metric, 2)
			c.Collect(cch)
			if len(cch) != tt.wantSize {
				t.Errorf("Collect() got %v, want %d", len(cch), tt.wantSize)
			}

			ctx := context.Background()
			c.Update(ctx)
		})
	}
}
