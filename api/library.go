package api

import "net/http"

// handleLibrary lists the automation templates an account may install.
//
//	GET /automations/library
func (s *Server) handleLibrary(w http.ResponseWriter, r *http.Request) {
	list, err := s.opts.Library.Templates(r.Context())
	if err != nil {
		s.opts.Logger.Error("could not list the automation library", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not list the automation library")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": list})
}
