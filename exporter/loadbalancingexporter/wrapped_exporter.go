// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package loadbalancingexporter // import "github.com/open-telemetry/opentelemetry-collector-contrib/exporter/loadbalancingexporter"

import (
	"context"
	"fmt"
	"sync"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/otel/attribute"
)

// wrappedExporter is an exporter that waits for the data processing to complete before shutting down.
// consumeWG has to be incremented explicitly by the consumer of the wrapped exporter.
type wrappedExporter struct {
	component.Component
	consumeWG sync.WaitGroup

	// the typed exporter handles are resolved once at construction so the hot path does
	// not pay for a type assertion on every Consume* call. Any of these may be nil if the
	// wrapped component does not implement that signal.
	tracesExporter  exporter.Traces
	metricsExporter exporter.Metrics
	logsExporter    exporter.Logs

	// we store the attributes here for both cases, to avoid new allocations on the hot path
	endpointAttr attribute.Set
	successAttr  attribute.Set
	failureAttr  attribute.Set
}

func newWrappedExporter(exp component.Component, identifier string) *wrappedExporter {
	ea := attribute.String("endpoint", identifier)
	te, _ := exp.(exporter.Traces)
	me, _ := exp.(exporter.Metrics)
	le, _ := exp.(exporter.Logs)
	return &wrappedExporter{
		Component:       exp,
		tracesExporter:  te,
		metricsExporter: me,
		logsExporter:    le,
		endpointAttr:    attribute.NewSet(ea),
		successAttr:     attribute.NewSet(ea, attribute.Bool("success", true)),
		failureAttr:     attribute.NewSet(ea, attribute.Bool("success", false)),
	}
}

func (we *wrappedExporter) Shutdown(ctx context.Context) error {
	we.consumeWG.Wait()
	return we.Component.Shutdown(ctx)
}

func (we *wrappedExporter) ConsumeTraces(ctx context.Context, td ptrace.Traces) error {
	if we.tracesExporter == nil {
		return fmt.Errorf("unable to export traces, unexpected exporter type: expected exporter.Traces but got %T", we.Component)
	}
	return we.tracesExporter.ConsumeTraces(ctx, td)
}

func (we *wrappedExporter) ConsumeMetrics(ctx context.Context, md pmetric.Metrics) error {
	if we.metricsExporter == nil {
		return fmt.Errorf("unable to export metrics, unexpected exporter type: expected exporter.Metrics but got %T", we.Component)
	}
	return we.metricsExporter.ConsumeMetrics(ctx, md)
}

func (we *wrappedExporter) ConsumeLogs(ctx context.Context, ld plog.Logs) error {
	if we.logsExporter == nil {
		return fmt.Errorf("unable to export logs, unexpected exporter type: expected exporter.Logs but got %T", we.Component)
	}
	return we.logsExporter.ConsumeLogs(ctx, ld)
}
