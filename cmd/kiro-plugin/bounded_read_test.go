package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// Discovery reads an upstream body into memory before parsing it. An unbounded
// read lets a broken or hostile upstream size that allocation, inside the shared
// CPA process, from a request the caller cannot see. Every other HTTP read in
// this plugin is capped at 1 MiB; these two were not.
//
// The assertion is on how much the server managed to send, not on the error:
// an oversized body fails to parse either way, so only the byte count separates
// a bounded read from one that buffers whatever arrives.
func TestProfileDiscoveryStopsReadingAnOversizedBody(t *testing.T) {
	t.Parallel()

	const cap = 1 << 20
	const ceiling = 32 << 20

	var served atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		chunk := make([]byte, 64<<10)
		for index := range chunk {
			chunk[index] = ' '
		}
		for served.Load() < ceiling {
			n, err := w.Write(chunk)
			served.Add(int64(n))
			if err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := listAvailableProfiles(ctx, server.Client(), server.URL, "access"); err == nil {
		t.Fatal("an oversized body was accepted as a profile list")
	}
	if ctx.Err() != nil {
		t.Fatalf("the read did not stop on its own: %v", ctx.Err())
	}

	// The server writes ahead of the reader, so the count is an upper bound on
	// what was buffered rather than an exact measure. A generous multiple of the
	// cap still separates "stopped at 1 MiB" from "read everything offered".
	if written := served.Load(); written >= ceiling {
		t.Fatalf("server sent %d bytes of an unbounded stream; a %d-byte cap would have ended it sooner", written, cap)
	}
}

// The cap must not truncate an ordinary response: discovery answers are small,
// and a cap that cut into one would break every listing instead of a hostile one.
func TestProfileDiscoveryStillReadsAnOrdinaryBody(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"profiles":[{"arn":"arn:aws:codewhisperer:us-east-1:123456789012:profile/ABCDEFGHIJKL","profileName":"default"}]}`)
	}))
	defer server.Close()

	profiles, err := listAvailableProfiles(context.Background(), server.Client(), server.URL, "access")
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 || profiles[0].ARN == "" {
		t.Fatalf("profiles = %+v, want the one the server sent", profiles)
	}
}
