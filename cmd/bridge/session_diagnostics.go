package main

// Diagnostic hashes describe the existing matching rules without retaining prompts or keys.
type sessionDiagnostic struct {
	parts  []string
	record *requestRecord
}
type retiredSession struct {
	scope   string
	history []string
	parts   []string
	reason  string
}

func (p *sessionPool) rememberRetired(s *liveSession, reason string) {
	p.retired = append(p.retired, retiredSession{s.scope, s.history, s.parts, reason})
	if len(p.retired) > recentRequestLimit {
		p.retired = p.retired[len(p.retired)-recentRequestLimit:]
	}
}
func (p *sessionPool) explainMiss(scope string, hashes []string, d sessionDiagnostic) (string, []string) {
	reason := "no_cached_session"
	bestDifference := []string(nil)
	labels := []string{"caller", "endpoint", "account", "model", "instructions", "tools", "tool_names", "tool_policy"}
	sameScope, busy := false, false
	for _, s := range p.entries {
		if s.scope == scope {
			sameScope = true
			if s.busy {
				busy = true
				continue
			}
			if historyPrefix(s.history, hashes) {
				return "continuation_mismatch", nil
			}
			if len(d.parts) > 4 && len(s.parts) > 4 && d.parts[4] != s.parts[4] {
				if reason != "history_mismatch" {
					reason = "instructions_mismatch"
				}
			} else {
				reason = "history_mismatch"
			}
			continue
		}
		if len(d.parts) != len(labels) || len(s.parts) != len(labels) || d.parts[0] != s.parts[0] {
			continue
		}
		different := []string{}
		for i := 1; i < len(labels); i++ {
			if d.parts[i] != s.parts[i] {
				different = append(different, labels[i])
			}
		}
		if len(different) > 0 && (bestDifference == nil || len(different) < len(bestDifference)) {
			bestDifference = different
		}
	}
	// A removed session is attributed only when this request extends its exact history and scope.
	for i := len(p.retired) - 1; i >= 0; i-- {
		old := p.retired[i]
		compatible := old.scope == scope || (len(d.parts) > 0 && digest(old.parts) == digest(d.parts))
		if compatible && historyPrefix(old.history, hashes) {
			return old.reason, nil
		}
	}
	if sameScope {
		if busy {
			return "matching_scope_busy", nil
		}
		return reason, nil
	}
	if len(bestDifference) > 0 {
		return "scope_mismatch", bestDifference
	}
	return reason, nil
}
