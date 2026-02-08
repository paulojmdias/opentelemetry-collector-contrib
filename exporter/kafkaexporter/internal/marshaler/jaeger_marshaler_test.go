// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package marshaler

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/gogo/protobuf/jsonpb"
	jaegerproto "github.com/jaegertracing/jaeger-idl/model/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/open-telemetry/opentelemetry-collector-contrib/pkg/translator/jaeger"
)

func TestJaegerMarshaler(t *testing.T) {
	td := ptrace.NewTraces()
	span := td.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	span.SetName("foo")
	span.SetStartTimestamp(pcommon.Timestamp(10))
	span.SetEndTimestamp(pcommon.Timestamp(20))
	span.SetTraceID([16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
	span.SetSpanID([8]byte{1, 2, 3, 4, 5, 6, 7, 8})
	batches := jaeger.ProtoFromTraces(td)

	batches[0].Spans[0].Process = batches[0].Process
	jaegerProtoBytes, err := batches[0].Spans[0].Marshal()
	messageKey := []byte(batches[0].Spans[0].TraceID.String())
	require.NoError(t, err)
	require.NotNil(t, jaegerProtoBytes)

	jsonMarshaler := &jsonpb.Marshaler{}
	jsonByteBuffer := new(bytes.Buffer)
	require.NoError(t, jsonMarshaler.Marshal(jsonByteBuffer, batches[0].Spans[0]))

	tests := []struct {
		marshaler TracesMarshaler
		encoding  string
		messages  []Message
	}{
		{
			marshaler: JaegerProtoSpanMarshaler{},
			messages:  []Message{{Value: jaegerProtoBytes, Key: messageKey}},
		},
		{
			marshaler: JaegerJSONSpanMarshaler{},
			messages:  []Message{{Value: jsonByteBuffer.Bytes(), Key: messageKey}},
		},
	}
	for _, test := range tests {
		t.Run(test.encoding, func(t *testing.T) {
			messages, err := test.marshaler.MarshalTraces(td)
			require.NoError(t, err)
			assert.Equal(t, test.messages, messages)
		})
	}
}

func TestJaegerMarshaler_PartialFailure(t *testing.T) {
	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	ss := rs.ScopeSpans().AppendEmpty()

	for i := range 3 {
		span := ss.Spans().AppendEmpty()
		span.SetName(fmt.Sprintf("span-%d", i))
		span.SetStartTimestamp(pcommon.Timestamp(10))
		span.SetEndTimestamp(pcommon.Timestamp(20))
		span.SetTraceID([16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, byte(i), 15, 16})
		span.SetSpanID([8]byte{1, 2, 3, 4, 5, 6, 7, byte(i)})
	}

	failOnIndex := 1
	callCount := 0
	customMarshal := func(span *jaegerproto.Span) ([]byte, error) {
		currentIndex := callCount
		callCount++
		if currentIndex == failOnIndex {
			return nil, errors.New("simulated marshal failure")
		}
		return span.Marshal()
	}

	messages, err := marshalJaeger(td, customMarshal)

	require.Equal(t, 3, callCount)
	require.Len(t, messages, 2, "successfully marshaled spans should be returned")
	require.Error(t, err, "error should be returned for failed spans")
	require.Contains(t, err.Error(), "simulated marshal failure")
}
