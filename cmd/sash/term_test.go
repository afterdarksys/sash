package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// is_tty() is true only where a person can be asked: the script is not on
// stdin, and stdin and stderr are both terminals.
func TestBinaryIsTTY(t *testing.T) {
	bin := ptyBin(t)
	const src = "echo tty [ is_tty() ]\n"
	file := filepath.Join(t.TempDir(), "s.sash")
	if err := os.WriteFile(file, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		run  ptyRun
		want string
	}{
		{"-c at a terminal", ptyRun{Argv: []string{bin, "-c", src}}, "tty true"},
		{"a file at a terminal", ptyRun{Argv: []string{bin, file}}, "tty true"},
		{"stdout piped, still a terminal to ask", ptyRun{Argv: []string{bin, "-c", src}, Stdout: "pipe"}, "tty true"},
		{"stdin is a pipe", ptyRun{Argv: []string{bin, "-c", src}, Stdin: "pipe"}, "tty false"},
		{"stdin is the script", ptyRun{Argv: []string{bin}, Stdin: "pipe", Input: src}, "tty false"},
		{"stderr is not a terminal", ptyRun{Argv: []string{bin, file}, Stderr: "pipe"}, "tty false"},
	} {
		rep := runPTY(t, c.run)
		if got := rep.TTY + rep.Stdout; rep.Code != 0 || !strings.Contains(got, c.want) {
			t.Errorf("%s: code %d, want %q, got tty %q stdout %q stderr %q", c.name, rep.Code, c.want, rep.TTY, rep.Stdout, rep.Stderr)
		}
	}
}

// term asks at a real terminal: the question and the echo are shown, the
// answer is a value, and the terminal mode is put back after each question.
func TestBinaryTermAtATerminal(t *testing.T) {
	bin := ptyBin(t)
	rep := runPTY(t, ptyRun{
		Argv: []string{bin, "-c", "let a = (term prompt \"unit?\")\necho got $a\nterm confirm \"again?\" && echo yes\n"},
		Steps: []ptyStep{
			{Expect: "unit? "}, {Send: "ngx\x7finx\r"},
			{Expect: "again? [y + Enter / N] "}, {Sleep: 0.4}, {Send: "y"}, {Sleep: 0.1}, {Send: "\r"},
		},
	})
	// A script's output is written when it ends, after both questions.
	if rep.Code != 0 || !rep.Restored || !strings.HasSuffix(rep.TTY, "got nginx\r\nyes\r\n") {
		t.Errorf("code %d restored %v tty %q", rep.Code, rep.Restored, rep.TTY)
	}
	// The question goes to stderr's terminal even when stdout is a pipe,
	// and the answer's value to the pipe.
	rep = runPTY(t, ptyRun{
		Argv: []string{bin, "-c", "term prompt \"name?\"\n"}, Stdout: "pipe",
		Steps: []ptyStep{{Expect: "name? "}, {Send: "bob\r"}},
	})
	if rep.Code != 0 || rep.Stdout != "bob" || !rep.Restored {
		t.Errorf("piped stdout: code %d stdout %q tty %q", rep.Code, rep.Stdout, rep.TTY)
	}
}

// Typeahead never answers a question: what was typed before it is thrown
// away, for a prompt and for a confirm.
func TestBinaryTermTypeaheadIsNotAnAnswer(t *testing.T) {
	bin := ptyBin(t)
	rep := runPTY(t, ptyRun{
		Argv:  []string{bin, "-c", "term confirm \"delete it?\" && echo DELETED\necho end\n"},
		Steps: []ptyStep{{Send: "y\r"}, {Expect: "delete it? [y + Enter / N] "}, {Sleep: 0.5}, {Send: "\r"}, {Expect: "end"}},
	})
	if rep.Code != 0 || strings.Contains(rep.TTY, "DELETED") || !rep.Restored {
		t.Errorf("confirm: code %d tty %q", rep.Code, rep.TTY)
	}
}

// A question with ESC, a bidi override, and a newline prints escaped, and
// so does what the person types back.
func TestBinaryTermQuestionAndEchoAreEscaped(t *testing.T) {
	bin := ptyBin(t)
	file := filepath.Join(t.TempDir(), "s.sash")
	src := "let q = [ unbase64(\"b2sbWzJK4oCuPwpmYWtl\") ]\nlet a = (term prompt $q)\n"
	if err := os.WriteFile(file, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	rep := runPTY(t, ptyRun{
		Argv:  []string{bin, file},
		Steps: []ptyStep{{Expect: "fake "}, {Send: rawBytes("a\u202eb\r")}},
	})
	if rep.Code != 0 || !strings.Contains(rep.TTY, "ok<U+001B>[2J<U+202E>?<U+000A>fake a<U+202E>b") || strings.Contains(rep.TTY, "\x1b[2J") || strings.Contains(rep.TTY, "‮") {
		t.Errorf("code %d tty %q", rep.Code, rep.TTY)
	}
}

// With no terminal to ask, both forms are exit 2 at once and nothing
// after them runs: stdin a pipe, the script on stdin, and a script file
// whose stderr is not a terminal.
func TestBinaryTermNeedsATerminal(t *testing.T) {
	bin := ptyBin(t)
	for _, form := range []string{"term prompt \"q\"", "term confirm \"q\""} {
		src := form + "\necho RAN\n"
		file := filepath.Join(t.TempDir(), "s.sash")
		if err := os.WriteFile(file, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		for name, run := range map[string]ptyRun{
			"stdin a pipe":          {Argv: []string{bin, "-c", src}, Stdin: "pipe"},
			"the script on stdin":   {Argv: []string{bin}, Stdin: "pipe", Input: src},
			"stderr not a terminal": {Argv: []string{bin, file}, Stderr: "pipe"},
			"plan, stdin a pipe":    {Argv: []string{bin, "--plan", "-c", src}, Stdin: "pipe"},
			"lint":                  {Argv: []string{bin, "--lint", "-c", src}, Stdin: "pipe"},
		} {
			rep := runPTY(t, run)
			all := rep.TTY + rep.Stderr
			if name == "lint" {
				// --lint runs nothing: it names the rule and asks nobody.
				if !strings.Contains(rep.TTY+rep.Stdout, "prompt-needs-terminal") || strings.Contains(all, "q ") {
					t.Errorf("%s %s: %+v", form, name, rep)
				}
				continue
			}
			if rep.Code != 2 || !strings.Contains(all, "term needs a terminal") || strings.Contains(rep.TTY+rep.Stdout, "RAN") {
				t.Errorf("%s, %s: code %d tty %q stdout %q stderr %q", form, name, rep.Code, rep.TTY, rep.Stdout, rep.Stderr)
			}
		}
	}
}

// Ctrl-C at a question is 130, SIGHUP 129, SIGTERM the signal's own end,
// and the end of input status 1; each leaves the terminal mode as it was.
func TestBinaryTermRestoresTheTerminal(t *testing.T) {
	bin := ptyBin(t)
	const src = "try { term prompt \"q?\" } catch e { echo CAUGHT }\necho AFTER\n"
	for _, c := range []struct {
		name string
		step ptyStep
		code int
		ran  bool
	}{
		{"Ctrl-C", ptyStep{Send: "abc\x03"}, 130, false},
		{"SIGTERM", ptyStep{Signal: "TERM"}, -15, false},
		{"SIGHUP", ptyStep{Signal: "HUP"}, 129, false},
		{"Ctrl-D", ptyStep{Send: "\x04"}, 0, true},
	} {
		rep := runPTY(t, ptyRun{Argv: []string{bin, "-c", src}, Steps: []ptyStep{{Expect: "q? "}, {Sleep: 0.2}, c.step}})
		if rep.Code != c.code || !rep.Restored || strings.Contains(rep.TTY, "CAUGHT") || strings.Contains(rep.TTY, "AFTER") != c.ran {
			t.Errorf("%s: code %d restored %v tty %q", c.name, rep.Code, rep.Restored, rep.TTY)
		}
	}
	// The same at a confirm question.
	rep := runPTY(t, ptyRun{Argv: []string{bin, "-c", "term confirm \"q?\"\necho AFTER\n"}, Steps: []ptyStep{{Expect: "q? [y + Enter / N] "}, {Sleep: 0.4}, {Send: "\x03"}}})
	if rep.Code != 130 || !rep.Restored || strings.Contains(rep.TTY, "AFTER") {
		t.Errorf("confirm Ctrl-C: code %d restored %v tty %q", rep.Code, rep.Restored, rep.TTY)
	}
	rep = runPTY(t, ptyRun{Argv: []string{bin, "-c", "term confirm \"q?\"\necho AFTER\n"}, Steps: []ptyStep{{Expect: "q? [y + Enter / N] "}, {Sleep: 0.4}, {Signal: "TERM"}}})
	if rep.Code != -15 || !rep.Restored || strings.Contains(rep.TTY, "AFTER") {
		t.Errorf("confirm SIGTERM: code %d restored %v tty %q", rep.Code, rep.Restored, rep.TTY)
	}
}

// A question inside timeout ends at the deadline.
func TestBinaryTermEndsAtTheDeadline(t *testing.T) {
	bin := ptyBin(t)
	rep := runPTY(t, ptyRun{Argv: []string{bin, "-c", "timeout 1s { term prompt \"q?\" }\necho AFTER\n"}, Steps: []ptyStep{{Expect: "q? "}}})
	if rep.Code != 2 || !rep.Restored || !strings.Contains(rep.TTY, "timeout") || strings.Contains(rep.TTY, "AFTER") {
		t.Errorf("code %d restored %v tty %q", rep.Code, rep.Restored, rep.TTY)
	}
}

// The prompt asks too: a question typed at sash> is answered on the same
// terminal, and the next line reads the value.
func TestBinaryTermAtThePrompt(t *testing.T) {
	bin := ptyBin(t)
	rep := runPTY(t, ptyRun{
		Argv: []string{bin},
		Steps: []ptyStep{
			{Expect: "sash> "}, {Send: "let a = (term prompt \"name?\")\r"}, {Expect: "name? "}, {Send: "bob\r"},
			{Expect: "sash> "}, {Send: "echo hi $a [ is_tty() ]\r"}, {Expect: "hi bob true"}, {Send: "\x04"},
		},
	})
	if rep.Code != 0 || !rep.Restored {
		t.Errorf("code %d restored %v tty %q", rep.Code, rep.Restored, rep.TTY)
	}
}
