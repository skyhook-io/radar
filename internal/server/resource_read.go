package server

import (
	"errors"
	"github.com/skyhook-io/radar/pkg/k8score"
	"net/http"
)

func (s *Server) writeResourceReadError(w http.ResponseWriter, err error) bool {
	var readErr *k8score.ResourceReadError
	if !errors.As(err, &readErr) {
		return false
	}
	status := http.StatusServiceUnavailable
	if readErr.Code == "kind_not_served" {
		status = http.StatusNotFound
	}
	s.writeErrorCode(w, status, readErr.Code, readErr.Error())
	return true
}
