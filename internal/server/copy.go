package server

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	k8sexec "k8s.io/client-go/util/exec"

	"github.com/skyhook-io/radar/internal/errorlog"
	"github.com/skyhook-io/radar/internal/images"
	rcpkg "github.com/skyhook-io/radar/pkg/remotecommand"
)

// PodFilesystem represents the file listing response for a pod container
type PodFilesystem struct {
	Root       *images.FileNode `json:"root"`
	TotalFiles int              `json:"totalFiles"`
}

// handlePodFileList lists files at a given path inside a pod container.
// GET /api/pods/{ns}/{name}/files?container=X&path=/
func (s *Server) handlePodFileList(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}

	namespace := chi.URLParam(r, "namespace")
	podName := chi.URLParam(r, "name")
	container := r.URL.Query().Get("container")
	dirPath := r.URL.Query().Get("path")
	if dirPath == "" {
		dirPath = "/"
	}

	// Clean the path to prevent traversal.
	// Use path.Clean (POSIX) not filepath.Clean — the path runs inside a Linux
	// container, but filepath is OS-specific and converts to backslashes on Windows.
	dirPath = path.Clean(dirPath)

	client := s.getClientForRequest(r)
	config := s.getConfigForRequest(r)
	if client == nil || config == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}

	// Use find to list files — provides type, size, timestamp, permissions
	// -maxdepth 1 lists only immediate children (like ls)
	// Output format: type\tsize\ttimestamp\tpermissions\tpath
	// Wrap in sh -c so the shell resolves PATH — direct exec via the container
	// runtime can fail to find binaries that are available through the shell's PATH.
	findCmd := fmt.Sprintf("find %s -maxdepth 1 -printf '%%y\\t%%s\\t%%T@\\t%%m\\t%%p\\n'", shellQuote(dirPath))
	cmd := []string{"/bin/sh", "-c", findCmd}

	req := client.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   cmd,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	exec, err := rcpkg.NewExecutor(config, req.URL())
	if err != nil {
		log.Printf("[copy] Failed to create executor for %s/%s: %v", namespace, podName, err)
		errorlog.Record("copy", "error", "failed to create executor for %s/%s: %v", namespace, podName, err)
		s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to create executor: %v", err))
		return
	}

	var stdout, stderr bytes.Buffer
	err = exec.StreamWithContext(r.Context(), remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		// find failed — could be missing command, unsupported flags (e.g. -printf on BusyBox), etc.
		// Always fall back to ls which is more universally available.
		findErrMsg := fmt.Sprintf("%v: %s", err, stderr.String())
		log.Printf("[copy] find failed for %s/%s (falling back to ls): %s", namespace, podName, findErrMsg)
		nodes, totalFiles, lsErr := s.listFilesWithLS(r, namespace, podName, container, dirPath)
		if lsErr != nil {
			errMsg := classifyExecError(findErrMsg, lsErr.Error())
			errorlog.Record("copy", "error", "file list failed for %s/%s: %s", namespace, podName, errMsg)
			s.writeError(w, http.StatusInternalServerError, errMsg)
			return
		}
		s.writeJSON(w, PodFilesystem{Root: buildRootNode(dirPath, nodes), TotalFiles: totalFiles})
		return
	}

	nodes, totalFiles := parseFindOutput(stdout.String(), dirPath)
	s.writeJSON(w, PodFilesystem{Root: buildRootNode(dirPath, nodes), TotalFiles: totalFiles})
}

// listFilesWithLS is a fallback when find is not available
func (s *Server) listFilesWithLS(r *http.Request, namespace, podName, container, dirPath string) ([]*images.FileNode, int, error) {
	client := s.getClientForRequest(r)
	config := s.getConfigForRequest(r)
	if client == nil || config == nil {
		return nil, 0, fmt.Errorf("cluster client not available")
	}

	cmd := []string{"/bin/sh", "-c", fmt.Sprintf("ls -la %s", shellQuote(dirPath))}

	req := client.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   cmd,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	exec, err := rcpkg.NewExecutor(config, req.URL())
	if err != nil {
		return nil, 0, err
	}

	var stdout, stderr bytes.Buffer
	err = exec.StreamWithContext(r.Context(), remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("%v: %s", err, stderr.String())
	}

	nodes := parseLSOutput(stdout.String(), dirPath)
	return nodes, len(nodes), nil
}

// handlePodFileDownload downloads a single file from a pod container.
// GET /api/pods/{ns}/{name}/files/download?container=X&path=/some/file
func (s *Server) handlePodFileDownload(w http.ResponseWriter, r *http.Request) {
	src := s.openPodFileForRequest(w, r)
	if src == nil {
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", src.name))
	w.Header().Set("Content-Length", strconv.FormatInt(src.size, 10))

	copied, copyErr := io.Copy(w, src)
	closeErr := src.Close()

	if copyErr != nil {
		log.Printf("[copy] transfer of %s/%s path=%s stopped after %d of %d bytes: %v", src.namespace, src.podName, src.filePath, copied, src.size, copyErr)
		errorlog.Record("copy", "error", "file download truncated for %s/%s path=%s after %d of %d bytes", src.namespace, src.podName, src.filePath, copied, src.size)
		// The declared Content-Length is already on the wire, so breaking the
		// connection is the only way left to stop the client from saving a
		// short file as if it were whole.
		panic(http.ErrAbortHandler)
	}
	if closeErr != nil {
		// Every declared byte arrived, so the body stands; the command still
		// reported trouble (an unreadable region, a file rewritten under it),
		// which belongs in the error log rather than in a torn response.
		log.Printf("[copy] reading %s/%s path=%s ended with an error after the file arrived in full: %v", src.namespace, src.podName, src.filePath, closeErr)
		errorlog.Record("copy", "warning", "reading %s/%s path=%s ended with an error after the file arrived in full: %v", src.namespace, src.podName, src.filePath, closeErr)
	}
}

// handlePodFileSave streams a file out of a pod straight onto the desktop app's
// disk. The download route sends the bytes to the webview, which then has to
// hand them back to be saved; for a large file that round trip is the part that
// fails, so the desktop app asks the backend to do the whole thing.
// POST /api/pods/{ns}/{name}/files/save?container=X&path=/some/file
func (s *Server) handlePodFileSave(w http.ResponseWriter, r *http.Request) {
	if s.saveFileStreamFunc == nil {
		s.writeError(w, http.StatusNotFound, "not available")
		return
	}

	src := s.openPodFileForRequest(w, r)
	if src == nil {
		return
	}
	defer src.Close()

	savedPath, err := s.saveFileStreamFunc(src.name, src)
	// Close before answering, so what the command reported is known — and
	// logged — before the caller is told the file is on disk.
	closeErr := src.Close()
	if err != nil {
		if errors.Is(err, ErrSaveCancelled) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		log.Printf("[copy] failed to save %s/%s path=%s: %v", src.namespace, src.podName, src.filePath, err)
		errorlog.Record("copy", "error", "file save failed for %s/%s path=%s: %v", src.namespace, src.podName, src.filePath, err)
		s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to save file: %v", err))
		return
	}
	if closeErr != nil {
		// The declared bytes all arrived and are on disk, so the save stands;
		// the command still reported trouble, which belongs in the error log.
		log.Printf("[copy] reading %s/%s path=%s ended with an error after the file arrived in full: %v", src.namespace, src.podName, src.filePath, closeErr)
		errorlog.Record("copy", "warning", "reading %s/%s path=%s ended with an error after the file arrived in full: %v", src.namespace, src.podName, src.filePath, closeErr)
	}
	s.writeJSON(w, map[string]string{"path": savedPath})
}

// podFileSource is one pod file ready to be read, however it was obtained.
type podFileSource struct {
	namespace string
	podName   string
	filePath  string
	name      string
	size      int64

	stream *podFileStream // nil when the cat fallback produced the bytes
	body   *bytes.Reader
}

func (p *podFileSource) Read(b []byte) (int, error) {
	if p.stream != nil {
		return p.stream.Read(b)
	}
	return p.body.Read(b)
}

func (p *podFileSource) Close() error {
	if p.stream != nil {
		return p.stream.Close()
	}
	return nil
}

// openPodFileForRequest validates the request and opens the named file, writing
// the error response itself and returning nil when it cannot.
func (s *Server) openPodFileForRequest(w http.ResponseWriter, r *http.Request) *podFileSource {
	if !s.requireConnected(w) {
		return nil
	}

	namespace := chi.URLParam(r, "namespace")
	podName := chi.URLParam(r, "name")
	container := r.URL.Query().Get("container")
	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		s.writeError(w, http.StatusBadRequest, "path parameter is required")
		return nil
	}

	filePath = path.Clean(filePath)
	if !isDownloadablePath(filePath) {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("Not a file: %s", filePath))
		return nil
	}

	client := s.getClientForRequest(r)
	config := s.getConfigForRequest(r)
	if client == nil || config == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return nil
	}

	src := &podFileSource{
		namespace: namespace,
		podName:   podName,
		filePath:  filePath,
		name:      path.Base(filePath),
	}

	stream, openErr := s.openPodFileWithTar(r.Context(), client, config, namespace, podName, container, filePath)
	if openErr != nil && openErr.commandMissing && !openErr.shellMissing {
		// The container has a shell but no tar; cat can still read the file.
		stream, openErr = s.openPodFileWithCat(r.Context(), client, config, namespace, podName, container, filePath)
	}
	if openErr == nil {
		src.stream = stream
		src.size = stream.size
		return src
	}

	switch {
	case openErr.commandMissing:
		// Nothing here can frame the file — read it as raw bytes and hope it is
		// small, which is all a container this bare is likely to hold.
		content, catErr := s.downloadWithCat(r, namespace, podName, container, filePath)
		if catErr != nil {
			if isCommandNotFound(catErr.Error()) {
				s.writeError(w, http.StatusInternalServerError, "Container lacks 'tar' and 'cat' commands. Cannot download files from distroless containers.")
				return nil
			}
			log.Printf("[copy] cat fallback failed for %s/%s path=%s: %v", namespace, podName, filePath, catErr)
			errorlog.Record("copy", "error", "reading %s/%s path=%s failed: %v", namespace, podName, filePath, catErr)
			s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to read file: %v", catErr))
			return nil
		}
		src.body = bytes.NewReader(content)
		src.size = int64(len(content))
		return src
	case openErr.notFound:
		s.writeError(w, http.StatusNotFound, openErr.message)
	case openErr.notAFile:
		s.writeError(w, http.StatusBadRequest, openErr.message)
	default:
		log.Printf("[copy] reading %s/%s path=%s failed: %v, stderr: %s", namespace, podName, filePath, openErr.err, openErr.stderr)
		errorlog.Record("copy", "error", "reading %s/%s path=%s failed: %v", namespace, podName, filePath, openErr.err)
		s.writeError(w, http.StatusInternalServerError, openErr.message)
	}
	return nil
}

// isDownloadablePath rejects the paths that cannot name a file inside the
// container, so they fail with a clear message instead of an odd tar error.
// Absolute is part of the contract: a relative name would reach `cat` as an
// option, and every path the file browser produces is absolute anyway.
func isDownloadablePath(cleaned string) bool {
	if !path.IsAbs(cleaned) {
		return false
	}
	base := path.Base(cleaned)
	return base != "/" && base != "." && base != ".."
}

// podFileDrainGuard keeps the remote shell alive on a stdin read after the file
// has been written out. Kubernetes tears the exec output stream down when the
// process exits and drops whatever the client has not drained yet, while still
// reporting success — over a slow link that silently costs the tail of a large
// file. Radar closes stdin only once it holds every byte, so the process
// outlives the transfer instead of racing it. The guard is armed only on
// success: a command that failed has nothing worth waiting for, and waiting
// would hang a caller that is still expecting the file.
const podFileDrainGuard = "; rc=$?; [ $rc -eq 0 ] && read _; exit $rc"

// podFileTarCommand archives a single file to stdout. "./" in front of the name
// keeps a file called e.g. "-C" from being read as a tar option, and -h resolves
// a symlink to the bytes it points at (the file browser offers Download on
// symlinks, and a link entry carries no content).
func podFileTarCommand(dir, base string) []string {
	script := "tar cfh - -C " + shellQuote(dir) + " " + shellQuote("./"+base) + podFileDrainGuard
	return []string{"/bin/sh", "-c", script}
}

// podFileNotRegularExit is how podFileCatCommand says the path exists but is
// not a regular file. Nothing else in these commands uses it: tar exits 0, 1
// or 2. A character device or a fifo has to be turned away before wc opens it,
// because wc on /dev/zero never reaches an end.
const podFileNotRegularExit = 3

// podFileCatCommand reads a file out of a container that has no tar. It prints
// the byte count on its own line first so the transfer can still tell a
// complete read from a truncated one, which is what the tar header does on the
// normal path. The type check is deliberately narrow — a path it cannot stat
// falls through to wc, whose own error says more than a guess would.
func podFileCatCommand(filePath string) []string {
	quoted := shellQuote(filePath)
	script := "if [ -e " + quoted + " ] && [ ! -f " + quoted + " ]; then echo 'not a regular file' >&2; exit " +
		strconv.Itoa(podFileNotRegularExit) + "; fi; " +
		"size=$(wc -c < " + quoted + ") || exit $?; printf '%s\n' \"$size\"; cat " + quoted + podFileDrainGuard
	return []string{"/bin/sh", "-c", script}
}

// podFileOpenError distinguishes the outcomes the download handlers act on.
type podFileOpenError struct {
	err            error
	stderr         string
	message        string
	commandMissing bool
	shellMissing   bool
	notFound       bool
	notAFile       bool
}

func (e *podFileOpenError) Error() string { return e.message }

// podFileStream carries one regular file's bytes out of a container. The exec
// runs for as long as the stream is open; Close releases it.
type podFileStream struct {
	size     int64
	written  int64
	payload  io.Reader
	stdout   *io.PipeReader
	stdin    *io.PipeWriter
	stderr   *bytes.Buffer
	done     <-chan error
	cancel   context.CancelFunc
	complete bool

	closeOnce sync.Once
	closeErr  error
}

// podFileCloseGrace bounds the wait for the remote command to finish after the
// file has arrived. Nothing is left to receive by then, so cutting a wedged
// connection loose costs nothing and keeps the handler from being pinned to it.
const podFileCloseGrace = 30 * time.Second

// podFileAbortGrace bounds the same wait when the file never arrived. A command
// that failed has already exited, so its status is normally there at once; the
// bound only covers a connection that has stopped answering.
const podFileAbortGrace = 5 * time.Second

// startPodFileExec runs cmd in the container and hands back a stream whose
// payload the caller frames. The command must keep stdin open per
// podFileDrainGuard.
func (s *Server) startPodFileExec(ctx context.Context, client kubernetes.Interface, config *rest.Config,
	namespace, podName, container string, cmd []string) (*podFileStream, error) {

	req := client.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   cmd,
			Stdin:     true,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	executor, err := rcpkg.NewExecutor(config, req.URL())
	if err != nil {
		return nil, err
	}

	execCtx, cancel := context.WithCancel(ctx)
	stdoutR, stdoutW := io.Pipe()
	stdinR, stdinW := io.Pipe()
	stderr := &bytes.Buffer{}
	done := make(chan error, 1)

	go func() {
		streamErr := executor.StreamWithContext(execCtx, remotecommand.StreamOptions{
			Stdin:  stdinR,
			Stdout: stdoutW,
			Stderr: stderr,
		})
		_ = stdoutW.CloseWithError(streamErr)
		_ = stdinR.Close()
		done <- streamErr
	}()

	return &podFileStream{
		size:   -1,
		stdout: stdoutR,
		stdin:  stdinW,
		stderr: stderr,
		done:   done,
		cancel: cancel,
	}, nil
}

// openPodFileWithTar starts the transfer and reads the tar header, so the
// caller knows the file's size before it commits to a response.
func (s *Server) openPodFileWithTar(ctx context.Context, client kubernetes.Interface, config *rest.Config,
	namespace, podName, container, filePath string) (*podFileStream, *podFileOpenError) {

	dir, base := path.Dir(filePath), path.Base(filePath)
	stream, err := s.startPodFileExec(ctx, client, config, namespace, podName, container, podFileTarCommand(dir, base))
	if err != nil {
		log.Printf("[copy] Failed to create executor for download %s/%s: %v", namespace, podName, err)
		return nil, &podFileOpenError{err: err, message: fmt.Sprintf("Failed to create executor: %v", err)}
	}

	tr := tar.NewReader(stream.stdout)
	header, headerErr := tr.Next()
	if headerErr != nil {
		streamErr := stream.Close()
		return nil, classifyPodFileOpenError(filePath, headerErr, streamErr, stream.stderr.String())
	}
	if header.Typeflag != tar.TypeReg {
		_ = stream.Close()
		return nil, &podFileOpenError{
			err:      fmt.Errorf("tar entry %q is not a regular file (type %q)", header.Name, string(header.Typeflag)),
			message:  fmt.Sprintf("Not a regular file: %s", filePath),
			notAFile: true,
		}
	}

	stream.size = header.Size
	stream.payload = tr
	return stream, nil
}

// openPodFileWithCat is the fallback for a container that has a shell but no
// tar. It reads the size the command announced, then frames the rest of stdout
// as the file.
func (s *Server) openPodFileWithCat(ctx context.Context, client kubernetes.Interface, config *rest.Config,
	namespace, podName, container, filePath string) (*podFileStream, *podFileOpenError) {

	stream, err := s.startPodFileExec(ctx, client, config, namespace, podName, container, podFileCatCommand(filePath))
	if err != nil {
		log.Printf("[copy] Failed to create executor for download %s/%s: %v", namespace, podName, err)
		return nil, &podFileOpenError{err: err, message: fmt.Sprintf("Failed to create executor: %v", err)}
	}

	buffered := bufio.NewReader(stream.stdout)
	line, readErr := buffered.ReadString('\n')
	if readErr != nil {
		streamErr := stream.Close()
		return nil, classifyPodFileOpenError(filePath, readErr, streamErr, stream.stderr.String())
	}
	size, parseErr := strconv.ParseInt(strings.TrimSpace(line), 10, 64)
	if parseErr != nil || size < 0 {
		_ = stream.Close()
		return nil, &podFileOpenError{
			err:     fmt.Errorf("unreadable size %q from wc: %v", strings.TrimSpace(line), parseErr),
			message: fmt.Sprintf("Failed to read file: %s", filePath),
		}
	}

	stream.size = size
	stream.payload = io.LimitReader(buffered, size)
	return stream, nil
}

// classifyPodFileOpenError turns "the file never arrived" into the reason the
// caller needs: a missing command to fall back from, a missing file, or a
// genuine failure.
func classifyPodFileOpenError(filePath string, readErr, streamErr error, stderr string) *podFileOpenError {
	combined := strings.TrimSpace(fmt.Sprintf("%v %s", streamErr, stderr))
	if isCommandMissing(streamErr, stderr) {
		return &podFileOpenError{
			err:            streamErr,
			stderr:         stderr,
			commandMissing: true,
			shellMissing:   isShellMissing(combined),
			message:        combined,
		}
	}
	var exitErr k8sexec.CodeExitError
	if errors.As(streamErr, &exitErr) && exitErr.Code == podFileNotRegularExit {
		return &podFileOpenError{err: streamErr, stderr: stderr, notAFile: true,
			message: fmt.Sprintf("Not a regular file: %s", filePath)}
	}
	lowered := strings.ToLower(combined)
	// Checked before the missing-file shapes below: a shell says "can't open"
	// for both, and answering "not found" for a file the container simply may
	// not read sends the reader looking for the wrong thing.
	if strings.Contains(lowered, "permission denied") || strings.Contains(lowered, "operation not permitted") {
		return &podFileOpenError{err: streamErr, stderr: stderr,
			message: fmt.Sprintf("Permission denied: the container user cannot read %s", filePath)}
	}
	// Shells and tars disagree on the wording — "No such file or directory",
	// "can't open ...: no such file" — so match on the shared shape, folded.
	if strings.Contains(lowered, "no such file") || strings.Contains(lowered, "not found") ||
		(streamErr == nil && readErr == io.EOF) {
		return &podFileOpenError{err: streamErr, stderr: stderr, notFound: true,
			message: fmt.Sprintf("File not found: %s", filePath)}
	}
	if streamErr == nil {
		streamErr = readErr
	}
	if errors.Is(streamErr, context.Canceled) {
		// Nothing came back to classify. Naming the cancellation would only
		// hand the reader an internal detail they cannot act on.
		return &podFileOpenError{err: streamErr, stderr: stderr,
			message: fmt.Sprintf("The transfer stopped before %s could be read", filePath)}
	}
	return &podFileOpenError{
		err:     streamErr,
		stderr:  stderr,
		message: fmt.Sprintf("Failed to download file: %v", streamErr),
	}
}

// Read yields the file's bytes and refuses to end early. A stream that stops
// short of the size the container announced is the truncation this transfer
// exists to catch, so it surfaces as an error instead of a complete-looking
// file.
func (p *podFileStream) Read(b []byte) (int, error) {
	n, err := p.payload.Read(b)
	p.written += int64(n)
	if err == io.EOF {
		if p.written != p.size {
			return n, fmt.Errorf("received %d of %d bytes: %w", p.written, p.size, io.ErrUnexpectedEOF)
		}
		p.complete = true
	}
	return n, err
}

// Close ends the exec and reports how the remote command finished. After a
// complete read it releases the drain guard and drains what is left, which is
// what lets the command finish writing and exit; otherwise it tears the
// connection down without pulling the rest of the file across.
func (p *podFileStream) Close() error {
	p.closeOnce.Do(func() {
		if p.complete {
			_ = p.stdin.Close()
			grace := time.AfterFunc(podFileCloseGrace, p.cancel)
			defer grace.Stop()
			_, _ = io.Copy(io.Discard, p.stdout)
		} else {
			// Closing the pipes unblocks the exec goroutine so the command can
			// still say why it failed. Cancelling ahead of it races that away
			// and leaves only "context canceled", losing both the exit status
			// and whatever the command wrote to stderr — which is the whole
			// basis for telling a missing file from an unreadable one.
			_ = p.stdin.CloseWithError(errPodFileAborted)
			_ = p.stdout.CloseWithError(errPodFileAborted)
			grace := time.AfterFunc(podFileAbortGrace, p.cancel)
			defer grace.Stop()
		}
		p.closeErr = <-p.done
		p.cancel()
		if p.complete && errors.Is(p.closeErr, context.Canceled) {
			// A client that has its last byte disconnects, which cancels the
			// request context while the exec is still winding down. The file
			// arrived; that is not something to report.
			p.closeErr = nil
		}
	})
	return p.closeErr
}

var errPodFileAborted = errors.New("pod file transfer aborted")

// isCommandMissing reports whether the container lacks the command we asked
// for. Behind /bin/sh a missing binary is exit code 127 rather than the
// runtime's "executable file not found", and shells word it differently
// ("tar: not found" vs "command not found"), so the code is the reliable part.
func isCommandMissing(streamErr error, stderr string) bool {
	var exitErr k8sexec.CodeExitError
	if errors.As(streamErr, &exitErr) && exitErr.Code == 127 {
		return true
	}
	if streamErr == nil {
		return false
	}
	combined := streamErr.Error() + " " + stderr
	return isCommandNotFound(combined) || isShellMissing(combined)
}

// downloadWithCat is a fallback for containers without tar. It has no way to
// learn the file's size up front, so it can neither hold the remote process
// open until the last byte arrives nor tell a truncated read from a complete
// one — treat it as best-effort for the small files these containers hold,
// not as an equal path for large ones.
func (s *Server) downloadWithCat(r *http.Request, namespace, podName, container, filePath string) ([]byte, error) {
	client := s.getClientForRequest(r)
	config := s.getConfigForRequest(r)
	if client == nil || config == nil {
		return nil, fmt.Errorf("cluster client not available")
	}

	cmd := []string{"cat", filePath}

	req := client.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   cmd,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	exec, err := rcpkg.NewExecutor(config, req.URL())
	if err != nil {
		return nil, err
	}

	var stdout, stderr bytes.Buffer
	err = exec.StreamWithContext(r.Context(), remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		return nil, fmt.Errorf("%v: %s", err, stderr.String())
	}

	return stdout.Bytes(), nil
}

// parseFindOutput parses the output of find -printf '%y\t%s\t%T@\t%m\t%p\n'
func parseFindOutput(output, dirPath string) ([]*images.FileNode, int) {
	var nodes []*images.FileNode
	totalFiles := 0

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, "\t", 5)
		if len(parts) < 5 {
			continue
		}

		fileType := parts[0]
		sizeStr := parts[1]
		perms := parts[3]
		filePath := parts[4]

		// Skip the directory itself
		if filePath == dirPath || filePath == "." {
			continue
		}

		size, _ := strconv.ParseInt(sizeStr, 10, 64)

		var nodeType string
		switch fileType {
		case "d":
			nodeType = "dir"
		case "l":
			nodeType = "symlink"
		default:
			nodeType = "file"
		}

		node := &images.FileNode{
			Name:        path.Base(filePath),
			Path:        filePath,
			Type:        nodeType,
			Size:        size,
			Permissions: formatOctalPerms(perms),
		}

		nodes = append(nodes, node)
		totalFiles++
	}

	sortFileNodes(nodes)
	return nodes, totalFiles
}

// parseLSOutput parses `ls -la` output as a fallback
func parseLSOutput(output, dirPath string) []*images.FileNode {
	var nodes []*images.FileNode

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "total ") {
			continue
		}

		// ls -la output: permissions links owner group size month day time name [-> target]
		fields := strings.Fields(line)
		if len(fields) < 9 {
			continue
		}

		perms := fields[0]
		sizeStr := fields[4]
		name := fields[8]

		// Skip . and ..
		if name == "." || name == ".." {
			continue
		}

		size, _ := strconv.ParseInt(sizeStr, 10, 64)

		var nodeType string
		var linkTarget string
		switch {
		case perms[0] == 'd':
			nodeType = "dir"
		case perms[0] == 'l':
			nodeType = "symlink"
			// Extract link target (after "->")
			for i, f := range fields {
				if f == "->" && i+1 < len(fields) {
					linkTarget = strings.Join(fields[i+1:], " ")
					break
				}
			}
		default:
			nodeType = "file"
		}

		nodePath := path.Join(dirPath, name)

		node := &images.FileNode{
			Name:        name,
			Path:        nodePath,
			Type:        nodeType,
			Size:        size,
			Permissions: perms,
			LinkTarget:  linkTarget,
		}

		nodes = append(nodes, node)
	}

	sortFileNodes(nodes)
	return nodes
}

// buildRootNode wraps file nodes in a root directory node
func buildRootNode(dirPath string, children []*images.FileNode) *images.FileNode {
	return &images.FileNode{
		Name:     path.Base(dirPath),
		Path:     dirPath,
		Type:     "dir",
		Children: children,
	}
}

// sortFileNodes sorts directories first, then files, alphabetically
func sortFileNodes(nodes []*images.FileNode) {
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Type == "dir" && nodes[j].Type != "dir" {
			return true
		}
		if nodes[i].Type != "dir" && nodes[j].Type == "dir" {
			return false
		}
		return nodes[i].Name < nodes[j].Name
	})
}

// formatOctalPerms converts octal permission string (e.g., "755") to rwx format
func formatOctalPerms(octal string) string {
	if len(octal) < 3 {
		return octal
	}

	// Take last 3 digits
	if len(octal) > 3 {
		octal = octal[len(octal)-3:]
	}

	rwx := func(digit byte) string {
		n := digit - '0'
		r := "-"
		w := "-"
		x := "-"
		if n&4 != 0 {
			r = "r"
		}
		if n&2 != 0 {
			w = "w"
		}
		if n&1 != 0 {
			x = "x"
		}
		return r + w + x
	}

	return rwx(octal[0]) + rwx(octal[1]) + rwx(octal[2])
}

// classifyExecError analyzes errors from both find and ls exec attempts and returns
// a user-friendly message that identifies the actual problem rather than always
// blaming missing commands.
func classifyExecError(findErr, lsErr string) string {
	combined := strings.ToLower(findErr + " " + lsErr)

	// Check for permission denied
	if strings.Contains(combined, "permission denied") || strings.Contains(combined, "operation not permitted") {
		return "Permission denied: the container user lacks access to this directory. Try a different path or container."
	}

	if isShellMissing(combined) {
		return "Container has no shell (/bin/sh). This is likely a distroless or scratch-based container that cannot be browsed."
	}

	// Check for both commands genuinely missing
	findMissing := isCommandNotFound(findErr)
	lsMissing := isCommandNotFound(lsErr)
	if findMissing && lsMissing {
		return "Container lacks 'find' and 'ls' commands. This container may be distroless or minimal."
	}

	// Check for no such file or directory (path doesn't exist)
	if strings.Contains(combined, "no such file or directory") && !findMissing && !lsMissing {
		return "Directory not found. The path may not exist in this container."
	}

	// Check for connection/network issues
	if strings.Contains(combined, "error dialing backend") || strings.Contains(combined, "connection refused") ||
		strings.Contains(combined, "transport closed") || strings.Contains(combined, "transport error") || strings.Contains(combined, "stream error") ||
		strings.Contains(combined, "websocket") || strings.Contains(combined, "upgrade") {
		return fmt.Sprintf("Failed to exec into container (connection error): %s", lsErr)
	}

	// Check for context deadline exceeded
	if strings.Contains(combined, "context deadline exceeded") || strings.Contains(combined, "context canceled") {
		return "Exec timed out. The container may be unresponsive or under heavy load."
	}

	// Default: include the actual ls error so users can diagnose
	return fmt.Sprintf("Failed to list files: %s", lsErr)
}

// shellQuote wraps a string in single quotes for safe use in sh -c commands.
// Single quotes inside the string are escaped by ending the quote, adding an
// escaped single quote, and re-opening the quote.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// isShellMissing detects a container without /bin/sh. Runtimes word it
// differently — "executable file not found" from one, `exec: "/bin/sh": stat
// /bin/sh: no such file or directory` from another — so both forms count.
func isShellMissing(errMsg string) bool {
	lower := strings.ToLower(errMsg)
	if strings.Contains(lower, "executable file not found") && (strings.Contains(lower, "sh") || strings.Contains(lower, "shell")) {
		return true
	}
	return strings.Contains(lower, "/bin/sh") && strings.Contains(lower, "no such file or directory")
}

// isCommandNotFound detects errors indicating a command is not available in the container
func isCommandNotFound(errMsg string) bool {
	lower := strings.ToLower(errMsg)
	patterns := []string{
		"executable file not found",
		"command not found",
		"not found in $path",
	}
	for _, p := range patterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}
