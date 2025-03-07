// Copyright The OpenTelemetry Authors
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

package otlpmetricgrpc // import "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc/internal/oconf"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc/internal/otest"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestExporterClientConcurrentSafe(t *testing.T) {
	const goroutines = 5

	coll, err := otest.NewGRPCCollector("", nil)
	require.NoError(t, err)

	ctx := context.Background()
	addr := coll.Addr().String()
	opts := []Option{WithEndpoint(addr), WithInsecure()}
	cfg := oconf.NewGRPCConfig(asGRPCOptions(opts)...)
	client, err := newClient(ctx, cfg)
	require.NoError(t, err)

	exp, err := newExporter(client, oconf.Config{})
	require.NoError(t, err)
	rm := new(metricdata.ResourceMetrics)

	done := make(chan struct{})
	first := make(chan struct{}, goroutines)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.NoError(t, exp.Export(ctx, rm))
			assert.NoError(t, exp.ForceFlush(ctx))
			// Ensure some work is done before shutting down.
			first <- struct{}{}

			for {
				_ = exp.Export(ctx, rm)
				_ = exp.ForceFlush(ctx)

				select {
				case <-done:
					return
				default:
				}
			}
		}()
	}

	for i := 0; i < goroutines; i++ {
		<-first
	}
	close(first)
	assert.NoError(t, exp.Shutdown(ctx))
	assert.ErrorIs(t, exp.Shutdown(ctx), errShutdown)

	close(done)
	wg.Wait()
}

func TestExporterDoesNotBlockTemporalityAndAggregation(t *testing.T) {
	rCh := make(chan otest.ExportResult, 1)
	coll, err := otest.NewGRPCCollector("", rCh)
	require.NoError(t, err)

	ctx := context.Background()
	addr := coll.Addr().String()
	opts := []Option{WithEndpoint(addr), WithInsecure()}
	cfg := oconf.NewGRPCConfig(asGRPCOptions(opts)...)
	client, err := newClient(ctx, cfg)
	require.NoError(t, err)

	exp, err := newExporter(client, oconf.Config{})
	require.NoError(t, err)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		rm := new(metricdata.ResourceMetrics)
		t.Log("starting export")
		require.NoError(t, exp.Export(ctx, rm))
		t.Log("export complete")
	}()

	assert.Eventually(t, func() bool {
		const inst = metric.InstrumentKindCounter
		// These should not be blocked.
		t.Log("getting temporality")
		_ = exp.Temporality(inst)
		t.Log("getting aggregation")
		_ = exp.Aggregation(inst)
		return true
	}, time.Second, 10*time.Millisecond)

	// Clear the export.
	rCh <- otest.ExportResult{}
	close(rCh)
	wg.Wait()
}
