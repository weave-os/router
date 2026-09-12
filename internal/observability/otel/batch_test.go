package otel

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func TestBatchEnvelope_MatchesGeneratedProtobuf(t *testing.T) {
	resource := buildResource("test", map[string]string{"deployment": "test"})
	scope := &commonv1.InstrumentationScope{Name: "workweave-router"}
	envelope, err := newBatchEnvelope(resource, scope)
	require.NoError(t, err)
	for _, size := range []int{0, 127, 128, 16383, 16384, 1 << 20} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			span := &tracev1.Span{Name: "span", Attributes: NewAttrBuilder(1).String("content", strings.Repeat("s", size)).Build()}
			log := &logsv1.LogRecord{Body: stringValue(strings.Repeat("l", size))}
			for _, tc := range []struct {
				name     string
				record   proto.Message
				exported proto.Message
			}{
				{"traces", span, &coltracepb.ExportTraceServiceRequest{ResourceSpans: []*tracev1.ResourceSpans{{
					Resource: resource, ScopeSpans: []*tracev1.ScopeSpans{{Scope: scope, Spans: []*tracev1.Span{span, span}}},
				}}}},
				{"logs", log, &collogspb.ExportLogsServiceRequest{ResourceLogs: []*logsv1.ResourceLogs{{
					Resource: resource, ScopeLogs: []*logsv1.ScopeLogs{{Scope: scope, LogRecords: []*logsv1.LogRecord{log, log}}},
				}}}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					encoded, err := proto.Marshal(tc.record)
					require.NoError(t, err)
					records := []queuedRecord{{body: encoded}, {body: encoded}}
					recordsSize := 2 * recordFieldSize(len(encoded))
					body := envelope.marshal(records, recordsSize)
					want, err := proto.Marshal(tc.exported)
					require.NoError(t, err)
					require.Equal(t, want, body)
					require.Equal(t, len(body), cap(body), "batch must allocate only one exact-sized output")
					require.Equal(t, len(body), envelope.size(recordsSize))
				})
			}
		})
	}
}
