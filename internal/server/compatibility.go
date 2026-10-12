package server

import "net/http"

func (s *Server) handleCompatibility(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	jsonResponse(w, http.StatusOK, s.store.ActiveCompatibility())
}
