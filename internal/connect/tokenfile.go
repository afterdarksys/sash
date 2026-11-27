package connect

import "path/filepath"

// tokenFileMax is the largest token file read, as the control plane's
// CLI has it.
const tokenFileMax = 4096

// tokenState is how reading the token file ended. No state carries the
// file's content or its path.
type tokenState int

const (
	tokenOK tokenState = iota
	// tokenNone: no absolute path was given, or no file is there.
	tokenNone
	// tokenRejected: a file is there and is not one sash reads.
	tokenRejected
)

// readToken reads the login token from the file SYSTEMAPI_TOKEN_FILE
// names, with the checks the control plane's CLI applies and one more.
//
// Threats: a token file another user can read or swap, a symlink or a
// FIFO planted at its path, and a file of any size. The file is opened
// read-only without following a symlink at its last component and without
// blocking, and must be a regular file of at most 4096 bytes, with no
// group or other permission bit, owned by the effective user. Its content,
// trimmed, must be one token: 16 to 512 bytes of printable ASCII with no
// space, quote, backslash or <. Any failure is one fixed refusal that
// names neither the path nor the content. It does not protect against the
// same user, or root, reading or replacing the file, nor against a
// symlinked directory above the file.
func readToken(path string) (string, tokenState) {
	if path == "" || !filepath.IsAbs(path) {
		return "", tokenNone
	}
	return openToken(path)
}

// tokenText is the token in the file's bytes, or false.
func tokenText(b []byte) (string, bool) {
	start, end := 0, len(b)
	for start < end && asciiSpace(b[start]) {
		start++
	}
	for end > start && asciiSpace(b[end-1]) {
		end--
	}
	t := b[start:end]
	if len(t) < 16 || len(t) > 512 {
		return "", false
	}
	for _, c := range t {
		if c < 0x21 || c > 0x7e || c == '"' || c == '\\' || c == '<' {
			return "", false
		}
	}
	return string(t), true
}

func asciiSpace(c byte) bool {
	return c == ' ' || c == '\n' || c == '\r' || c == '\t' || c == '\v' || c == '\f'
}
