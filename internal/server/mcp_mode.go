package server

func (s *Server) mcpEnabled() bool {
	return s.mcpHandler != nil || s.mcpReadOnlyHandler != nil || s.mcpInvestigationHandler != nil
}

func (s *Server) mcpReadOnly() bool {
	return s.mcpEnabled() && s.mcpHandler == nil
}
