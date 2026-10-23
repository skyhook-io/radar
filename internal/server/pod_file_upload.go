package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	k8sexec "k8s.io/client-go/util/exec"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/errorlog"
	"github.com/skyhook-io/radar/internal/k8s"
)

// podFileUploadMaxBytes caps one upload's body. The UI mirrors it as
// POD_FILE_UPLOAD_MAX_BYTES so it can refuse before sending.
var podFileUploadMaxBytes int64 = 100 << 20

// podFileMaxConcurrentWrites bounds writes in flight across all users. Each
// one holds an exec stream open for as long as its body takes to arrive.
const podFileMaxConcurrentWrites = 4

// podFileUploadOutputCap bounds what the remote command may print. The script
// writes a couple of short lines; anything more is noise, not information.
const podFileUploadOutputCap = 16 << 10

const podFileUploadCleanupTimeout = 10 * time.Second

// podFileUploadCommitTimeout bounds the wait for the container once the whole
// body has been handed over. All that is left then is a byte count and a
// rename; a session still open after this is one the kubelet never closed.
var podFileUploadCommitTimeout = 2 * time.Minute

// podFileUploadStallTimeout ends an upload whose body stops arriving. A
// refusal still reads the body to its end, so without it a client trickling
// bytes could hold one of the few upload slots indefinitely.
var podFileUploadStallTimeout = 2 * time.Minute

// When the container refuses before reading the body, the rest of it is
// still on its way. Reading a bounded amount lets the browser receive the
// refusal; past that, closing the connection is cheaper than taking the file.
const (
	podFileUploadDrainCap     = 8 << 20
	podFileUploadDrainTimeout = 10 * time.Second
)

const (
	uploadCodeDisabled         = "exec_disabled"
	uploadCodeBadRequest       = "bad_request"
	uploadCodeTooLarge         = "file_too_large"
	uploadCodeLengthRequired   = "length_required"
	uploadCodeBusy             = "too_many_uploads"
	uploadCodeForbidden        = "forbidden"
	uploadCodePodNotFound      = "pod_not_found"
	uploadCodeNoSuchContainer  = "container_not_found"
	uploadCodeDirNotFound      = "directory_not_found"
	uploadCodeNotADirectory    = "not_a_directory"
	uploadCodeExists           = "file_exists"
	uploadCodeNotARegularFile  = "not_a_regular_file"
	uploadCodeMountPoint       = "mount_point"
	uploadCodeModeUnknown      = "mode_not_preserved"
	uploadCodeOwnerChange      = "ownership_change"
	uploadCodeIncomplete       = "incomplete_upload"
	uploadCodeSymlinkedDir     = "symlinked_directory"
	uploadCodeStalled          = "upload_stalled"
	uploadCodeReadOnly         = "read_only_filesystem"
	uploadCodePermissionDenied = "permission_denied"
	uploadCodeNoSpace          = "no_space"
	uploadCodeNoShell          = "no_shell"
	uploadCodeMissingTools     = "container_missing_tools"
	uploadCodeUncertain        = "outcome_unknown"
	uploadCodeFailed           = "upload_failed"
)

// Exit statuses podFileUploadScript uses to say why it refused. They sit
// clear of what the tools it runs return (1, 2, 126, 127, 128+signal).
const (
	uploadExitDirNotFound   = 20
	uploadExitNotADirectory = 21
	uploadExitExists        = 22
	uploadExitNotRegular    = 23
	uploadExitMountPoint    = 24
	uploadExitModeUnknown   = 25
	uploadExitOwnerChange   = 26
	uploadExitIncomplete    = 27
	uploadExitSymlinkedDir  = 28
)

// podFileUploadScript writes stdin to a file without ever exposing a partial
// one. Everything the user chose arrives as a positional argument — $1 the
// directory, $2 the file name, $3 "1" to allow replacing, $4 the byte count
// the request declared — and nothing the user sent is part of the script.
//
// The bytes go to a temp file in the same directory, so the final step is a
// rename within one filesystem: readers see the old file or the new one,
// never a truncated one. Disconnects surface as an early EOF on stdin, which
// the byte count catches; the EXIT trap removes the temp file on every path.
//
// A directory reached through a symlink is refused rather than followed: the
// browser shows paths, and writing somewhere else than the path the user
// chose is the surprise to avoid. Refusing names the real location.
//
// When replacing, the temp file takes the old one's owner before the body is
// written, so a user who could not keep that owner is refused before the
// transfer; the mode is applied last, after the write (a read-only mode would
// stop a non-root writer) and after chown (which clears setuid and setgid).
//
// The commit never treats the destination as a directory (-T), so a directory
// that appears under that name mid-upload refuses it instead of swallowing the
// file. Without permission to replace, ln does the commit: it fails if the
// name exists, which closes the gap between the existence check and the
// rename. Where -T or hard links are missing, the plain commands run after a
// second check; the window that leaves is the time between two syscalls.
//
// Every failure reads stdin to its end before exiting. An exec whose process
// exits with stdin still arriving is never closed by the kubelet, which
// leaves the request hanging; reading the rest is what lets the refusal
// arrive. The UI checks what it can beforehand so this is rarely paid.
//
// The mount check reads /proc/self/mountinfo, so it misses a mount point
// whose path the kernel escapes (a name with spaces). mv onto a mount point
// then fails with EBUSY, which the classifier reports the same way.
const podFileUploadScript = `dir=$1 name=$2 overwrite=$3 size=$4 tmp= replace=
set -f
drain() { cat >/dev/null 2>&1 || while read -r _; do :; done; }
trap 'rc=$?; [ -n "$tmp" ] && rm -f -- "$tmp"; [ $rc -ne 0 ] && drain; exit $rc' EXIT
trap 'exit 129' HUP INT TERM PIPE
if [ ! -e "$dir" ]; then echo "radar: directory not found" >&2; exit 20; fi
if [ ! -d "$dir" ]; then echo "radar: not a directory" >&2; exit 21; fi
cd -- "$dir" || exit 1
here=$(pwd -P) || exit 1
if [ "$here" != "$dir" ]; then echo "radar: resolves to $here" >&2; exit 28; fi
dest=./$name
check_dest() {
  if [ -L "$dest" ] || { [ -e "$dest" ] && [ ! -f "$dest" ]; }; then echo "radar: not a regular file" >&2; exit 23; fi
  if [ -e "$dest" ] && [ "$overwrite" != 1 ]; then echo "radar: file exists" >&2; exit 22; fi
}
check_dest
if [ -e "$dest" ]; then
  case $here in /) abs=/$name ;; *) abs=$here/$name ;; esac
  if [ -r /proc/self/mountinfo ]; then
    while read -r _ _ _ _ mp _; do
      if [ "$mp" = "$abs" ]; then echo "radar: mount point" >&2; exit 24; fi
    done < /proc/self/mountinfo
  fi
fi
tmp=$(mktemp ./.radar-upload.XXXXXX) || exit $?
echo "tmp=${tmp#./}"
if [ -e "$dest" ]; then
  replace=1
  set -- $(ls -nd -- "$dest")
  if [ "$3" != "$(id -u)" ] || [ "$4" != "$(id -g)" ]; then
    chown "$3:$4" "$tmp" 2>/dev/null || { echo "radar: owner $3:$4 differs" >&2; exit 26; }
  fi
fi
cat > "$tmp" || exit $?
n=$(wc -c < "$tmp") || exit $?
[ $n -eq "$size" ] || { echo "radar: received $n of $size bytes" >&2; exit 27; }
if [ -n "$replace" ]; then
  if ! chmod --reference="$dest" "$tmp" 2>/dev/null; then
    mode=$(stat -c %a -- "$dest" 2>/dev/null) && chmod "$mode" "$tmp" || { echo "radar: cannot read the file mode" >&2; exit 25; }
  fi
  echo replaced
else
  chmod "$(printf %o $((0666 & ~0$(umask))))" "$tmp" || exit 1
fi
if [ "$overwrite" != 1 ]; then
  if ln -T -- "$tmp" "$dest" 2>/dev/null; then exit 0; fi
  check_dest
  if ln -- "$tmp" "$dest" 2>/dev/null; then exit 0; fi
  check_dest
fi
check_dest
mv -f -T -- "$tmp" "$dest" 2>/dev/null && exit 0
check_dest
mv -f -- "$tmp" "$dest"
`

func podFileUploadCommand(dir, name string, overwrite bool, size int64) []string {
	ow := "0"
	if overwrite {
		ow = "1"
	}
	return []string{"/bin/sh", "-c", podFileUploadScript, "sh", dir, name, ow, strconv.FormatInt(size, 10)}
}

const podFileUploadCleanupScript = `cd -- "$1" && rm -f -- "./$2"`

var podFileUploadTempName = regexp.MustCompile(`^\.radar-upload\.[A-Za-z0-9]+$`)

// validUploadName accepts a single path segment. The script prefixes it with
// "./" before use, so a leading dash is safe; what must never get through is
// anything that names a different directory.
func validUploadName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 255 {
		return false
	}
	return !strings.ContainsAny(name, "/\x00")
}

type podFileUploadResult struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Replaced bool   `json:"replaced"`
}

func (s *Server) writePodFileError(w http.ResponseWriter, status int, code, message string) {
	s.writeErrorCode(w, status, code, message)
}

// uploadBody feeds the request body to the exec's stdin. A failed read means
// the browser went away or the cap was hit; the exec is cancelled at once so
// the remote side stops waiting, and the byte count in the script refuses
// whatever partial file the half-closed stdin produced.
type uploadBody struct {
	readMu sync.Mutex // serializes reads of r between client-go and drain
	r      io.Reader
	rc     *http.ResponseController
	done   atomic.Bool
	sawEOF atomic.Bool
	errMu  sync.Mutex
	err    error
	cancel context.CancelFunc

	eofOnce  sync.Once
	watchdog *time.Timer
}

func (u *uploadBody) Read(p []byte) (int, error) {
	// client-go's stdin copier can outlive the stream; once the handler has
	// its answer the request body is no longer ours to read.
	if u.done.Load() {
		return 0, io.EOF
	}
	u.readMu.Lock()
	if u.rc != nil {
		_ = u.rc.SetReadDeadline(time.Now().Add(podFileUploadStallTimeout))
	}
	n, err := u.r.Read(p)
	if err == io.EOF {
		u.sawEOF.Store(true)
	}
	if err == io.EOF && u.rc != nil {
		// net/http keeps reading after the body to notice a client leaving;
		// a stall deadline left in place would cancel the commit.
		_ = u.rc.SetReadDeadline(time.Time{})
	}
	u.readMu.Unlock()
	if err == io.EOF {
		u.eofOnce.Do(func() {
			u.errMu.Lock()
			if !u.done.Load() {
				u.watchdog = time.AfterFunc(podFileUploadCommitTimeout, u.cancel)
			}
			u.errMu.Unlock()
		})
	}
	if err != nil && err != io.EOF {
		u.errMu.Lock()
		u.err = err
		u.errMu.Unlock()
		u.cancel()
	}
	return n, err
}

func (u *uploadBody) finish() error {
	u.done.Store(true)
	u.errMu.Lock()
	defer u.errMu.Unlock()
	if u.watchdog != nil {
		u.watchdog.Stop()
	}
	return u.err
}

// drain discards up to max more bytes of the body, waiting at most until
// the read deadline the caller set.
func (u *uploadBody) drain(max int64) {
	u.readMu.Lock()
	defer u.readMu.Unlock()
	_, _ = io.CopyN(io.Discard, u.r, max)
}

// cappedBuffer keeps the first max bytes written to it and drops the rest
// without failing the writer, so a chatty command cannot grow memory or abort
// the stream that carries its exit status.
type cappedBuffer struct {
	buf bytes.Buffer
	max int
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
		} else {
			c.buf.Write(p)
		}
	}
	return len(p), nil
}

func (c *cappedBuffer) String() string { return c.buf.String() }

// handlePodFileUpload writes the request body into a file in a pod container.
// Anyone who may exec can already write files through the terminal, so
// uploads ride on that permission rather than a switch of their own: the exec
// runs as the requesting user, RBAC on pods/exec decides who may write,
// --disable-exec turns uploads off, and the file is written as the
// container's own OS user.
// POST /api/pods/{ns}/{name}/files/upload?container=X&dir=/path&name=file[&overwrite=true]
func (s *Server) handlePodFileUpload(w http.ResponseWriter, r *http.Request) {
	if k8s.ForceDisableExec {
		s.writePodFileError(w, http.StatusForbidden, uploadCodeDisabled,
			"Exec is turned off on this Radar (--disable-exec), and uploading runs over exec.")
		return
	}
	if !s.requireConnected(w) {
		return
	}

	namespace := chi.URLParam(r, "namespace")
	podName := chi.URLParam(r, "name")
	q := r.URL.Query()
	container := q.Get("container")
	name := q.Get("name")
	overwrite := q.Get("overwrite") == "true"
	dir := q.Get("dir")
	if dir == "" || !path.IsAbs(dir) {
		s.writePodFileError(w, http.StatusBadRequest, uploadCodeBadRequest, "dir must be an absolute path")
		return
	}
	dir = path.Clean(dir)
	if !validUploadName(name) {
		s.writePodFileError(w, http.StatusBadRequest, uploadCodeBadRequest,
			"name must be a single file name: not empty, not . or .., without / or NUL, at most 255 bytes")
		return
	}
	destPath := path.Join(dir, name)

	limit := podFileUploadMaxBytes
	// The script checks what arrived against the declared length, so a
	// request without one cannot be told apart from a cut-off one.
	if r.ContentLength < 0 {
		s.writePodFileError(w, http.StatusLengthRequired, uploadCodeLengthRequired, "Content-Length is required")
		return
	}
	if r.ContentLength > limit {
		s.writePodFileError(w, http.StatusRequestEntityTooLarge, uploadCodeTooLarge,
			fmt.Sprintf("File is %d bytes; uploads are limited to %d bytes", r.ContentLength, limit))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)

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
			"Too many uploads are already in progress on this Radar; try again when one finishes")
		return
	}

	auth.AuditLog(r, namespace, podName)

	execCtx, cancel := context.WithCancel(r.Context())
	defer cancel()
	rc := http.NewResponseController(w)
	// net/http does not clear a deadline set here before reading the next
	// request on a kept-alive connection.
	defer func() { _ = rc.SetReadDeadline(time.Time{}) }()
	body := &uploadBody{r: r.Body, rc: rc, cancel: cancel}
	stdout := &cappedBuffer{max: podFileUploadOutputCap}
	stderr := &cappedBuffer{max: podFileUploadOutputCap}

	streamErr := s.runPodExec(execCtx, client, config, namespace, podName, container,
		podFileUploadCommand(dir, name, overwrite, r.ContentLength), body, stdout, stderr)
	bodyErr := body.finish()
	if !body.sawEOF.Load() {
		// Otherwise net/http reads the rest of the body before answering,
		// and a client that stopped sending never gets the answer.
		w.Header().Set("Connection", "close")
	}

	if streamErr == nil && bodyErr == nil {
		s.writeJSON(w, podFileUploadResult{
			Path:     destPath,
			Size:     r.ContentLength,
			Replaced: strings.Contains(stdout.String(), "\nreplaced\n"),
		})
		return
	}

	tmpName := uploadTempName(stdout.String())
	var exitErr k8sexec.CodeExitError
	if !errors.As(streamErr, &exitErr) && tmpName != "" {
		// The command's status never arrived, so whether its trap ran is
		// unknown. Removing the temp file again is harmless either way: a
		// commit that already happened moved it, and one that has not yet
		// will now fail and leave the destination as it was.
		s.cleanupUploadTemp(client, config, namespace, podName, container, dir, tmpName)
	}

	var maxErr *http.MaxBytesError
	if errors.As(bodyErr, &maxErr) {
		s.writePodFileError(w, http.StatusRequestEntityTooLarge, uploadCodeTooLarge,
			fmt.Sprintf("Uploads are limited to %d bytes", limit))
		return
	}
	if errors.Is(bodyErr, os.ErrDeadlineExceeded) {
		s.writePodFileError(w, http.StatusRequestTimeout, uploadCodeStalled,
			fmt.Sprintf("No data arrived for %s, so the upload was stopped; nothing was written", podFileUploadStallTimeout))
		return
	}
	if bodyErr != nil {
		log.Printf("[copy] upload to %s/%s path=%s stopped: request body: %v", namespace, podName, destPath, bodyErr)
		errorlog.Record("copy", "warning", "upload to %s/%s path=%s stopped before the file arrived: %v", namespace, podName, destPath, bodyErr)
		s.writePodFileError(w, http.StatusBadRequest, uploadCodeIncomplete,
			"The upload stopped before the whole file arrived; nothing was written")
		return
	}

	if bodyErr == nil {
		_ = rc.SetReadDeadline(time.Now().Add(podFileUploadDrainTimeout))
		body.drain(podFileUploadDrainCap)
	}

	status, code, message := classifyPodFileUploadError(destPath, streamErr, stderr.String())
	if status >= 500 {
		log.Printf("[copy] Failed to upload %s/%s path=%s: %v, stderr: %s", namespace, podName, destPath, streamErr, stderr.String())
		errorlog.Record("copy", "error", "upload to %s/%s path=%s failed: %v", namespace, podName, destPath, streamErr)
	}
	s.writePodFileError(w, status, code, message)
}

func (s *Server) podFileWriteSlotsChan() chan struct{} {
	s.podFileWriteSlotsOnce.Do(func() {
		s.podFileWriteSlots = make(chan struct{}, podFileMaxConcurrentWrites)
	})
	return s.podFileWriteSlots
}

func uploadTempName(stdout string) string {
	for _, line := range strings.Split(stdout, "\n") {
		if name, ok := strings.CutPrefix(line, "tmp="); ok && podFileUploadTempName.MatchString(name) {
			return name
		}
	}
	return ""
}

func (s *Server) cleanupUploadTemp(client kubernetes.Interface, config *rest.Config, namespace, podName, container, dir, tmpName string) {
	ctx, cancel := context.WithTimeout(context.Background(), podFileUploadCleanupTimeout)
	defer cancel()
	cmd := []string{"/bin/sh", "-c", podFileUploadCleanupScript, "sh", dir, tmpName}
	var stderr cappedBuffer
	stderr.max = podFileUploadOutputCap
	if err := s.runPodExec(ctx, client, config, namespace, podName, container, cmd, nil, io.Discard, &stderr); err != nil {
		log.Printf("[copy] could not remove upload temp file %s/%s %s/%s: %v %s", namespace, podName, dir, tmpName, err, stderr.String())
		errorlog.Record("copy", "warning", "could not remove upload temp file %s in %s/%s %s: %v", tmpName, namespace, podName, dir, err)
	}
}

// runPodExec runs cmd in the container and waits for it to finish.
func (s *Server) runPodExec(ctx context.Context, client kubernetes.Interface, config *rest.Config,
	namespace, podName, container string, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error {

	req := client.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   cmd,
			Stdin:     stdin != nil,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	executor, err := s.executorFor(config, req.URL())
	if err != nil {
		return err
	}
	return executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
	})
}

// isExecForbidden recognises the apiserver refusing pods/exec. The WebSocket
// executor hands back a StatusError, but the SPDY fallback it retries with
// only has the refusal as text.
func isExecForbidden(err error) bool {
	if err == nil {
		return false
	}
	if apierrors.IsForbidden(err) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "unable to upgrade connection") && strings.Contains(msg, "forbidden")
}

// upgradeRefusal returns the apiserver's refusal of the exec upgrade, from
// either executor, or "" when the exec got that far.
func upgradeRefusal(err error) string {
	if err == nil {
		return ""
	}
	if apierrors.IsNotFound(err) || apierrors.IsBadRequest(err) {
		return err.Error()
	}
	if msg := err.Error(); strings.Contains(msg, "unable to upgrade connection") {
		return msg
	}
	return ""
}

// classifyPodFileUploadError maps a failed upload to a status, a code the UI
// can act on, and a message for the person who tried it.
func classifyPodFileUploadError(destPath string, streamErr error, stderr string) (int, string, string) {
	dir := path.Dir(destPath)
	if isExecForbidden(streamErr) {
		return http.StatusForbidden, uploadCodeForbidden,
			fmt.Sprintf("You are not allowed to exec into this pod, which uploading requires: %v", streamErr)
	}

	// The apiserver turned the exec away, so nothing ran in the container.
	if upgrade := upgradeRefusal(streamErr); upgrade != "" {
		switch {
		case strings.Contains(upgrade, "is not valid for pod"):
			return http.StatusBadRequest, uploadCodeNoSuchContainer, "That container is not part of this pod"
		case strings.Contains(upgrade, "not found"):
			return http.StatusNotFound, uploadCodePodNotFound, "The pod no longer exists"
		}
	}

	var exitErr k8sexec.CodeExitError
	if errors.As(streamErr, &exitErr) {
		switch exitErr.Code {
		case uploadExitDirNotFound:
			return http.StatusNotFound, uploadCodeDirNotFound, fmt.Sprintf("Directory not found: %s", dir)
		case uploadExitNotADirectory:
			return http.StatusBadRequest, uploadCodeNotADirectory, fmt.Sprintf("Not a directory: %s", dir)
		case uploadExitExists:
			return http.StatusConflict, uploadCodeExists, fmt.Sprintf("%s already exists", destPath)
		case uploadExitNotRegular:
			return http.StatusConflict, uploadCodeNotARegularFile,
				fmt.Sprintf("%s is a symlink, directory or special file; only regular files can be replaced", destPath)
		case uploadExitMountPoint:
			return http.StatusConflict, uploadCodeMountPoint,
				fmt.Sprintf("%s is mounted into the container on its own (a single-file volume mount); change it at its source", destPath)
		case uploadExitModeUnknown:
			return http.StatusConflict, uploadCodeModeUnknown,
				fmt.Sprintf("Could not read the permissions of %s to keep them, so it was left unchanged", destPath)
		case uploadExitOwnerChange:
			return http.StatusConflict, uploadCodeOwnerChange,
				fmt.Sprintf("%s is owned by another user; replacing it as the container's user would change its owner, so it was left unchanged", destPath)
		case uploadExitSymlinkedDir:
			real := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(stderr), "radar: resolves to "))
			return http.StatusConflict, uploadCodeSymlinkedDir,
				fmt.Sprintf("%s goes through a symlink and resolves to %s; browse to %s to upload there", dir, real, real)
		case uploadExitIncomplete:
			return http.StatusBadRequest, uploadCodeIncomplete, "The upload stopped before the whole file arrived; nothing was written"
		}
	}

	combined := strings.ToLower(fmt.Sprintf("%v %s", streamErr, stderr))
	switch {
	case isShellMissing(combined):
		return http.StatusNotImplemented, uploadCodeNoShell,
			"Container has no shell (/bin/sh), which uploading needs. Distroless and scratch images cannot take uploads."
	case isCommandMissing(streamErr, stderr):
		return http.StatusNotImplemented, uploadCodeMissingTools,
			"Container lacks a standard command uploading needs (it uses sh, mktemp, cat, wc, ln, mv, chmod, stat, ls, id and rm). Minimal images may not support uploads."
	case strings.Contains(combined, "read-only file system"):
		return http.StatusConflict, uploadCodeReadOnly,
			fmt.Sprintf("%s is on a read-only filesystem", dir)
	case strings.Contains(combined, "no space left"):
		return http.StatusInsufficientStorage, uploadCodeNoSpace,
			fmt.Sprintf("No space left on the filesystem holding %s", dir)
	case strings.Contains(combined, "resource busy"):
		return http.StatusConflict, uploadCodeMountPoint,
			fmt.Sprintf("%s is mounted into the container on its own (a single-file volume mount); change it at its source", destPath)
	case strings.Contains(combined, "permission denied") || strings.Contains(combined, "operation not permitted"):
		return http.StatusForbidden, uploadCodePermissionDenied,
			fmt.Sprintf("Permission denied: the container's user cannot write to %s", dir)
	}

	if !errors.As(streamErr, &exitErr) {
		return http.StatusBadGateway, uploadCodeUncertain,
			fmt.Sprintf("The connection to the container dropped before it confirmed the upload; refresh the listing to see whether %s changed", destPath)
	}
	return http.StatusInternalServerError, uploadCodeFailed, fmt.Sprintf("Upload failed: %s", strings.TrimSpace(fmt.Sprintf("%v %s", streamErr, stderr)))
}
