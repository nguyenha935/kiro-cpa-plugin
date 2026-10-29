package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"syscall"
	"testing"
)

func TestRetryableTransportErrorsUseValues(t *testing.T) {
	for _, cause := range []error{syscall.ECONNREFUSED, syscall.ECONNRESET, syscall.EPIPE, syscall.ETIMEDOUT, syscall.ENETUNREACH, syscall.EHOSTUNREACH, io.EOF, io.ErrUnexpectedEOF} {
		err := &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", cause)}
		if !isRetryableError(fmt.Errorf("request: %w", err)) {
			t.Errorf("wrapped %v was not retryable", cause)
		}
	}
	for _, err := range []error{nil, context.Canceled, context.DeadlineExceeded, errors.New("connection refused"), errors.New("timeout in client input"), &net.OpError{Op: "dial", Net: "tcp", Err: syscall.EINVAL}} {
		if isRetryableError(err) {
			t.Errorf("%v was retryable without a transient error value", err)
		}
	}
}

func TestRetryableTransportErrorsKeepTransientDNSAndSyscalls(t *testing.T) {
	for _, cause := range []error{&net.DNSError{Err: "temporary resolver failure", Name: "example.com", IsTemporary: true}, syscall.EINTR, syscall.EMFILE, syscall.ENFILE} {
		err := &net.OpError{Op: "dial", Net: "tcp", Err: cause}
		if !isRetryableError(err) {
			t.Errorf("transient transport error %v lost retry classification", cause)
		}
	}
	if isRetryableError(&net.DNSError{Err: "host not found", Name: "example.com", IsNotFound: true}) {
		t.Fatal("permanent DNS failure became retryable")
	}
}
