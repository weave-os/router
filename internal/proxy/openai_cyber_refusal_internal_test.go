package proxy

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const cyberRefusalFrame = "event: error\n" +
	`data: {"type":"error","message":"This content was flagged for possible cybersecurity risk. If this seems wrong, try rephrasing your request."}` + "\n\n"

const responsesCreatedFrame = "event: response.created\n" +
	`data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_1","status":"in_progress","model":"gpt-5.6-sol"}}` + "\n\n"

const responsesOutputFrame = "event: response.output_text.delta\n" +
	`data: {"type":"response.output_text.delta","sequence_number":1,"delta":"hello"}` + "\n\n"

// The refusal is the stream's first frame — the shape Codex aborts on. Nothing
// may reach the client, since that is what leaves the turn re-dispatchable.
func TestCyberRefusalGate_WithholdsRefusalBeforeOutput(t *testing.T) {
	rec := httptest.NewRecorder()
	gate := newCyberRefusalGate(rec, true)

	n, err := gate.Write([]byte(responsesCreatedFrame + cyberRefusalFrame))
	require.NoError(t, err)
	assert.Equal(t, len(responsesCreatedFrame+cyberRefusalFrame), n)
	require.NoError(t, gate.Finalize())

	assert.True(t, gate.refused)
	assert.True(t, gate.withheld)
	assert.Empty(t, rec.Body.String(), "a withheld refusal must not reach the client")
}

// The wire splits SSE events across writes, so the refusal has to be caught
// when its frame arrives in pieces after the preamble frame.
func TestCyberRefusalGate_WithholdsRefusalSplitAcrossWrites(t *testing.T) {
	rec := httptest.NewRecorder()
	gate := newCyberRefusalGate(rec, true)

	stream := responsesCreatedFrame + cyberRefusalFrame
	for start := 0; start < len(stream); start += 17 {
		end := min(start+17, len(stream))
		_, err := gate.Write([]byte(stream[start:end]))
		require.NoError(t, err)
	}
	require.NoError(t, gate.Finalize())

	assert.True(t, gate.withheld)
	assert.Empty(t, rec.Body.String())
}

// An ordinary turn must be delivered byte-for-byte; the gate only defers it.
func TestCyberRefusalGate_ReleasesOrdinaryStream(t *testing.T) {
	rec := httptest.NewRecorder()
	gate := newCyberRefusalGate(rec, true)

	stream := responsesCreatedFrame + responsesOutputFrame
	for start := 0; start < len(stream); start += 23 {
		end := min(start+23, len(stream))
		_, err := gate.Write([]byte(stream[start:end]))
		require.NoError(t, err)
	}
	require.NoError(t, gate.Finalize())

	assert.False(t, gate.refused)
	assert.False(t, gate.withheld)
	assert.Equal(t, stream, rec.Body.String())
}

// A refusal arriving after output is no longer rescuable: it must be forwarded,
// while still being recorded so the session is re-pinned off the model.
func TestCyberRefusalGate_ForwardsRefusalAfterOutput(t *testing.T) {
	rec := httptest.NewRecorder()
	gate := newCyberRefusalGate(rec, true)

	stream := responsesCreatedFrame + responsesOutputFrame + cyberRefusalFrame
	_, err := gate.Write([]byte(stream))
	require.NoError(t, err)
	require.NoError(t, gate.Finalize())

	assert.True(t, gate.refused)
	assert.False(t, gate.withheld, "output was already delivered, so the turn is not rescuable")
	assert.Equal(t, stream, rec.Body.String())
}

// Unarmed the gate is observe-only: same bytes, same order, no buffering.
func TestCyberRefusalGate_UnarmedForwardsVerbatim(t *testing.T) {
	rec := httptest.NewRecorder()
	gate := newCyberRefusalGate(rec, false)

	_, err := gate.Write([]byte(cyberRefusalFrame))
	require.NoError(t, err)

	assert.True(t, gate.refused)
	assert.False(t, gate.withheld)
	assert.Equal(t, cyberRefusalFrame, rec.Body.String())
	require.NoError(t, gate.Finalize())
}

// A non-streaming turn completes no SSE frame, so Finalize is where its body is
// classified.
func TestCyberRefusalGate_ClassifiesNonStreamingBodyOnFinalize(t *testing.T) {
	refusal := httptest.NewRecorder()
	gate := newCyberRefusalGate(refusal, true)
	_, err := gate.Write([]byte(`{"error":{"code":"cyber_policy","message":"declined"}}`))
	require.NoError(t, err)
	require.NoError(t, gate.Finalize())
	assert.True(t, gate.withheld)
	assert.Empty(t, refusal.Body.String())

	body := `{"id":"resp_1","status":"completed","output":[]}`
	ordinary := httptest.NewRecorder()
	gate = newCyberRefusalGate(ordinary, true)
	_, err = gate.Write([]byte(body))
	require.NoError(t, err)
	require.NoError(t, gate.Finalize())
	assert.False(t, gate.withheld)
	assert.Equal(t, body, ordinary.Body.String())
}

// The preamble is held while a frame is still arriving, since the frame that
// decides the turn may be the refusal.
func TestCyberRefusalGate_HoldsIncompleteFrame(t *testing.T) {
	rec := httptest.NewRecorder()
	gate := newCyberRefusalGate(rec, true)

	_, err := gate.Write([]byte(responsesCreatedFrame + "event: response.output_text.delta\ndata: {\"delta\""))
	require.NoError(t, err)
	assert.Empty(t, rec.Body.String())

	_, err = gate.Write([]byte(":\"hi\"}\n\n"))
	require.NoError(t, err)
	assert.NotEmpty(t, rec.Body.String(), "the completed output frame releases the stream")
}

// response.created echoes the request's instructions and tools, so a Codex
// preamble is far larger than the frames above and must not be mistaken for
// committed output.
func TestCyberRefusalGate_WithholdsRefusalAfterLargePreamble(t *testing.T) {
	rec := httptest.NewRecorder()
	gate := newCyberRefusalGate(rec, true)

	_, err := gate.Write([]byte(responsesLargeCreatedFrame()))
	require.NoError(t, err)
	require.Empty(t, rec.Body.String(), "a large preamble frame is still a preamble")

	_, err = gate.Write([]byte(cyberRefusalFrame))
	require.NoError(t, err)
	require.NoError(t, gate.Finalize())

	assert.True(t, gate.withheld)
	assert.Empty(t, rec.Body.String())
}

// responsesLargeCreatedFrame echoes a Codex-sized instructions string, so the
// preamble alone spans dozens of provider reads.
func responsesLargeCreatedFrame() string {
	return "event: response.created\n" +
		`data: {"type":"response.created","response":{"id":"resp_1","instructions":"` +
		strings.Repeat("x", 256*1024) + `"}}` + "\n\n"
}

// cyberGateOutcome is what dispatch reads off the gate together with what the
// client was actually sent.
type cyberGateOutcome struct {
	refused  bool
	withheld bool
	body     string
}

func outcomeOf(gate *cyberRefusalGate, rec *httptest.ResponseRecorder) cyberGateOutcome {
	return cyberGateOutcome{refused: gate.refused, withheld: gate.withheld, body: rec.Body.String()}
}

// Resetting before each write emulates stateless framing while retaining the
// same write boundaries. After release, refusal matching deliberately remains
// per-write, so splitting that text can change its observed flag.
func TestCyberRefusalGate_FragmentationMatchesStatelessFraming(t *testing.T) {
	fixtures := []struct {
		name string
		body string
	}{
		{name: "refusal before output", body: responsesCreatedFrame + cyberRefusalFrame},
		{name: "ordinary output", body: responsesCreatedFrame + responsesOutputFrame},
		{name: "refusal after output", body: responsesCreatedFrame + responsesOutputFrame + cyberRefusalFrame},
		{name: "crlf ordinary output", body: strings.ReplaceAll(responsesCreatedFrame+responsesOutputFrame, "\n", "\r\n")},
		{name: "large preamble then output", body: responsesLargeCreatedFrame() + responsesOutputFrame},
		{name: "large preamble then refusal", body: responsesLargeCreatedFrame() + cyberRefusalFrame},
	}
	checkChunks := func(t *testing.T, body string, chunks []string) {
		t.Helper()
		rec, oracleRec := httptest.NewRecorder(), httptest.NewRecorder()
		gate, oracle := newCyberRefusalGate(rec, true), newCyberRefusalGate(oracleRec, true)
		for _, chunk := range chunks {
			oracle.framing.Reset()
			writeChunks(t, oracle, []string{chunk})
			writeChunks(t, gate, []string{chunk})
			require.Equal(t, outcomeOf(oracle, oracleRec), outcomeOf(gate, rec))
		}
		require.NoError(t, oracle.Finalize())
		require.NoError(t, gate.Finalize())
		assert.Equal(t, outcomeOf(oracle, oracleRec), outcomeOf(gate, rec))
		if gate.withheld {
			assert.Empty(t, rec.Body.String())
		} else {
			assert.Equal(t, body, rec.Body.String())
		}
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			for _, size := range chunkSizesFor(fixture.body) {
				checkChunks(t, fixture.body, chunkEvery(fixture.body, size))
			}
			if len(fixture.body) <= largeFixtureBytes {
				for cut := 1; cut < len(fixture.body); cut++ {
					checkChunks(t, fixture.body, []string{fixture.body[:cut], fixture.body[cut:]})
				}
			}
		})
	}
}

// A frame that never completes is held only up to the cap; the write that
// reaches it commits everything held so far, and later writes flow straight
// through.
func TestCyberRefusalGate_ReleasesWhenHeldTailReachesCap(t *testing.T) {
	rec := httptest.NewRecorder()
	gate := newCyberRefusalGate(rec, true)
	writeChunks(t, gate, []string{responsesCreatedFrame})
	require.Empty(t, rec.Body.String())

	unterminated := "event: response.output_text.delta\ndata: {\"delta\":\"" + strings.Repeat("x", cyberRefusalHoldCap+8192)
	written := len(responsesCreatedFrame)
	releasedAt := 0
	for i, chunk := range chunkEvery(unterminated, 4096) {
		writeChunks(t, gate, []string{chunk})
		written += len(chunk)
		if written < cyberRefusalHoldCap {
			require.Empty(t, rec.Body.String(), "write %d is still under the cap", i)
			continue
		}
		if releasedAt == 0 {
			releasedAt = i
		}
		require.Equal(t, written, rec.Body.Len(), "write %d: everything held is committed once the cap is reached", i)
	}
	require.Positive(t, releasedAt, "the fixture must cross the cap")
	require.NoError(t, gate.Finalize())

	assert.False(t, gate.refused)
	assert.False(t, gate.withheld)
	assert.Equal(t, responsesCreatedFrame+unterminated, rec.Body.String())
}
