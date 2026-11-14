package api

import "chat-agent/internal/pluginapi"

// WithRoleWrite implements pluginapi.WriteGate for plugins that mutate
// role-scoped state.
//
// The server lock stays held across write, which is the whole point: the refusals
// below are only meaningful if no run can start between the check and the commit.
// A plugin therefore passes the read-modify-write in, not the result of a separate
// "is it safe" question.
func (s *Server) WithRoleWrite(role, sessionID string, write func(pluginapi.WriteScope) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.runs[sessionID] == nil {
		return pluginapi.ErrWriteSessionEnded
	}
	for id := range s.runs {
		if id != sessionID && s.sessions[id] != nil && s.sessions[id].RoleID() == role {
			return pluginapi.ErrWriteRoleBusy
		}
	}
	// Measured under the same lock as the write, so the barrier a tool publishes
	// cannot be stale by the time it lands.
	cuts, err := s.roleContextCuts(role, sessionID)
	if err != nil {
		return pluginapi.ErrWriteContextUnavailable
	}
	return write(pluginapi.WriteScope{Cuts: cuts})
}
