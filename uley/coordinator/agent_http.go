package coordinator

import (
	"net/http"
	"strconv"
)

// The "What Улей remembers" panel: people see and control everything the
// agent keeps about them and their projects.

func (s *Server) registerAgentAPI() {
	p := "/api/v1"
	s.mux.HandleFunc("GET "+p+"/agent", s.userAuth(s.handleAgentState))
	s.mux.HandleFunc("POST "+p+"/memories", s.userAuth(s.handleAddMemory))
	s.mux.HandleFunc("DELETE "+p+"/memories/{id}", s.userAuth(s.handleDeleteMemory))
	s.mux.HandleFunc("DELETE "+p+"/skills/{id}", s.userAuth(s.handleDeleteSkill))
	s.mux.HandleFunc("DELETE "+p+"/schedules/{id}", s.userAuth(s.handleDeleteSchedule))
}

// scopeFor is the person's own scope, or a project's if they are a member.
func (s *Server) scopeFor(r *http.Request) (scope, error) {
	u := currentUser(r)
	if pid := r.URL.Query().Get("project"); pid != "" {
		if !s.store.isMember(pid, u.ID) {
			return scope{}, ErrForbidden
		}
		return scope{projectID: pid}, nil
	}
	return scope{userID: u.ID}, nil
}

func (s *Server) handleAgentState(w http.ResponseWriter, r *http.Request) {
	sc, err := s.scopeFor(r)
	if err != nil {
		userErr(w, err)
		return
	}
	mem, err := s.store.Memories(sc, maxMemories)
	if err != nil {
		userErr(w, err)
		return
	}
	skills, err := s.store.Skills(sc)
	if err != nil {
		userErr(w, err)
		return
	}
	out := map[string]any{"memories": mem, "skills": skills}
	if sc.projectID == "" {
		sched, err := s.store.Schedules(currentUser(r).ID, s.loc())
		if err != nil {
			userErr(w, err)
			return
		}
		out["schedules"] = sched
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAddMemory(w http.ResponseWriter, r *http.Request) {
	sc, err := s.scopeFor(r)
	if err != nil {
		userErr(w, err)
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	m, err := s.store.Remember(sc, req.Text)
	if err != nil {
		userErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func pathID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id
}

func (s *Server) handleDeleteMemory(w http.ResponseWriter, r *http.Request) {
	sc, err := s.scopeFor(r)
	if err == nil {
		err = s.store.Forget(sc, pathID(r))
	}
	if err != nil {
		userErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteSkill(w http.ResponseWriter, r *http.Request) {
	sc, err := s.scopeFor(r)
	if err == nil {
		err = s.store.DeleteSkill(sc, pathID(r))
	}
	if err != nil {
		userErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	if err := s.store.CancelSchedule(currentUser(r).ID, pathID(r)); err != nil {
		userErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
