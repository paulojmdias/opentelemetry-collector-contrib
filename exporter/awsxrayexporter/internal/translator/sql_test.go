// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package translator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func TestClientSpanWithStatementAttribute(t *testing.T) {
	attributes := make(map[string]pcommon.Value)
	attributes["db.system.name"] = pcommon.NewValueStr("mysql")
	attributes["db.namespace"] = pcommon.NewValueStr("customers")
	attributes["db.query.text"] = pcommon.NewValueStr("SELECT * FROM user WHERE user_id = ?")
	attributes["user.name"] = pcommon.NewValueStr("readonly_user")
	attributes["server.address"] = pcommon.NewValueStr("db.example.com")
	span := constructSQLSpan(attributes)

	filtered, sqlData := makeSQL(span, attributes)

	assert.NotNil(t, filtered)
	assert.NotNil(t, sqlData)

	w := testWriters.borrow()
	require.NoError(t, w.Encode(sqlData))
	jsonStr := w.String()
	testWriters.release(w)
	assert.Contains(t, jsonStr, "db.example.com/customers")
}

func TestClientSpanWithNonSQLDatabase(t *testing.T) {
	attributes := make(map[string]pcommon.Value)
	attributes["db.system.name"] = pcommon.NewValueStr("redis")
	attributes["db.namespace"] = pcommon.NewValueStr("0")
	attributes["db.query.text"] = pcommon.NewValueStr("SET key value")
	attributes["user.name"] = pcommon.NewValueStr("readonly_user")
	attributes["server.address"] = pcommon.NewValueStr("db.example.com")
	span := constructSQLSpan(attributes)

	filtered, sqlData := makeSQL(span, attributes)
	assert.Nil(t, sqlData)
	assert.NotNil(t, filtered)
}

func TestClientSpanWithoutDBurlAttribute(t *testing.T) {
	attributes := make(map[string]pcommon.Value)
	attributes["db.system.name"] = pcommon.NewValueStr("postgresql")
	attributes["db.namespace"] = pcommon.NewValueStr("customers")
	attributes["db.query.text"] = pcommon.NewValueStr("SELECT * FROM user WHERE user_id = ?")
	attributes["user.name"] = pcommon.NewValueStr("readonly_user")
	span := constructSQLSpan(attributes)

	filtered, sqlData := makeSQL(span, attributes)
	assert.NotNil(t, filtered)
	assert.NotNil(t, sqlData)

	assert.Equal(t, "users.findUnique", *sqlData.URL)
}

func constructSQLSpan(attributes map[string]pcommon.Value) ptrace.Span {
	endTime := time.Now().Round(time.Second)
	startTime := endTime.Add(-90 * time.Second)

	// constructSpanAttributes() in segment_test accepts a map of interfaces...
	interfaceAttributes := make(map[string]any)
	for k, v := range attributes {
		interfaceAttributes[k] = v
	}
	spanAttributes := constructSpanAttributes(interfaceAttributes)

	span := ptrace.NewSpan()
	span.SetTraceID(newTraceID())
	span.SetSpanID(newSegmentID())
	span.SetParentSpanID(newSegmentID())
	span.SetName("users.findUnique")
	span.SetKind(ptrace.SpanKindClient)
	span.SetStartTimestamp(pcommon.NewTimestampFromTime(startTime))
	span.SetEndTimestamp(pcommon.NewTimestampFromTime(endTime))

	status := ptrace.NewStatus()
	status.SetCode(0)
	status.SetMessage("OK")
	status.CopyTo(span.Status())

	spanAttributes.CopyTo(span.Attributes())
	return span
}
