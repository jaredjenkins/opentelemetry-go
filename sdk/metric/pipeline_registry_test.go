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

package metric // import "go.opentelemetry.io/otel/sdk/metric"

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/aggregation"
	"go.opentelemetry.io/otel/sdk/metric/internal/aggregate"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/metric/metricdata/metricdatatest"
	"go.opentelemetry.io/otel/sdk/resource"
)

var defaultView = NewView(Instrument{Name: "*"}, Stream{})

type invalidAggregation struct {
	aggregation.Aggregation
}

func (invalidAggregation) Copy() aggregation.Aggregation {
	return invalidAggregation{}
}
func (invalidAggregation) Err() error {
	return nil
}

func requireN[N int64 | float64](t *testing.T, n int, m []aggregate.Measure[N], comps []aggregate.ComputeAggregation, err error) {
	t.Helper()
	assert.NoError(t, err)
	require.Len(t, m, n)
	require.Len(t, comps, n)
}

func assertSum[N int64 | float64](n int, temp metricdata.Temporality, mono bool, v [2]N) func(*testing.T, []aggregate.Measure[N], []aggregate.ComputeAggregation, error) {
	return func(t *testing.T, meas []aggregate.Measure[N], comps []aggregate.ComputeAggregation, err error) {
		t.Helper()
		requireN[N](t, n, meas, comps, err)

		for m := 0; m < n; m++ {
			t.Logf("input/output number: %d", m)
			in, out := meas[m], comps[m]
			in(context.Background(), 1, *attribute.EmptySet())

			var got metricdata.Aggregation
			assert.Equal(t, 1, out(&got), "1 data-point expected")
			metricdatatest.AssertAggregationsEqual(t, metricdata.Sum[N]{
				Temporality: temp,
				IsMonotonic: mono,
				DataPoints:  []metricdata.DataPoint[N]{{Value: v[0]}},
			}, got, metricdatatest.IgnoreTimestamp())

			in(context.Background(), 3, *attribute.EmptySet())

			assert.Equal(t, 1, out(&got), "1 data-point expected")
			metricdatatest.AssertAggregationsEqual(t, metricdata.Sum[N]{
				Temporality: temp,
				IsMonotonic: mono,
				DataPoints:  []metricdata.DataPoint[N]{{Value: v[1]}},
			}, got, metricdatatest.IgnoreTimestamp())
		}
	}
}

func assertHist[N int64 | float64](temp metricdata.Temporality) func(*testing.T, []aggregate.Measure[N], []aggregate.ComputeAggregation, error) {
	return func(t *testing.T, meas []aggregate.Measure[N], comps []aggregate.ComputeAggregation, err error) {
		t.Helper()
		requireN[N](t, 1, meas, comps, err)

		in, out := meas[0], comps[0]
		in(context.Background(), 1, *attribute.EmptySet())

		var got metricdata.Aggregation
		assert.Equal(t, 1, out(&got), "1 data-point expected")
		buckets := []uint64{0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
		n := 1
		metricdatatest.AssertAggregationsEqual(t, metricdata.Histogram[N]{
			Temporality: temp,
			DataPoints: []metricdata.HistogramDataPoint[N]{{
				Count:        uint64(n),
				Bounds:       []float64{0, 5, 10, 25, 50, 75, 100, 250, 500, 750, 1000, 2500, 5000, 7500, 10000},
				BucketCounts: buckets,
				Min:          metricdata.NewExtrema[N](1),
				Max:          metricdata.NewExtrema[N](1),
				Sum:          N(n),
			}},
		}, got, metricdatatest.IgnoreTimestamp())

		in(context.Background(), 1, *attribute.EmptySet())

		if temp == metricdata.CumulativeTemporality {
			buckets[1] = 2
			n = 2
		}
		assert.Equal(t, 1, out(&got), "1 data-point expected")
		metricdatatest.AssertAggregationsEqual(t, metricdata.Histogram[N]{
			Temporality: temp,
			DataPoints: []metricdata.HistogramDataPoint[N]{{
				Count:        uint64(n),
				Bounds:       []float64{0, 5, 10, 25, 50, 75, 100, 250, 500, 750, 1000, 2500, 5000, 7500, 10000},
				BucketCounts: buckets,
				Min:          metricdata.NewExtrema[N](1),
				Max:          metricdata.NewExtrema[N](1),
				Sum:          N(n),
			}},
		}, got, metricdatatest.IgnoreTimestamp())
	}
}

func assertLastValue[N int64 | float64](t *testing.T, meas []aggregate.Measure[N], comps []aggregate.ComputeAggregation, err error) {
	t.Helper()
	requireN[N](t, 1, meas, comps, err)

	in, out := meas[0], comps[0]
	in(context.Background(), 10, *attribute.EmptySet())
	in(context.Background(), 1, *attribute.EmptySet())

	var got metricdata.Aggregation
	assert.Equal(t, 1, out(&got), "1 data-point expected")
	metricdatatest.AssertAggregationsEqual(t, metricdata.Gauge[N]{
		DataPoints: []metricdata.DataPoint[N]{{Value: 1}},
	}, got, metricdatatest.IgnoreTimestamp())
}

func testCreateAggregators[N int64 | float64](t *testing.T) {
	changeAggView := NewView(
		Instrument{Name: "foo"},
		Stream{Aggregation: aggregation.ExplicitBucketHistogram{
			Boundaries: []float64{0, 100},
			NoMinMax:   true,
		}},
	)
	renameView := NewView(Instrument{Name: "foo"}, Stream{Name: "bar"})
	invalidAggView := NewView(
		Instrument{Name: "foo"},
		Stream{Aggregation: invalidAggregation{}},
	)

	instruments := []Instrument{
		{Name: "foo", Kind: InstrumentKind(0)}, //Unknown kind
		{Name: "foo", Kind: InstrumentKindCounter},
		{Name: "foo", Kind: InstrumentKindUpDownCounter},
		{Name: "foo", Kind: InstrumentKindHistogram},
		{Name: "foo", Kind: InstrumentKindObservableCounter},
		{Name: "foo", Kind: InstrumentKindObservableUpDownCounter},
		{Name: "foo", Kind: InstrumentKindObservableGauge},
	}

	testcases := []struct {
		name     string
		reader   Reader
		views    []View
		inst     Instrument
		validate func(*testing.T, []aggregate.Measure[N], []aggregate.ComputeAggregation, error)
	}{
		{
			name:   "Default/Drop",
			reader: NewManualReader(WithAggregationSelector(func(ik InstrumentKind) aggregation.Aggregation { return aggregation.Drop{} })),
			inst:   instruments[InstrumentKindCounter],
			validate: func(t *testing.T, meas []aggregate.Measure[N], comps []aggregate.ComputeAggregation, err error) {
				t.Helper()
				assert.NoError(t, err)
				assert.Len(t, meas, 0)
				assert.Len(t, comps, 0)
			},
		},
		{
			name:     "Default/Delta/Sum/NonMonotonic",
			reader:   NewManualReader(WithTemporalitySelector(deltaTemporalitySelector)),
			inst:     instruments[InstrumentKindUpDownCounter],
			validate: assertSum[N](1, metricdata.DeltaTemporality, false, [2]N{1, 3}),
		},
		{
			name:     "Default/Delta/ExplicitBucketHistogram",
			reader:   NewManualReader(WithTemporalitySelector(deltaTemporalitySelector)),
			inst:     instruments[InstrumentKindHistogram],
			validate: assertHist[N](metricdata.DeltaTemporality),
		},
		{
			name:     "Default/Delta/PrecomputedSum/Monotonic",
			reader:   NewManualReader(WithTemporalitySelector(deltaTemporalitySelector)),
			inst:     instruments[InstrumentKindObservableCounter],
			validate: assertSum[N](1, metricdata.DeltaTemporality, true, [2]N{1, 2}),
		},
		{
			name:     "Default/Delta/PrecomputedSum/NonMonotonic",
			reader:   NewManualReader(WithTemporalitySelector(deltaTemporalitySelector)),
			inst:     instruments[InstrumentKindObservableUpDownCounter],
			validate: assertSum[N](1, metricdata.DeltaTemporality, false, [2]N{1, 2}),
		},
		{
			name:     "Default/Delta/Gauge",
			reader:   NewManualReader(WithTemporalitySelector(deltaTemporalitySelector)),
			inst:     instruments[InstrumentKindObservableGauge],
			validate: assertLastValue[N],
		},
		{
			name:     "Default/Delta/Sum/Monotonic",
			reader:   NewManualReader(WithTemporalitySelector(deltaTemporalitySelector)),
			inst:     instruments[InstrumentKindCounter],
			validate: assertSum[N](1, metricdata.DeltaTemporality, true, [2]N{1, 3}),
		},
		{
			name:     "Default/Cumulative/Sum/NonMonotonic",
			reader:   NewManualReader(),
			inst:     instruments[InstrumentKindUpDownCounter],
			validate: assertSum[N](1, metricdata.CumulativeTemporality, false, [2]N{1, 4}),
		},
		{
			name:     "Default/Cumulative/ExplicitBucketHistogram",
			reader:   NewManualReader(),
			inst:     instruments[InstrumentKindHistogram],
			validate: assertHist[N](metricdata.CumulativeTemporality),
		},
		{
			name:     "Default/Cumulative/PrecomputedSum/Monotonic",
			reader:   NewManualReader(),
			inst:     instruments[InstrumentKindObservableCounter],
			validate: assertSum[N](1, metricdata.CumulativeTemporality, true, [2]N{1, 3}),
		},
		{
			name:     "Default/Cumulative/PrecomputedSum/NonMonotonic",
			reader:   NewManualReader(),
			inst:     instruments[InstrumentKindObservableUpDownCounter],
			validate: assertSum[N](1, metricdata.CumulativeTemporality, false, [2]N{1, 3}),
		},
		{
			name:     "Default/Cumulative/Gauge",
			reader:   NewManualReader(),
			inst:     instruments[InstrumentKindObservableGauge],
			validate: assertLastValue[N],
		},
		{
			name:     "Default/Cumulative/Sum/Monotonic",
			reader:   NewManualReader(),
			inst:     instruments[InstrumentKindCounter],
			validate: assertSum[N](1, metricdata.CumulativeTemporality, true, [2]N{1, 4}),
		},
		{
			name:   "ViewHasPrecedence",
			reader: NewManualReader(),
			views:  []View{changeAggView},
			inst:   instruments[InstrumentKindCounter],
			validate: func(t *testing.T, meas []aggregate.Measure[N], comps []aggregate.ComputeAggregation, err error) {
				t.Helper()
				requireN[N](t, 1, meas, comps, err)

				in, out := meas[0], comps[0]
				in(context.Background(), 1, *attribute.EmptySet())

				var got metricdata.Aggregation
				assert.Equal(t, 1, out(&got), "1 data-point expected")
				metricdatatest.AssertAggregationsEqual(t, metricdata.Histogram[N]{
					Temporality: metricdata.CumulativeTemporality,
					DataPoints: []metricdata.HistogramDataPoint[N]{{
						Count:        1,
						Bounds:       []float64{0, 100},
						BucketCounts: []uint64{0, 1, 0},
						Sum:          1,
					}},
				}, got, metricdatatest.IgnoreTimestamp())

				in(context.Background(), 1, *attribute.EmptySet())

				assert.Equal(t, 1, out(&got), "1 data-point expected")
				metricdatatest.AssertAggregationsEqual(t, metricdata.Histogram[N]{
					Temporality: metricdata.CumulativeTemporality,
					DataPoints: []metricdata.HistogramDataPoint[N]{{
						Count:        2,
						Bounds:       []float64{0, 100},
						BucketCounts: []uint64{0, 2, 0},
						Sum:          2,
					}},
				}, got, metricdatatest.IgnoreTimestamp())
			},
		},
		{
			name:     "MultipleViews",
			reader:   NewManualReader(),
			views:    []View{defaultView, renameView},
			inst:     instruments[InstrumentKindCounter],
			validate: assertSum[N](2, metricdata.CumulativeTemporality, true, [2]N{1, 4}),
		},
		{
			name:     "Reader/Default/Cumulative/Sum/Monotonic",
			reader:   NewManualReader(WithAggregationSelector(func(ik InstrumentKind) aggregation.Aggregation { return aggregation.Default{} })),
			inst:     instruments[InstrumentKindCounter],
			validate: assertSum[N](1, metricdata.CumulativeTemporality, true, [2]N{1, 4}),
		},
		{
			name:     "Reader/Default/Cumulative/Sum/NonMonotonic",
			reader:   NewManualReader(WithAggregationSelector(func(ik InstrumentKind) aggregation.Aggregation { return aggregation.Default{} })),
			inst:     instruments[InstrumentKindUpDownCounter],
			validate: assertSum[N](1, metricdata.CumulativeTemporality, false, [2]N{1, 4}),
		},
		{
			name:     "Reader/Default/Cumulative/ExplicitBucketHistogram",
			reader:   NewManualReader(WithAggregationSelector(func(ik InstrumentKind) aggregation.Aggregation { return aggregation.Default{} })),
			inst:     instruments[InstrumentKindHistogram],
			validate: assertHist[N](metricdata.CumulativeTemporality),
		},
		{
			name:     "Reader/Default/Cumulative/PrecomputedSum/Monotonic",
			reader:   NewManualReader(WithAggregationSelector(func(ik InstrumentKind) aggregation.Aggregation { return aggregation.Default{} })),
			inst:     instruments[InstrumentKindObservableCounter],
			validate: assertSum[N](1, metricdata.CumulativeTemporality, true, [2]N{1, 3}),
		},
		{
			name:     "Reader/Default/Cumulative/PrecomputedSum/NonMonotonic",
			reader:   NewManualReader(WithAggregationSelector(func(ik InstrumentKind) aggregation.Aggregation { return aggregation.Default{} })),
			inst:     instruments[InstrumentKindObservableUpDownCounter],
			validate: assertSum[N](1, metricdata.CumulativeTemporality, false, [2]N{1, 3}),
		},
		{
			name:     "Reader/Default/Gauge",
			reader:   NewManualReader(WithAggregationSelector(func(ik InstrumentKind) aggregation.Aggregation { return aggregation.Default{} })),
			inst:     instruments[InstrumentKindObservableGauge],
			validate: assertLastValue[N],
		},
		{
			name:   "InvalidAggregation",
			reader: NewManualReader(),
			views:  []View{invalidAggView},
			inst:   instruments[InstrumentKindCounter],
			validate: func(t *testing.T, _ []aggregate.Measure[N], _ []aggregate.ComputeAggregation, err error) {
				assert.ErrorIs(t, err, errCreatingAggregators)
			},
		},
	}
	for _, tt := range testcases {
		t.Run(tt.name, func(t *testing.T) {
			var c cache[string, instID]
			p := newPipeline(nil, tt.reader, tt.views)
			i := newInserter[N](p, &c)
			input, err := i.Instrument(tt.inst)
			var comps []aggregate.ComputeAggregation
			for _, instSyncs := range p.aggregations {
				for _, i := range instSyncs {
					comps = append(comps, i.compAgg)
				}
			}
			tt.validate(t, input, comps, err)
		})
	}
}

func TestCreateAggregators(t *testing.T) {
	t.Run("Int64", testCreateAggregators[int64])
	t.Run("Float64", testCreateAggregators[float64])
}

func testInvalidInstrumentShouldPanic[N int64 | float64]() {
	var c cache[string, instID]
	i := newInserter[N](newPipeline(nil, NewManualReader(), []View{defaultView}), &c)
	inst := Instrument{
		Name: "foo",
		Kind: InstrumentKind(255),
	}
	_, _ = i.Instrument(inst)
}

func TestInvalidInstrumentShouldPanic(t *testing.T) {
	assert.Panics(t, testInvalidInstrumentShouldPanic[int64])
	assert.Panics(t, testInvalidInstrumentShouldPanic[float64])
}

func TestPipelinesAggregatorForEachReader(t *testing.T) {
	r0, r1 := NewManualReader(), NewManualReader()
	pipes := newPipelines(resource.Empty(), []Reader{r0, r1}, nil)
	require.Len(t, pipes, 2, "created pipelines")

	inst := Instrument{Name: "foo", Kind: InstrumentKindCounter}
	var c cache[string, instID]
	r := newResolver[int64](pipes, &c)
	aggs, err := r.Aggregators(inst)
	require.NoError(t, err, "resolved Aggregators error")
	require.Len(t, aggs, 2, "instrument aggregators")

	for i, p := range pipes {
		var aggN int
		for _, is := range p.aggregations {
			aggN += len(is)
		}
		assert.Equalf(t, 1, aggN, "pipeline %d: number of instrumentSync", i)
	}
}

func TestPipelineRegistryCreateAggregators(t *testing.T) {
	renameView := NewView(Instrument{Name: "foo"}, Stream{Name: "bar"})
	testRdr := NewManualReader()
	testRdrHistogram := NewManualReader(WithAggregationSelector(func(ik InstrumentKind) aggregation.Aggregation { return aggregation.ExplicitBucketHistogram{} }))

	testCases := []struct {
		name      string
		readers   []Reader
		views     []View
		inst      Instrument
		wantCount int
	}{
		{
			name: "No views have no aggregators",
			inst: Instrument{Name: "foo"},
		},
		{
			name:      "1 reader 1 view gets 1 aggregator",
			inst:      Instrument{Name: "foo"},
			readers:   []Reader{testRdr},
			wantCount: 1,
		},
		{
			name:      "1 reader 2 views gets 2 aggregator",
			inst:      Instrument{Name: "foo"},
			readers:   []Reader{testRdr},
			views:     []View{defaultView, renameView},
			wantCount: 2,
		},
		{
			name:      "2 readers 1 view each gets 2 aggregators",
			inst:      Instrument{Name: "foo"},
			readers:   []Reader{testRdr, testRdrHistogram},
			wantCount: 2,
		},
		{
			name:      "2 reader 2 views each gets 4 aggregators",
			inst:      Instrument{Name: "foo"},
			readers:   []Reader{testRdr, testRdrHistogram},
			views:     []View{defaultView, renameView},
			wantCount: 4,
		},
		{
			name:      "An instrument is duplicated in two views share the same aggregator",
			inst:      Instrument{Name: "foo"},
			readers:   []Reader{testRdr},
			views:     []View{defaultView, defaultView},
			wantCount: 1,
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			p := newPipelines(resource.Empty(), tt.readers, tt.views)
			testPipelineRegistryResolveIntAggregators(t, p, tt.wantCount)
			testPipelineRegistryResolveFloatAggregators(t, p, tt.wantCount)
		})
	}
}

func testPipelineRegistryResolveIntAggregators(t *testing.T, p pipelines, wantCount int) {
	inst := Instrument{Name: "foo", Kind: InstrumentKindCounter}
	var c cache[string, instID]
	r := newResolver[int64](p, &c)
	aggs, err := r.Aggregators(inst)
	assert.NoError(t, err)

	require.Len(t, aggs, wantCount)
}

func testPipelineRegistryResolveFloatAggregators(t *testing.T, p pipelines, wantCount int) {
	inst := Instrument{Name: "foo", Kind: InstrumentKindCounter}
	var c cache[string, instID]
	r := newResolver[float64](p, &c)
	aggs, err := r.Aggregators(inst)
	assert.NoError(t, err)

	require.Len(t, aggs, wantCount)
}

func TestPipelineRegistryResource(t *testing.T) {
	v := NewView(Instrument{Name: "bar"}, Stream{Name: "foo"})
	readers := []Reader{NewManualReader()}
	views := []View{defaultView, v}
	res := resource.NewSchemaless(attribute.String("key", "val"))
	pipes := newPipelines(res, readers, views)
	for _, p := range pipes {
		assert.True(t, res.Equal(p.resource), "resource not set")
	}
}

func TestPipelineRegistryCreateAggregatorsIncompatibleInstrument(t *testing.T) {
	testRdrHistogram := NewManualReader(WithAggregationSelector(func(ik InstrumentKind) aggregation.Aggregation { return aggregation.Sum{} }))

	readers := []Reader{testRdrHistogram}
	views := []View{defaultView}
	p := newPipelines(resource.Empty(), readers, views)
	inst := Instrument{Name: "foo", Kind: InstrumentKindObservableGauge}

	var vc cache[string, instID]
	ri := newResolver[int64](p, &vc)
	intAggs, err := ri.Aggregators(inst)
	assert.Error(t, err)
	assert.Len(t, intAggs, 0)

	rf := newResolver[float64](p, &vc)
	floatAggs, err := rf.Aggregators(inst)
	assert.Error(t, err)
	assert.Len(t, floatAggs, 0)
}

type logCounter struct {
	logr.LogSink

	errN  uint32
	infoN uint32
}

func (l *logCounter) Info(level int, msg string, keysAndValues ...interface{}) {
	atomic.AddUint32(&l.infoN, 1)
	l.LogSink.Info(level, msg, keysAndValues...)
}

func (l *logCounter) InfoN() int {
	return int(atomic.SwapUint32(&l.infoN, 0))
}

func (l *logCounter) Error(err error, msg string, keysAndValues ...interface{}) {
	atomic.AddUint32(&l.errN, 1)
	l.LogSink.Error(err, msg, keysAndValues...)
}

func (l *logCounter) ErrorN() int {
	return int(atomic.SwapUint32(&l.errN, 0))
}

func TestResolveAggregatorsDuplicateErrors(t *testing.T) {
	tLog := testr.NewWithOptions(t, testr.Options{Verbosity: 6})
	l := &logCounter{LogSink: tLog.GetSink()}
	otel.SetLogger(logr.New(l))

	renameView := NewView(Instrument{Name: "bar"}, Stream{Name: "foo"})
	readers := []Reader{NewManualReader()}
	views := []View{defaultView, renameView}

	fooInst := Instrument{Name: "foo", Kind: InstrumentKindCounter}
	barInst := Instrument{Name: "bar", Kind: InstrumentKindCounter}

	p := newPipelines(resource.Empty(), readers, views)

	var vc cache[string, instID]
	ri := newResolver[int64](p, &vc)
	intAggs, err := ri.Aggregators(fooInst)
	assert.NoError(t, err)
	assert.Equal(t, 0, l.InfoN(), "no info logging should happen")
	assert.Len(t, intAggs, 1)

	// The Rename view should produce the same instrument without an error, the
	// default view should also cause a new aggregator to be returned.
	intAggs, err = ri.Aggregators(barInst)
	assert.NoError(t, err)
	assert.Equal(t, 0, l.InfoN(), "no info logging should happen")
	assert.Len(t, intAggs, 2)

	// Creating a float foo instrument should log a warning because there is an
	// int foo instrument.
	rf := newResolver[float64](p, &vc)
	floatAggs, err := rf.Aggregators(fooInst)
	assert.NoError(t, err)
	assert.Equal(t, 1, l.InfoN(), "instrument conflict not logged")
	assert.Len(t, floatAggs, 1)

	fooInst = Instrument{Name: "foo-float", Kind: InstrumentKindCounter}

	floatAggs, err = rf.Aggregators(fooInst)
	assert.NoError(t, err)
	assert.Equal(t, 0, l.InfoN(), "no info logging should happen")
	assert.Len(t, floatAggs, 1)

	floatAggs, err = rf.Aggregators(barInst)
	assert.NoError(t, err)
	// Both the rename and default view aggregators created above should now
	// conflict. Therefore, 2 warning messages should be logged.
	assert.Equal(t, 2, l.InfoN(), "instrument conflicts not logged")
	assert.Len(t, floatAggs, 2)
}

func TestIsAggregatorCompatible(t *testing.T) {
	var undefinedInstrument InstrumentKind

	testCases := []struct {
		name string
		kind InstrumentKind
		agg  aggregation.Aggregation
		want error
	}{
		{
			name: "SyncCounter and Drop",
			kind: InstrumentKindCounter,
			agg:  aggregation.Drop{},
		},
		{
			name: "SyncCounter and LastValue",
			kind: InstrumentKindCounter,
			agg:  aggregation.LastValue{},
			want: errIncompatibleAggregation,
		},
		{
			name: "SyncCounter and Sum",
			kind: InstrumentKindCounter,
			agg:  aggregation.Sum{},
		},
		{
			name: "SyncCounter and ExplicitBucketHistogram",
			kind: InstrumentKindCounter,
			agg:  aggregation.ExplicitBucketHistogram{},
		},
		{
			name: "SyncUpDownCounter and Drop",
			kind: InstrumentKindUpDownCounter,
			agg:  aggregation.Drop{},
		},
		{
			name: "SyncUpDownCounter and LastValue",
			kind: InstrumentKindUpDownCounter,
			agg:  aggregation.LastValue{},
			want: errIncompatibleAggregation,
		},
		{
			name: "SyncUpDownCounter and Sum",
			kind: InstrumentKindUpDownCounter,
			agg:  aggregation.Sum{},
		},
		{
			name: "SyncUpDownCounter and ExplicitBucketHistogram",
			kind: InstrumentKindUpDownCounter,
			agg:  aggregation.ExplicitBucketHistogram{},
		},
		{
			name: "SyncHistogram and Drop",
			kind: InstrumentKindHistogram,
			agg:  aggregation.Drop{},
		},
		{
			name: "SyncHistogram and LastValue",
			kind: InstrumentKindHistogram,
			agg:  aggregation.LastValue{},
			want: errIncompatibleAggregation,
		},
		{
			name: "SyncHistogram and Sum",
			kind: InstrumentKindHistogram,
			agg:  aggregation.Sum{},
		},
		{
			name: "SyncHistogram and ExplicitBucketHistogram",
			kind: InstrumentKindHistogram,
			agg:  aggregation.ExplicitBucketHistogram{},
		},
		{
			name: "ObservableCounter and Drop",
			kind: InstrumentKindObservableCounter,
			agg:  aggregation.Drop{},
		},
		{
			name: "ObservableCounter and LastValue",
			kind: InstrumentKindObservableCounter,
			agg:  aggregation.LastValue{},
			want: errIncompatibleAggregation,
		},
		{
			name: "ObservableCounter and Sum",
			kind: InstrumentKindObservableCounter,
			agg:  aggregation.Sum{},
		},
		{
			name: "ObservableCounter and ExplicitBucketHistogram",
			kind: InstrumentKindObservableCounter,
			agg:  aggregation.ExplicitBucketHistogram{},
		},
		{
			name: "ObservableUpDownCounter and Drop",
			kind: InstrumentKindObservableUpDownCounter,
			agg:  aggregation.Drop{},
		},
		{
			name: "ObservableUpDownCounter and LastValue",
			kind: InstrumentKindObservableUpDownCounter,
			agg:  aggregation.LastValue{},
			want: errIncompatibleAggregation,
		},
		{
			name: "ObservableUpDownCounter and Sum",
			kind: InstrumentKindObservableUpDownCounter,
			agg:  aggregation.Sum{},
		},
		{
			name: "ObservableUpDownCounter and ExplicitBucketHistogram",
			kind: InstrumentKindObservableUpDownCounter,
			agg:  aggregation.ExplicitBucketHistogram{},
		},
		{
			name: "ObservableGauge and Drop",
			kind: InstrumentKindObservableGauge,
			agg:  aggregation.Drop{},
		},
		{
			name: "ObservableGauge and aggregation.LastValue{}",
			kind: InstrumentKindObservableGauge,
			agg:  aggregation.LastValue{},
		},
		{
			name: "ObservableGauge and Sum",
			kind: InstrumentKindObservableGauge,
			agg:  aggregation.Sum{},
			want: errIncompatibleAggregation,
		},
		{
			name: "ObservableGauge and ExplicitBucketHistogram",
			kind: InstrumentKindObservableGauge,
			agg:  aggregation.ExplicitBucketHistogram{},
		},
		{
			name: "unknown kind with Sum should error",
			kind: undefinedInstrument,
			agg:  aggregation.Sum{},
			want: errIncompatibleAggregation,
		},
		{
			name: "unknown kind with LastValue should error",
			kind: undefinedInstrument,
			agg:  aggregation.LastValue{},
			want: errIncompatibleAggregation,
		},
		{
			name: "unknown kind with Histogram should error",
			kind: undefinedInstrument,
			agg:  aggregation.ExplicitBucketHistogram{},
			want: errIncompatibleAggregation,
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			err := isAggregatorCompatible(tt.kind, tt.agg)
			assert.ErrorIs(t, err, tt.want)
		})
	}
}
