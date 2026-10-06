//go:build kiroprobe

package executor

// Raw capture of what Kiro sends. Runs the real ExecuteStream path with the
// pooled transport wrapped so the upstream request and every response byte are
// written to disk, then decodes the AWS event stream with a parser independent
// of the executor's (every header, every value type, every payload).
//
//	KIRO_PROBE_CRED=/root/.cli-proxy-api/<file>.json \
//	KIRO_PROBE_BODY=<claude /v1/messages body> KIRO_PROBE_MODEL=claude-sonnet-4.5 \
//	KIRO_PROBE_OUT=<dir> go test -tags kiroprobe -run TestKiroProbe -count=1 -v ./internal/runtime/executor/

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nguyenha935/kiro-cpa-plugin/internal/modelcapabilities"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type probeRead struct {
	At     time.Duration `json:"at_ms"`
	Offset int           `json:"offset"`
	N      int           `json:"n"`
}

type probeBody struct {
	io.ReadCloser
	start time.Time
	mu    sync.Mutex
	raw   bytes.Buffer
	reads []probeRead
}

func (b *probeBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.mu.Lock()
		b.reads = append(b.reads, probeRead{At: time.Since(b.start) / time.Millisecond, Offset: b.raw.Len(), N: n})
		b.raw.Write(p[:n])
		b.mu.Unlock()
	}
	return n, err
}

type probeTransport struct {
	base   http.RoundTripper
	outDir string
	body   *probeBody
}

func (t *probeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	sent, _ := io.ReadAll(request.Body)
	request.Body = io.NopCloser(bytes.NewReader(sent))
	headers := request.Header.Clone()
	headers.Del("Authorization")
	meta, _ := json.MarshalIndent(map[string]any{"url": request.URL.String(), "headers": headers}, "", "  ")
	_ = os.WriteFile(filepath.Join(t.outDir, "upstream-request.json"), meta, 0o600)
	_ = os.WriteFile(filepath.Join(t.outDir, "upstream-request-body.json"), sent, 0o600)
	start := time.Now()
	response, err := t.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	status, _ := json.MarshalIndent(map[string]any{"status": response.StatusCode, "headers": response.Header, "header_ms": time.Since(start).Milliseconds()}, "", "  ")
	_ = os.WriteFile(filepath.Join(t.outDir, "upstream-response.json"), status, 0o600)
	t.body = &probeBody{ReadCloser: response.Body, start: start}
	response.Body = t.body
	return response, nil
}

type probeFrame struct {
	Index   int            `json:"index"`
	Offset  int            `json:"offset"`
	AtMS    int64          `json:"at_ms"`
	Length  int            `json:"length"`
	Headers map[string]any `json:"headers"`
	Payload string         `json:"payload"`
	Error   string         `json:"error,omitempty"`
}

// decodeProbeFrames reads application/vnd.amazon.eventstream: a 12-byte
// prelude (total length, headers length, prelude CRC), headers, payload and a
// trailing message CRC.
func decodeProbeFrames(raw []byte, reads []probeRead) []probeFrame {
	arrival := func(offset int) int64 {
		for _, read := range reads {
			if offset < read.Offset+read.N {
				return int64(read.At)
			}
		}
		return -1
	}
	var frames []probeFrame
	for offset := 0; offset < len(raw); {
		frame := probeFrame{Index: len(frames), Offset: offset, AtMS: arrival(offset), Headers: map[string]any{}}
		if len(raw)-offset < 12 {
			frame.Error = fmt.Sprintf("trailing %d bytes: %s", len(raw)-offset, hex.EncodeToString(raw[offset:]))
			frames = append(frames, frame)
			break
		}
		total := int(binary.BigEndian.Uint32(raw[offset:]))
		headerLen := int(binary.BigEndian.Uint32(raw[offset+4:]))
		frame.Length = total
		if total < 16 || offset+total > len(raw) || 12+headerLen > total-4 {
			frame.Error = fmt.Sprintf("bad prelude total=%d headers=%d remaining=%d", total, headerLen, len(raw)-offset)
			frames = append(frames, frame)
			break
		}
		headers := raw[offset+12 : offset+12+headerLen]
		for h := 0; h < len(headers); {
			nameLen := int(headers[h])
			h++
			if h+nameLen+1 > len(headers) {
				frame.Error = "truncated header name"
				break
			}
			name := string(headers[h : h+nameLen])
			h += nameLen
			kind := headers[h]
			h++
			var value any
			size := 0
			switch kind {
			case 0:
				value = true
			case 1:
				value = false
			case 2:
				size = 1
			case 3:
				size = 2
			case 4:
				size = 4
			case 5, 8:
				size = 8
			case 9:
				size = 16
			case 6, 7:
				if h+2 > len(headers) {
					frame.Error = "truncated header length"
					break
				}
				size = int(binary.BigEndian.Uint16(headers[h:]))
				h += 2
			default:
				frame.Error = fmt.Sprintf("unknown header type %d for %q", kind, name)
			}
			if frame.Error != "" || h+size > len(headers) {
				if frame.Error == "" {
					frame.Error = "truncated header value"
				}
				break
			}
			chunk := headers[h : h+size]
			h += size
			switch kind {
			case 2:
				value = int8(chunk[0])
			case 3:
				value = int16(binary.BigEndian.Uint16(chunk))
			case 4:
				value = int32(binary.BigEndian.Uint32(chunk))
			case 5:
				value = int64(binary.BigEndian.Uint64(chunk))
			case 8:
				value = time.UnixMilli(int64(binary.BigEndian.Uint64(chunk))).UTC().Format(time.RFC3339Nano)
			case 6, 9:
				value = hex.EncodeToString(chunk)
			case 7:
				value = string(chunk)
			}
			frame.Headers[name] = value
		}
		frame.Payload = string(raw[offset+12+headerLen : offset+total-4])
		frames = append(frames, frame)
		offset += total
	}
	return frames
}

func TestKiroProbe(t *testing.T) {
	credPath, bodyPath, outDir, model := os.Getenv("KIRO_PROBE_CRED"), os.Getenv("KIRO_PROBE_BODY"), os.Getenv("KIRO_PROBE_OUT"), os.Getenv("KIRO_PROBE_MODEL")
	if credPath == "" || bodyPath == "" || outDir == "" || model == "" {
		t.Skip("KIRO_PROBE_CRED, KIRO_PROBE_BODY, KIRO_PROBE_MODEL and KIRO_PROBE_OUT are required")
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(credPath)
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(stored, &metadata); err != nil {
		t.Fatal(err)
	}
	authID := filepath.Base(credPath)
	auth := &cliproxyauth.Auth{ID: authID, Provider: "kiro", Metadata: metadata}

	// Register this credential's stored catalogue the way model.for_auth does,
	// so the payload carries the same model capability as in production.
	var catalog struct {
		Models []struct {
			ModelID     string          `json:"modelId"`
			Schema      json.RawMessage `json:"additionalModelRequestFieldsSchema"`
			TokenLimits struct {
				MaxInputTokens int64 `json:"maxInputTokens"`
			} `json:"tokenLimits"`
		} `json:"models"`
	}
	if raw, ok := metadata["kiro_model_catalog"]; ok {
		encoded, _ := json.Marshal(raw)
		_ = json.Unmarshal(encoded, &catalog)
	}
	capabilities := make([]modelcapabilities.Capability, 0, len(catalog.Models))
	for _, entry := range catalog.Models {
		id := strings.TrimSpace(entry.ModelID)
		if strings.EqualFold(id, "auto") {
			id = "kiro/auto"
		}
		capability := modelcapabilities.Parse(id, entry.Schema)
		capability.InputTokenLimit = entry.TokenLimits.MaxInputTokens
		capabilities = append(capabilities, capability)
	}
	modelcapabilities.ReplaceForAuth(authID, capabilities)

	body, err := os.ReadFile(bodyPath)
	if err != nil {
		t.Fatal(err)
	}
	transport := &probeTransport{base: getKiroPooledHTTPClient().Transport, outDir: outDir}
	original := kiroHTTPClientFor
	kiroHTTPClientFor = func(context.Context, *config.Config, *cliproxyauth.Auth, time.Duration) *http.Client {
		return &http.Client{Transport: transport}
	}
	t.Cleanup(func() { kiroHTTPClientFor = original })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	request := cliproxyexecutor.Request{Model: model, Payload: body}
	options := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude"), OriginalRequest: body, Stream: true}
	started := time.Now()
	result, err := NewKiroExecutor(nil).ExecuteStream(ctx, auth, request, options)
	var sse bytes.Buffer
	var streamErr string
	if err != nil {
		streamErr = err.Error()
	} else {
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				streamErr = chunk.Err.Error()
				continue
			}
			sse.Write(chunk.Payload)
		}
	}
	_ = os.WriteFile(filepath.Join(outDir, "client-sse.txt"), sse.Bytes(), 0o600)

	summary := map[string]any{"auth": authID, "model": model, "elapsed_ms": time.Since(started).Milliseconds(), "executor_error": streamErr}
	if transport.body != nil {
		raw := transport.body.raw.Bytes()
		_ = os.WriteFile(filepath.Join(outDir, "upstream-raw.bin"), raw, 0o600)
		frames := decodeProbeFrames(raw, transport.body.reads)
		var lines bytes.Buffer
		counts := map[string]int{}
		for _, frame := range frames {
			encoded, _ := json.Marshal(frame)
			lines.Write(append(encoded, '\n'))
			key := fmt.Sprintf("%v/%v", frame.Headers[":message-type"], frame.Headers[":event-type"])
			if exception, ok := frame.Headers[":exception-type"]; ok {
				key += "/" + fmt.Sprint(exception)
			}
			counts[key]++
		}
		_ = os.WriteFile(filepath.Join(outDir, "frames.jsonl"), lines.Bytes(), 0o600)
		summary["upstream_bytes"] = len(raw)
		summary["frames"] = len(frames)
		summary["frame_kinds"] = counts
	}
	encoded, _ := json.MarshalIndent(summary, "", "  ")
	_ = os.WriteFile(filepath.Join(outDir, "summary.json"), encoded, 0o600)
	t.Logf("%s", encoded)
}
