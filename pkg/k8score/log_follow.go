package k8score

import (
	"errors"
	"strings"
)

var (
	errLogWatcherFiles       = errors.New("Log streaming stopped: the cluster node could not create a log watcher (too many open files).")
	errLogWatcherSystemFiles = errors.New("Log streaming stopped: the cluster node could not create a log watcher (too many open files in system).")
	errLogWatcherLimit       = errors.New("Log streaming stopped: the cluster node ran out of filesystem watcher resources.")
)

// LogFollowError recognises node notices in a timestamped log follow. Callers
// must request timestamps: otherwise an application's own error is ambiguous.
// Snapshot readers must use IsLogsUnavailableNotice instead.
func LogFollowError(line string) error {
	if IsLogsUnavailableNotice(line) {
		return ErrLogsUnavailable
	}
	line = strings.TrimSpace(line)
	switch line {
	case "failed to create fsnotify watcher: too many open files":
		return errLogWatcherFiles
	case "failed to create fsnotify watcher: too many open files in system":
		return errLogWatcherSystemFiles
	}
	if !strings.Contains(line, "\n") && strings.HasPrefix(line, `failed to watch directory "`) && strings.HasSuffix(line, `": no space left on device`) {
		return errLogWatcherLimit
	}
	return nil
}
