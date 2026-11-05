package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/servicehttp"
)

func (s *Server) handleCurlService(w http.ResponseWriter, r *http.Request) {
	var input servicehttp.Request
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		s.writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	result, err := servicehttp.Do(r.Context(), input)
	if err != nil {
		var failure *servicehttp.Error
		if errors.As(err, &failure) {
			s.writeError(w, failure.Status, failure.Message)
		} else {
			s.writeError(w, http.StatusBadGateway, err.Error())
		}
		return
	}
	auth.AuditLog(r, input.Namespace, input.Name)
	s.writeJSON(w, result)
}
