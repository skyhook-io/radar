package server

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"strings"
	"testing"
)

// buildTarArchive returns a tar archive containing a single regular file.
func buildTarArchive(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name:     name,
		Mode:     0o644,
		Size:     int64(len(content)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatalf("write tar content: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	return buf.Bytes()
}

// fakeTarExec simulates the pod-exec tar attempts. Each attempt serves the
// archive from the requested offset, optionally stopping early with an
// injected outcome.
type fakeTarExec struct {
	archive []byte
	// stopAfter[i] truncates attempt i after that many bytes (relative to the
	// attempt's offset); -1 means serve to the end.
	stopAfter []int
	// errs[i] is returned by attempt i when it stops (nil simulates the
	// "exec reported success but the stream was truncated" mode).
	errs    []error
	offsets []uint64
}

func (f *fakeTarExec) run(_ context.Context, offset uint64, stdout io.Writer) error {
	attempt := len(f.offsets)
	f.offsets = append(f.offsets, offset)

	if offset > uint64(len(f.archive)) {
		return fmt.Errorf("offset %d beyond archive", offset)
	}
	data := f.archive[offset:]
	stop := -1
	var err error
	if attempt < len(f.stopAfter) {
		stop = f.stopAfter[attempt]
	}
	if attempt < len(f.errs) {
		err = f.errs[attempt]
	}
	if stop >= 0 && stop < len(data) {
		data = data[:stop]
	}
	// Write in small chunks like a real stream would arrive.
	for len(data) > 0 {
		n := 1024
		if n > len(data) {
			n = len(data)
		}
		if _, werr := stdout.Write(data[:n]); werr != nil {
			return werr
		}
		data = data[n:]
	}
	return err
}

func randomContent(t *testing.T, size int) []byte {
	t.Helper()
	content := make([]byte, size)
	rng := rand.New(rand.NewSource(1511))
	if _, err := rng.Read(content); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return content
}

// extractSingleFile reads the archive from the stream the way the download
// handler does: header, then the entry body only (never the tar trailer).
func extractSingleFile(t *testing.T, r io.Reader) []byte {
	t.Helper()
	tr := tar.NewReader(r)
	header, err := tr.Next()
	if err != nil {
		t.Fatalf("tar.Next: %v", err)
	}
	if header.Typeflag != tar.TypeReg {
		t.Fatalf("unexpected typeflag %v", header.Typeflag)
	}
	var out bytes.Buffer
	if _, err := io.CopyN(&out, tr, header.Size); err != nil {
		t.Fatalf("copy entry: %v", err)
	}
	return out.Bytes()
}

func TestResumingTarStreamCleanTransfer(t *testing.T) {
	content := randomContent(t, 64*1024)
	fake := &fakeTarExec{archive: buildTarArchive(t, "data.csv", content)}

	stream := newResumingTarStream(context.Background(), fake.run, 3)
	defer stream.Close()

	got := extractSingleFile(t, stream)
	if !bytes.Equal(got, content) {
		t.Fatalf("content mismatch: got %d bytes, want %d", len(got), len(content))
	}
	if len(fake.offsets) != 1 {
		t.Fatalf("expected a single attempt, got offsets %v", fake.offsets)
	}
}

func TestResumingTarStreamResumesAfterTransportError(t *testing.T) {
	content := randomContent(t, 256*1024)
	archive := buildTarArchive(t, "data.csv", content)
	fake := &fakeTarExec{
		archive:   archive,
		stopAfter: []int{100 * 1024, -1},
		errs:      []error{errors.New("next reader: unexpected EOF")},
	}

	stream := newResumingTarStream(context.Background(), fake.run, 3)
	defer stream.Close()

	got := extractSingleFile(t, stream)
	if !bytes.Equal(got, content) {
		t.Fatalf("content mismatch after resume: got %d bytes, want %d", len(got), len(content))
	}
	if len(fake.offsets) != 2 {
		t.Fatalf("expected 2 attempts, got offsets %v", fake.offsets)
	}
	if fake.offsets[0] != 0 {
		t.Fatalf("first attempt should start at 0, got %d", fake.offsets[0])
	}
	if fake.offsets[1] != 100*1024 {
		t.Fatalf("resume should start at the truncation point %d, got %d", 100*1024, fake.offsets[1])
	}
}

// The failure mode from issue #1511: the exec completes without error (the
// transport surfaced a normal close) but the tar stream is truncated.
func TestResumingTarStreamResumesAfterSilentTruncation(t *testing.T) {
	content := randomContent(t, 256*1024)
	archive := buildTarArchive(t, "data.csv", content)
	fake := &fakeTarExec{
		archive:   archive,
		stopAfter: []int{64 * 1024, -1},
		errs:      []error{nil},
	}

	stream := newResumingTarStream(context.Background(), fake.run, 3)
	defer stream.Close()

	got := extractSingleFile(t, stream)
	if !bytes.Equal(got, content) {
		t.Fatalf("content mismatch after silent truncation: got %d bytes, want %d", len(got), len(content))
	}
	if len(fake.offsets) != 2 {
		t.Fatalf("expected 2 attempts, got offsets %v", fake.offsets)
	}
	if fake.offsets[1] != 64*1024 {
		t.Fatalf("resume should start at %d, got %d", 64*1024, fake.offsets[1])
	}
}

func TestResumingTarStreamFirstAttemptErrorPropagates(t *testing.T) {
	execErr := errors.New(`exec: "tar": executable file not found in $PATH`)
	fake := &fakeTarExec{
		archive:   buildTarArchive(t, "data.csv", []byte("hello")),
		stopAfter: []int{0},
		errs:      []error{execErr},
	}

	stream := newResumingTarStream(context.Background(), fake.run, 3)
	defer stream.Close()

	_, err := io.ReadAll(stream)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "executable file not found") {
		t.Fatalf("expected original exec error, got: %v", err)
	}
	if len(fake.offsets) != 1 {
		t.Fatalf("a zero-byte first-attempt failure must not retry, got offsets %v", fake.offsets)
	}
}

func TestResumingTarStreamGivesUpAfterMaxRetries(t *testing.T) {
	content := randomContent(t, 64*1024)
	archive := buildTarArchive(t, "data.csv", content)
	transportErr := errors.New("next reader: unexpected EOF")
	fake := &fakeTarExec{
		archive:   archive,
		stopAfter: []int{1024, 0, 0, 0},
		errs:      []error{transportErr, transportErr, transportErr, transportErr},
	}

	stream := newResumingTarStream(context.Background(), fake.run, 2)
	defer stream.Close()

	_, err := io.ReadAll(stream)
	if err == nil {
		t.Fatal("expected error after exhausting retries, got nil")
	}
	// initial attempt + 2 retries
	if len(fake.offsets) != 3 {
		t.Fatalf("expected 3 attempts, got offsets %v", fake.offsets)
	}
}

func TestResumingTarStreamNoRetryAfterContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	content := randomContent(t, 64*1024)
	archive := buildTarArchive(t, "data.csv", content)
	fake := &fakeTarExec{
		archive:   archive,
		stopAfter: []int{1024},
		errs:      []error{errors.New("context canceled")},
	}

	stream := newResumingTarStream(ctx, fake.run, 5)
	defer stream.Close()

	buf := make([]byte, 2048)
	if _, err := io.ReadFull(stream, buf[:1024]); err != nil {
		t.Fatalf("initial read: %v", err)
	}
	cancel()

	_, err := io.ReadAll(stream)
	if err == nil {
		t.Fatal("expected error after cancel, got nil")
	}
	if len(fake.offsets) != 1 {
		t.Fatalf("must not retry after context cancel, got offsets %v", fake.offsets)
	}
}
