package lang

import "strings"

// Failure is machine-readable failure metadata. Codes are stable categories;
// message text is for people. Line is one-based in the expanded source.
type Failure struct {
	Status  int
	Code    string
	Message string
	Key     string
	Source  string
	Line    int
}

func (m *machine) noteFailure(status int, message string) {
	source := m.provSrc
	if source == "" {
		source = "script"
	}
	msg := strings.TrimSpace(message)
	m.failure = &Failure{Status: status, Code: failureCode(status, msg), Message: safeText(msg), Key: failKey(msg), Source: source, Line: m.provLine()}
}

func failureCode(status int, msg string) string {
	line := firstLine(msg)
	switch {
	case line == "timeout":
		return "timeout"
	case strings.HasPrefix(line, "await: mutation"):
		return "await_mutation"
	case strings.HasPrefix(line, "declined "):
		return "declined"
	case strings.HasPrefix(line, "denied "):
		return "denied"
	case strings.HasPrefix(line, "unknown "):
		return "unknown_command"
	case strings.HasPrefix(line, "usage "):
		return "invalid_argument"
	case strings.HasPrefix(line, "refused "):
		return "refused"
	case strings.HasPrefix(line, "parent "):
		return "parent_error"
	case strings.HasPrefix(line, "type "):
		return "type_error"
	case strings.HasPrefix(line, "parse:"):
		return "parse_error"
	case line == "loop max" || line == "verb max" || line == "fn depth":
		return "limit_exceeded"
	case strings.Contains(line, "secret"):
		return "secret_refused"
	case line == "hangup":
		return "hangup"
	}
	switch status {
	case exitDenied:
		return "denied"
	case exitUnknown:
		return "unknown_command"
	case exitType:
		return "runtime_error"
	}
	return "command_failed"
}
