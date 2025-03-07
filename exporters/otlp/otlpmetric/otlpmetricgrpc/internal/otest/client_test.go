// Code created by gotmpl. DO NOT MODIFY.
// source: internal/shared/otlp/otlpmetric/otest/client_test.go.tmpl

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

package otest

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc/internal"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/aggregation"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	cpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	mpb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

type client struct {
	rCh     <-chan ExportResult
	storage *Storage
}

func (c *client) Temporality(k metric.InstrumentKind) metricdata.Temporality {
	return metric.DefaultTemporalitySelector(k)
}

func (c *client) Aggregation(k metric.InstrumentKind) aggregation.Aggregation {
	return metric.DefaultAggregationSelector(k)
}

func (c *client) Collect() *Storage {
	return c.storage
}

func (c *client) UploadMetrics(ctx context.Context, rm *mpb.ResourceMetrics) error {
	c.storage.Add(&cpb.ExportMetricsServiceRequest{
		ResourceMetrics: []*mpb.ResourceMetrics{rm},
	})
	if c.rCh != nil {
		r := <-c.rCh
		if r.Response != nil && r.Response.GetPartialSuccess() != nil {
			msg := r.Response.GetPartialSuccess().GetErrorMessage()
			n := r.Response.GetPartialSuccess().GetRejectedDataPoints()
			if msg != "" || n > 0 {
				otel.Handle(internal.MetricPartialSuccessError(n, msg))
			}
		}
		return r.Err
	}
	return ctx.Err()
}

func (c *client) ForceFlush(ctx context.Context) error { return ctx.Err() }
func (c *client) Shutdown(ctx context.Context) error   { return ctx.Err() }

func TestClientTests(t *testing.T) {
	factory := func(rCh <-chan ExportResult) (Client, Collector) {
		c := &client{rCh: rCh, storage: NewStorage()}
		return c, c
	}

	t.Run("Integration", RunClientTests(factory))
}
