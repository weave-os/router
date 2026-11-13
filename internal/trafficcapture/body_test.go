package trafficcapture

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"testing"
)

// TestBodySpoolReaderKeepsWriteOffset proves a recorded exchange can read the
// captured prefix without losing the bytes that are still streaming in.
func TestBodySpoolReaderKeepsWriteOffset(t *testing.T) {
	spool := &BodySpool{}
	spool.Write([]byte(`{"prompt":"first"`))

	captured := readSpool(t, spool)
	if captured != `{"prompt":"first"` {
		t.Fatalf("captured body = %q, want %q", captured, `{"prompt":"first"`)
	}

	spool.Write([]byte(`,"stream":true}`))
	captured = readSpool(t, spool)
	if captured != `{"prompt":"first","stream":true}` {
		t.Fatalf("captured body after second write = %q, want the full payload", captured)
	}

	if err := spool.Close(); err != nil {
		t.Fatalf("Close() error = %v, want nil", err)
	}
}

// TestBodySpoolReclaimsTemporaryFile pins the platform contract for spooled
// capture bodies: writing must not fail, and the temporary file must be gone
// once the spool closes. Windows cannot unlink a file whose handle is still
// open, so the spool has to keep the name and remove it on close; on Unix the
// name is already unlinked when the file is created.
func TestBodySpoolReclaimsTemporaryFile(t *testing.T) {
	spool := &BodySpool{}
	spool.Write([]byte(`{"model":"capture-test"}`))
	if err := spool.Err(); err != nil {
		t.Fatalf("spool write error = %v, want nil", err)
	}
	if spool.file == nil {
		t.Fatal("spool has no temporary file after a non-empty write")
	}
	name := spool.file.Name()

	if err := spool.Close(); err != nil {
		t.Fatalf("Close() error = %v, want nil", err)
	}
	if _, err := os.Stat(name); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("temporary capture file %s still present after Close: %v", name, err)
	}
}

func readSpool(t *testing.T, spool *BodySpool) string {
	t.Helper()
	reader, err := spool.Reader()
	if err != nil {
		t.Fatalf("Reader() error = %v, want nil", err)
	}
	captured, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read captured body: %v", err)
	}
	return string(captured)
}
