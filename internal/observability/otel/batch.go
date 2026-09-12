package otel

import (
	"fmt"

	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

// OTLP traces and logs share the envelope layout: export.resource_* (1),
// resource.scope_* (2), scope.records (2). Keeping records encoded avoids
// retaining caller-owned graphs or decoding another copy just to batch them.
type batchEnvelope struct {
	resource []byte
	scope    []byte
}

func newBatchEnvelope(resource *resourcev1.Resource, scope *commonv1.InstrumentationScope) (batchEnvelope, error) {
	resourceBody, err := proto.Marshal(resource)
	if err != nil {
		return batchEnvelope{}, fmt.Errorf("marshal OTLP resource: %w", err)
	}
	scopeBody, err := proto.Marshal(scope)
	if err != nil {
		return batchEnvelope{}, fmt.Errorf("marshal OTLP scope: %w", err)
	}
	return batchEnvelope{
		resource: protowire.AppendBytes(protowire.AppendTag(nil, 1, protowire.BytesType), resourceBody),
		scope:    protowire.AppendBytes(protowire.AppendTag(nil, 1, protowire.BytesType), scopeBody),
	}, nil
}

func recordFieldSize(size int) int { return 1 + protowire.SizeBytes(size) }

func (e batchEnvelope) size(recordsSize int) int {
	scopeSize := len(e.scope) + recordsSize
	resourceSize := len(e.resource) + recordFieldSize(scopeSize)
	return recordFieldSize(resourceSize)
}

func (e batchEnvelope) marshal(records []queuedRecord, recordsSize int) []byte {
	scopeSize := len(e.scope) + recordsSize
	resourceSize := len(e.resource) + recordFieldSize(scopeSize)
	body := make([]byte, 0, recordFieldSize(resourceSize))
	body = protowire.AppendTag(body, 1, protowire.BytesType)
	body = protowire.AppendVarint(body, uint64(resourceSize))
	body = append(body, e.resource...)
	body = protowire.AppendTag(body, 2, protowire.BytesType)
	body = protowire.AppendVarint(body, uint64(scopeSize))
	body = append(body, e.scope...)
	for _, record := range records {
		body = protowire.AppendTag(body, 2, protowire.BytesType)
		body = protowire.AppendBytes(body, record.body)
	}
	return body
}
