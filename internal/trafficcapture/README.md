# internal/trafficcapture

Opt-in capture of upstream request/response exchanges.

- `BodySpool` streams captured bodies to a temporary file so capture memory
  stays bounded. Reading a spool does not move the write offset, so a stream
  can keep appending while a reader drains it. On Unix the file is unlinked as
  soon as it is created; on Windows it is removed when the spool is closed.
- `Recorder` implementations must be safe for concurrent calls and must consume
  every `BodySpool` in an `Exchange` before `Record` returns; the caller closes
  the spools afterwards.
- URLs and request errors are redacted before they are recorded.
