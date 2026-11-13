package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"weave-os/router/internal/trafficcapture"
)

const (
	trafficCaptureFileEnv              = "ROUTER_HTTP_CAPTURE_FILE"
	trafficCaptureSensitiveHeadersEnv  = "ROUTER_HTTP_CAPTURE_SENSITIVE_HEADERS"
	trafficCaptureFilePermissions      = 0o600
	trafficCaptureDirectoryPermissions = 0o700
)

type jsonlTrafficCapture struct {
	file             *os.File
	includeSensitive bool
	mu               sync.Mutex
	closed           bool
}

func newTrafficCaptureFromEnvironment() (*jsonlTrafficCapture, error) {
	path := strings.TrimSpace(os.Getenv(trafficCaptureFileEnv))
	if path == "" {
		if strings.EqualFold(os.Getenv(trafficCaptureSensitiveHeadersEnv), "true") {
			return nil, fmt.Errorf("%s requires %s", trafficCaptureSensitiveHeadersEnv, trafficCaptureFileEnv)
		}
		return nil, nil
	}

	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, trafficCaptureDirectoryPermissions); err != nil {
		return nil, fmt.Errorf("create HTTP capture directory: %w", err)
	}
	file, err := os.OpenFile(
		path,
		os.O_CREATE|os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW|syscall.O_NONBLOCK,
		trafficCaptureFilePermissions,
	)
	if err != nil {
		return nil, fmt.Errorf("open HTTP capture file: %w", err)
	}
	fileInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect HTTP capture file: %w", err)
	}
	if !fileInfo.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("HTTP capture path %q is not a regular file", path)
	}
	if err := file.Chmod(trafficCaptureFilePermissions); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("restrict HTTP capture file permissions: %w", err)
	}
	return &jsonlTrafficCapture{
		file:             file,
		includeSensitive: strings.EqualFold(os.Getenv(trafficCaptureSensitiveHeadersEnv), "true"),
	}, nil
}

func (capture *jsonlTrafficCapture) Record(exchange trafficcapture.Exchange) error {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if capture.closed {
		return fmt.Errorf("HTTP capture file is closed")
	}
	if !capture.includeSensitive {
		exchange.Request.Header = redactCaptureHeaders(exchange.Request.Header)
		if exchange.Response != nil {
			exchange.Response.Header = redactCaptureHeaders(exchange.Response.Header)
		}
	}
	exchange.Request.URL = trafficcapture.RedactURL(exchange.Request.URL)
	recordOffset, err := capture.file.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("locate HTTP capture record boundary: %w", err)
	}
	if err := writeCaptureExchange(capture.file, exchange); err != nil {
		encodeErr := fmt.Errorf("encode HTTP capture exchange: %w", err)
		// A partial JSONL line must not absorb the next successful exchange.
		rollbackErr := capture.file.Truncate(recordOffset)
		if rollbackErr == nil {
			rollbackErr = capture.file.Sync()
		}
		if rollbackErr != nil {
			capture.closed = true
			return errors.Join(encodeErr, fmt.Errorf("rollback HTTP capture exchange: %w", rollbackErr), capture.file.Close())
		}
		return encodeErr
	}
	if err := capture.file.Sync(); err != nil {
		return fmt.Errorf("sync HTTP capture exchange: %w", err)
	}
	return nil
}

type captureJSONWriter struct {
	writer    io.Writer
	hasFields bool
}

func (writer *captureJSONWriter) startObject() error {
	_, err := io.WriteString(writer.writer, "{")
	return err
}

func (writer *captureJSONWriter) endObject() error {
	_, err := io.WriteString(writer.writer, "}")
	return err
}

func (writer *captureJSONWriter) field(name string, value any) error {
	if writer.hasFields {
		if _, err := io.WriteString(writer.writer, ","); err != nil {
			return err
		}
	}
	writer.hasFields = true
	encodedName, err := json.Marshal(name)
	if err != nil {
		return err
	}
	encodedValue, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err := writer.writer.Write(encodedName); err != nil {
		return err
	}
	if _, err := io.WriteString(writer.writer, ":"); err != nil {
		return err
	}
	_, err = writer.writer.Write(encodedValue)
	return err
}

func (writer *captureJSONWriter) objectField(name string, writeObject func() error) error {
	if writer.hasFields {
		if _, err := io.WriteString(writer.writer, ","); err != nil {
			return err
		}
	}
	writer.hasFields = true
	encodedName, err := json.Marshal(name)
	if err != nil {
		return err
	}
	if _, err := writer.writer.Write(encodedName); err != nil {
		return err
	}
	if _, err := io.WriteString(writer.writer, ":"); err != nil {
		return err
	}
	return writeObject()
}

func (writer *captureJSONWriter) body(name string, body []byte, spool *trafficcapture.BodySpool) error {
	if writer.hasFields {
		if _, err := io.WriteString(writer.writer, ","); err != nil {
			return err
		}
	}
	writer.hasFields = true
	encodedName, err := json.Marshal(name)
	if err != nil {
		return err
	}
	if _, err := writer.writer.Write(encodedName); err != nil {
		return err
	}
	if _, err := io.WriteString(writer.writer, ":"); err != nil {
		return err
	}
	if spool == nil {
		encodedBody, err := json.Marshal(body)
		if err != nil {
			return err
		}
		_, err = writer.writer.Write(encodedBody)
		return err
	}
	if _, err := io.WriteString(writer.writer, `"`); err != nil {
		return err
	}
	bodyReader, err := spool.Reader()
	if err != nil {
		return err
	}
	base64Writer := base64.NewEncoder(base64.StdEncoding, writer.writer)
	if _, err := io.Copy(base64Writer, bodyReader); err != nil {
		_ = base64Writer.Close()
		return err
	}
	if err := base64Writer.Close(); err != nil {
		return err
	}
	_, err = io.WriteString(writer.writer, `"`)
	return err
}

func writeCaptureRequest(output io.Writer, request trafficcapture.Request) error {
	encodedRequest := captureJSONWriter{writer: output}
	if err := encodedRequest.startObject(); err != nil {
		return err
	}
	for _, field := range []struct {
		name  string
		value any
	}{
		{"method", request.Method},
		{"url", request.URL},
		{"proto", request.Proto},
		{"headers", request.Header},
		{"content_length", request.ContentLength},
	} {
		if err := encodedRequest.field(field.name, field.value); err != nil {
			return err
		}
	}
	if request.Host != "" {
		if err := encodedRequest.field("host", request.Host); err != nil {
			return err
		}
	}
	if len(request.TransferEncoding) != 0 {
		if err := encodedRequest.field("transfer_encoding", request.TransferEncoding); err != nil {
			return err
		}
	}
	if err := encodedRequest.body("body", request.Body, request.BodySpool); err != nil {
		return err
	}
	if err := encodedRequest.field("body_complete", request.BodyComplete); err != nil {
		return err
	}
	return encodedRequest.endObject()
}

func writeCaptureResponse(output io.Writer, response trafficcapture.Response) error {
	encodedResponse := captureJSONWriter{writer: output}
	if err := encodedResponse.startObject(); err != nil {
		return err
	}
	for _, field := range []struct {
		name  string
		value any
	}{
		{"proto", response.Proto},
		{"status_code", response.StatusCode},
		{"headers", response.Header},
		{"content_length", response.ContentLength},
	} {
		if err := encodedResponse.field(field.name, field.value); err != nil {
			return err
		}
	}
	if len(response.TransferEncoding) != 0 {
		if err := encodedResponse.field("transfer_encoding", response.TransferEncoding); err != nil {
			return err
		}
	}
	if err := encodedResponse.body("body", response.Body, response.BodySpool); err != nil {
		return err
	}
	if err := encodedResponse.field("body_complete", response.BodyComplete); err != nil {
		return err
	}
	return encodedResponse.endObject()
}

func writeCaptureExchange(output io.Writer, exchange trafficcapture.Exchange) error {
	encodedExchange := captureJSONWriter{writer: output}
	if err := encodedExchange.startObject(); err != nil {
		return err
	}
	for _, field := range []struct {
		name  string
		value any
	}{
		{"schema_version", exchange.SchemaVersion},
		{"id", exchange.ID},
		{"direction", exchange.Direction},
		{"started_at", exchange.StartedAt},
		{"duration_ms", exchange.DurationMS},
	} {
		if err := encodedExchange.field(field.name, field.value); err != nil {
			return err
		}
	}
	if exchange.ParentID != "" {
		if err := encodedExchange.field("parent_id", exchange.ParentID); err != nil {
			return err
		}
	}
	if exchange.Attempt != 0 {
		if err := encodedExchange.field("attempt", exchange.Attempt); err != nil {
			return err
		}
	}
	if err := encodedExchange.objectField("request", func() error {
		return writeCaptureRequest(output, exchange.Request)
	}); err != nil {
		return err
	}
	if exchange.Response != nil {
		if err := encodedExchange.objectField("response", func() error {
			return writeCaptureResponse(output, *exchange.Response)
		}); err != nil {
			return err
		}
	}
	if err := encodedExchange.field("complete", exchange.Complete); err != nil {
		return err
	}
	if exchange.Error != "" {
		if err := encodedExchange.field("error", exchange.Error); err != nil {
			return err
		}
	}
	if err := encodedExchange.endObject(); err != nil {
		return err
	}
	_, err := io.WriteString(output, "\n")
	return err
}

func (capture *jsonlTrafficCapture) Close() error {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if capture.closed {
		return nil
	}
	capture.closed = true
	if err := capture.file.Sync(); err != nil {
		_ = capture.file.Close()
		return fmt.Errorf("sync HTTP capture file on close: %w", err)
	}
	if err := capture.file.Close(); err != nil {
		return fmt.Errorf("close HTTP capture file: %w", err)
	}
	return nil
}

func redactCaptureHeaders(headers map[string][]string) map[string][]string {
	redacted := make(map[string][]string, len(headers))
	for name, values := range headers {
		if !isSafeCaptureHeader(name) {
			redacted[name] = []string{"[REDACTED]"}
			continue
		}
		redacted[name] = append([]string(nil), values...)
	}
	return redacted
}

func isSafeCaptureHeader(name string) bool {
	switch strings.ToLower(name) {
	case "accept", "accept-encoding", "anthropic-beta", "anthropic-version", "content-encoding", "content-length", "content-type", "openai-beta", "transfer-encoding", "user-agent", "x-goog-api-client":
		return true
	default:
		return false
	}
}
