package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	k8sexec "k8s.io/client-go/util/exec"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/errorlog"
	"github.com/skyhook-io/radar/internal/k8s"
)

// podFileEditTimeout bounds a save: one exec to re-read the file and one to
// write at most podFilePreviewByteCap bytes.
var podFileEditTimeout = 2 * time.Minute

const (
	editCodeChanged   = "file_changed"
	editCodeNotText   = "not_text"
	editCodeTooLarge  = "file_too_large"
	editCodeReadError = "read_failed"
)

var sha256HexPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

type podFileEditResult struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// handlePodFileEdit saves new contents over a text file the user previewed.
// It is gated exactly like an upload (exec permission, --disable-exec, off in
// Radar Cloud) and writes through the same script, so a save is never a
// truncate in place and keeps the file's owner and permissions.
//
// The request carries the SHA-256 the preview returned. The save is refused
// with 409 when the file no longer has it: once here, after re-reading the
// file, and again inside the container just before the rename. What is left
// uncovered is the moment between that last hash and the rename; over exec
// there is no way to make the compare and the swap one step.
//
// Like the preview, an edit is limited to UTF-8 text within
// podFilePreviewByteCap, checked here on the new contents too.
// PUT /api/pods/{ns}/{name}/files/content?container=X&path=/abs/file&sha256=<hex>
func (s *Server) handlePodFileEdit(w http.ResponseWriter, r *http.Request) {
	if cloudMode() {
		s.writePodFileError(w, http.StatusForbidden, uploadCodeCloud,
			"Editing files in pods isn't available in Radar Cloud.")
		return
	}
	if k8s.ForceDisableExec {
		s.writePodFileError(w, http.StatusForbidden, uploadCodeDisabled,
			"Exec is turned off on this Radar (--disable-exec), and saving files runs over exec.")
		return
	}
	if !s.requireConnected(w) {
		return
	}

	namespace := chi.URLParam(r, "namespace")
	podName := chi.URLParam(r, "name")
	q := r.URL.Query()
	container := q.Get("container")
	filePath := q.Get("path")
	if filePath == "" || !path.IsAbs(filePath) {
		s.writePodFileError(w, http.StatusBadRequest, uploadCodeBadRequest, "path must be an absolute file path")
		return
	}
	filePath = path.Clean(filePath)
	dir, name := path.Dir(filePath), path.Base(filePath)
	if !validUploadName(name) {
		s.writePodFileError(w, http.StatusBadRequest, uploadCodeBadRequest, fmt.Sprintf("Not a file: %s", filePath))
		return
	}
	expect := strings.ToLower(q.Get("sha256"))
	if !sha256HexPattern.MatchString(expect) {
		s.writePodFileError(w, http.StatusBadRequest, uploadCodeBadRequest,
			"sha256 must be the hex SHA-256 of the contents that were opened")
		return
	}

	content, err := io.ReadAll(http.MaxBytesReader(w, r.Body, podFilePreviewByteCap))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			s.writePodFileError(w, http.StatusRequestEntityTooLarge, editCodeTooLarge,
				fmt.Sprintf("Edited files are limited to %d bytes", podFilePreviewByteCap))
			return
		}
		s.writePodFileError(w, http.StatusBadRequest, uploadCodeIncomplete, "The new contents did not arrive in full; nothing was written")
		return
	}
	if classifyPreviewBytes(content).code == previewCodeBinaryFile {
		s.writePodFileError(w, http.StatusUnsupportedMediaType, editCodeNotText,
			"Only UTF-8 text can be saved from the editor")
		return
	}

	client := s.getClientForRequest(r)
	config := s.getConfigForRequest(r)
	if client == nil || config == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}

	slots := s.podFileWriteSlotsChan()
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	default:
		s.writePodFileError(w, http.StatusTooManyRequests, uploadCodeBusy,
			"Too many file writes are already in progress on this Radar; try again when one finishes")
		return
	}

	audit := auth.AuditActionDetails{Action: "edit_file", Kind: "Pod", Namespace: namespace, Name: podName,
		Container: container, Source: "ui", Outcome: "failed", Directory: dir, File: name, Bytes: int64(len(content))}
	defer func() { auth.AuditLogAction(r, audit) }()

	ctx, cancel := context.WithTimeout(r.Context(), podFileEditTimeout)
	defer cancel()

	current, status, code, message := s.readPodFileForEdit(ctx, client, config, namespace, podName, container, filePath)
	if status != 0 {
		audit.Outcome = code
		s.writePodFileError(w, status, code, message)
		return
	}
	if sha256Hex(current) != expect {
		audit.Outcome = editCodeChanged
		s.writePodFileError(w, http.StatusConflict, editCodeChanged,
			fmt.Sprintf("%s changed since you opened it, so your edit was not saved", filePath))
		return
	}

	stdout := &cappedBuffer{max: podFileUploadOutputCap}
	stderr := &cappedBuffer{max: podFileUploadOutputCap}
	streamErr := s.runPodExec(ctx, client, config, namespace, podName, container,
		podFileWriteCommand(dir, name, true, int64(len(content)), expect), bytes.NewReader(content), stdout, stderr)
	if streamErr == nil {
		audit.Outcome = "saved"
		s.writeJSON(w, podFileEditResult{Path: filePath, Size: int64(len(content)), SHA256: sha256Hex(content)})
		return
	}

	var exitErr k8sexec.CodeExitError
	if tmp := uploadTempName(stdout.String()); tmp != "" && (!errors.As(streamErr, &exitErr) || exitErr.Code >= 128) {
		s.cleanupUploadTemp(client, config, namespace, podName, container, dir, tmp)
	}
	if errors.As(streamErr, &exitErr) && exitErr.Code == uploadExitChanged {
		audit.Outcome = editCodeChanged
		s.writePodFileError(w, http.StatusConflict, editCodeChanged,
			fmt.Sprintf("%s changed since you opened it, so your edit was not saved", filePath))
		return
	}

	status, code, message = classifyPodFileWriteError(editWording, filePath, streamErr, stderr.String())
	audit.Outcome = code
	if status >= 500 {
		log.Printf("[copy] Failed to save %s/%s path=%s: %v, stderr: %s", namespace, podName, filePath, streamErr, stderr.String())
		errorlog.Record("copy", "error", "save to %s/%s path=%s failed: %v", namespace, podName, filePath, streamErr)
	}
	s.writePodFileError(w, status, code, message)
}

// readPodFileForEdit reads the file as the preview does, for the hash check.
// A file that is gone, grown past the edit limit or no longer text counts as
// changed: it is not the file the user opened.
func (s *Server) readPodFileForEdit(ctx context.Context, client kubernetes.Interface, config *rest.Config,
	namespace, podName, container, filePath string) ([]byte, int, string, string) {

	changed := func(why string) ([]byte, int, string, string) {
		return nil, http.StatusConflict, editCodeChanged,
			fmt.Sprintf("%s %s since you opened it, so your edit was not saved", filePath, why)
	}

	stream, openErr := s.openPodFileWithTar(ctx, client, config, namespace, podName, container, filePath)
	if openErr != nil && openErr.commandMissing && !openErr.shellMissing {
		stream, openErr = s.openPodFileWithCat(ctx, client, config, namespace, podName, container, filePath)
	}
	if openErr != nil {
		switch {
		case openErr.notFound:
			return changed("was removed")
		case openErr.notAFile:
			return changed("stopped being a regular file")
		}
		status, code := classifyOpenErrorForPreview(openErr)
		if code == previewCodeReadFailed {
			code = editCodeReadError
			log.Printf("[copy] Failed to re-read %s/%s path=%s before saving: %v, stderr: %s", namespace, podName, filePath, openErr.err, openErr.stderr)
		}
		return nil, status, code, openErr.message
	}
	if stream.size > podFilePreviewByteCap {
		_ = stream.Close()
		return changed("grew past the edit limit")
	}
	body, readErr := io.ReadAll(io.LimitReader(stream, stream.size+1))
	closeErr := stream.Close()
	if readErr != nil {
		log.Printf("[copy] Failed to re-read %s/%s path=%s before saving: %v", namespace, podName, filePath, readErr)
		return nil, http.StatusBadGateway, editCodeReadError,
			fmt.Sprintf("Could not re-read %s to check it is unchanged, so nothing was written", filePath)
	}
	if closeErr != nil {
		log.Printf("[copy] re-reading %s/%s path=%s ended with an error after the file arrived in full: %v", namespace, podName, filePath, closeErr)
	}
	return body, 0, "", ""
}
