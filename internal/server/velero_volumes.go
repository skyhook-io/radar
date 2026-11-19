package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type veleroVolumeDetails struct {
	SnapshotHandle            string `json:"snapshotHandle,omitempty"`
	Size                      *int64 `json:"size,omitempty"`
	IncrementalSize           *int64 `json:"incrementalSize,omitempty"`
	Driver                    string `json:"driver,omitempty"`
	VSCName                   string `json:"vscName,omitempty"`
	OperationID               string `json:"operationID,omitempty"`
	ReadyToUse                *bool  `json:"readyToUse,omitempty"`
	VolumeGroupSnapshotHandle string `json:"volumeGroupSnapshotHandle,omitempty"`
	DataMover                 string `json:"dataMover,omitempty"`
	UploaderType              string `json:"uploaderType,omitempty"`
	RetainedSnapshot          string `json:"retainedSnapshot,omitempty"`
	VolumeType                string `json:"volumeType,omitempty"`
	VolumeAZ                  string `json:"volumeAZ,omitempty"`
	IOPS                      string `json:"iops,omitempty"`
	VolumeName                string `json:"volumeName,omitempty"`
	PodName                   string `json:"podName,omitempty"`
	PodNamespace              string `json:"podNamespace,omitempty"`
	NodeName                  string `json:"nodeName,omitempty"`
	Phase                     string `json:"phase,omitempty"`
}

type veleroVolumeRecord struct {
	PVCName                  string               `json:"pvcName,omitempty"`
	PVCNamespace             string               `json:"pvcNamespace,omitempty"`
	PVName                   string               `json:"pvName,omitempty"`
	Method                   string               `json:"method,omitempty"`
	SnapshotDataMoved        bool                 `json:"snapshotDataMoved"`
	PreserveLocalSnapshot    *bool                `json:"preserveLocalSnapshot,omitempty"`
	Result                   string               `json:"result,omitempty"`
	Skipped                  *bool                `json:"skipped,omitempty"`
	SkippedReason            string               `json:"skippedReason,omitempty"`
	Started                  string               `json:"started,omitempty"`
	Completed                string               `json:"completed,omitempty"`
	CSISnapshotInfo          *veleroVolumeDetails `json:"csiSnapshotInfo,omitempty"`
	SnapshotDataMovementInfo *veleroVolumeDetails `json:"snapshotDataMovementInfo,omitempty"`
	NativeSnapshotInfo       *veleroVolumeDetails `json:"nativeSnapshotInfo,omitempty"`
	PodVolumeInfo            *veleroVolumeDetails `json:"podVolumeInfo,omitempty"`
}

type veleroRunVolumes struct {
	Volumes   []veleroVolumeRecord `json:"volumes"`
	Total     int                  `json:"total"`
	Truncated bool                 `json:"truncated"`
	// FinishedAtRead: the run had finished when Radar read it, just before
	// asking for the file. Velero rewrites a backup's record when its plugin
	// operations finish, before the phase leaves Finalizing, so a record read
	// earlier may be provisional whatever the run's phase is by now.
	FinishedAtRead bool `json:"finishedAtRead"`
}

// POST uses the same impersonated DownloadRequest, run-read gate and namespace
// pin as Contents. RestoreVolumeInfo describes intent before operations finish.
func (s *Server) handleVeleroRunVolumes(w http.ResponseWriter, r *http.Request) {
	restore := strings.EqualFold(chi.URLParam(r, "kind"), "restores")
	suffix := "VolumeInfos"
	if restore {
		suffix = "VolumeInfo"
	}
	run, ok := s.openVeleroRun(w, r, "volume record")
	if !ok {
		return
	}
	phase, _, _ := unstructured.NestedString(run.object.Object, "status", "phase")
	result, failure := s.downloadVeleroRunArtifact(r.Context(), run, "volume record", suffix, maxVeleroCompressedBytes, maxVeleroDecodedBytes, func(reader io.Reader) (any, error) {
		return decodeVeleroVolumes(reader, restore)
	})
	if failure != nil {
		s.writeError(w, failure.status, failure.message)
		return
	}
	volumes := result.(veleroRunVolumes)
	volumes.FinishedAtRead = veleroRunFinished(phase)
	s.writeJSON(w, volumes)
}

// A run in any other phase may still change what Velero records for it.
func veleroRunFinished(phase string) bool {
	switch phase {
	case "Completed", "PartiallyFailed", "Failed", "FailedValidation", "Deleting":
		return true
	}
	return false
}

func decodeVeleroVolumes(reader io.Reader, restore bool) (any, error) {
	out := veleroRunVolumes{Volumes: []veleroVolumeRecord{}}
	dec := json.NewDecoder(reader)
	start, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if start != json.Delim('[') {
		return nil, errNotAResultsFile
	}
	for dec.More() {
		raw := &struct {
			veleroVolumeRecord
			BackupMethod        string               `json:"backupMethod"`
			RestoreMethod       string               `json:"restoreMethod"`
			StartTimestamp      string               `json:"startTimestamp"`
			CompletionTimestamp string               `json:"completionTimestamp"`
			PVBInfo             *veleroVolumeDetails `json:"pvbInfo"`
			PVRInfo             *veleroVolumeDetails `json:"pvrInfo"`
		}{}
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		if raw == nil {
			return nil, errNotAResultsFile
		}
		row := raw.veleroVolumeRecord
		row.Method, row.Started, row.Completed, row.PodVolumeInfo = raw.BackupMethod, raw.StartTimestamp, raw.CompletionTimestamp, raw.PVBInfo
		if restore {
			row.Method, row.PodVolumeInfo = raw.RestoreMethod, raw.PVRInfo
			row.Result, row.Skipped, row.SkippedReason, row.PreserveLocalSnapshot = "", nil, "", nil
			row.Started, row.Completed = "", ""
		}
		out.Total++
		if len(out.Volumes) < maxVeleroArtifactRows {
			out.Volumes = append(out.Volumes, row)
		} else {
			out.Truncated = true
		}
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			err = errNotAResultsFile
		}
		return nil, err
	}
	return out, nil
}
