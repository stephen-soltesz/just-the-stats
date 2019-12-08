package main

import (
	"context"
	"io/ioutil"
	"log"
	"testing"
	"time"

	"github.com/stephen-soltesz/just-the-stats/stats"

	"github.com/prometheus/client_golang/prometheus"
)

// fakeCollector implements the stats.UpdateCollector interface and does nothing.
type fakeCollector struct{}

func (f *fakeCollector) Describe(ch chan<- *prometheus.Desc) {
}
func (f *fakeCollector) Collect(ch chan<- prometheus.Metric) {
}
func (f *fakeCollector) Update(ctx context.Context) {
}

func init() {
	log.SetOutput(ioutil.Discard)
}

func Test_main(t *testing.T) {
	updateDelay = time.Millisecond
	newCollector = func(client stats.Client) stats.UpdateCollector {
		return &fakeCollector{}
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		mainCancel()
	}()

	main()

}
