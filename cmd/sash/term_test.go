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
			{Expect: "unit? "}, {Sleep: 0.4}, {Send: "ngx\x7finx\r"},
			{Expect: "again? [y + Enter / N] "}, {Sleep: 0.4}, {Send: "y"}, {Sleep: 0.1}, {Send: "\r"},
		},
	})
	// What the script printed is written before the next question, and
	// the rest when it ends.
	if rep.Code != 0 || !rep.Restored || !strings.Contains(rep.TTY, "got nginx\r\n") || strings.Index(rep.TTY, "got nginx") > strings.Index(rep.TTY, "again?") || !strings.HasSuffix(rep.TTY, "yes\r\n") {
		t.Errorf("code %d restored %v tty %q", rep.Code, rep.Restored, rep.TTY)
	}
	// The question goes to stderr's terminal even when stdout is a pipe,
	// and the answer's value to the pipe.
	rep = runPTY(t, ptyRun{
		Argv: []string{bin, "-c", "term prompt \"name?\"\n"}, Stdout: "pipe",
		Steps: []ptyStep{{Expect: "name? "}, {Sleep: 0.4}, {Send: "bob\r"}},
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
		Steps: []ptyStep{{Expect: "fake "}, {Sleep: 0.4}, {Send: rawBytes("a\u202eb\r")}},
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
		rep := runPTY(t, ptyRun{Argv: []string{bin, "-c", src}, Steps: []ptyStep{{Expect: "q? "}, {Sleep: 0.4}, c.step}})
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
			{Expect: "sash> "}, {Send: "let a = (term prompt \"name?\")\r"}, {Expect: "name? "}, {Sleep: 0.4}, {Send: "bob\r"},
			{Expect: "sash> "}, {Send: "echo hi $a [ is_tty() ]\r"}, {Expect: "hi bob true"}, {Send: "\x04"},
		},
	})
	if rep.Code != 0 || !rep.Restored {
		t.Errorf("code %d restored %v tty %q", rep.Code, rep.Restored, rep.TTY)
	}
}

// term prompt -s at a real terminal echoes nothing, the value prints as
// ***, and the typed secret is nowhere on the terminal or in the output.
func TestBinaryTermSecretIsNotEchoed(t *testing.T) {
	bin := ptyBin(t)
	rep := runPTY(t, ptyRun{
		Argv:  []string{bin, "-c", "declare -s tok = (term prompt -s \"token?\")\necho $tok [ len($tok) ]\n"},
		Steps: []ptyStep{{Expect: "token? "}, {Sleep: 0.4}, {Send: "hunter2\x7f3\r"}},
	})
	if rep.Code != 0 || !rep.Restored || !strings.HasSuffix(rep.TTY, "token? \r\r\n\x1b[?2004l*** ***\r\n") || strings.Contains(rep.TTY, "hunter") {
		t.Errorf("code %d restored %v tty %q", rep.Code, rep.Restored, rep.TTY)
	}
	// Misplaced, nobody is asked and nothing before it runs.
	rep = runPTY(t, ptyRun{Argv: []string{bin, "-c", "echo before\nlet tok = (term prompt -s \"token?\")\n"}})
	if rep.Code != 2 || strings.Contains(rep.TTY, "before") || strings.Contains(rep.TTY, "token? ") || !strings.Contains(rep.TTY, "term prompt -s: only as declare -s name") {
		t.Errorf("misplaced: code %d tty %q", rep.Code, rep.TTY)
	}
}

// A recording made at a terminal holds no byte of the secret, and its
// replay refuses instead of asking.
func TestBinaryTermSecretRecordAndReplay(t *testing.T) {
	bin := ptyBin(t)
	dir := t.TempDir()
	fixture := filepath.Join(dir, "fx.json")
	if err := os.WriteFile(fixture, []byte(`{"grant":{"entitlements":[],"file_read":[],"file_write":[]},"results":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	const src = "let who = (term prompt \"who?\")\ndeclare -s tok = (term prompt -s \"token?\")\necho $who $tok\n"
	rec := filepath.Join(dir, "rec.jsonl")
	rep := runPTY(t, ptyRun{
		Argv:  []string{bin, "--record", rec, "--fixture", fixture, "-c", src},
		Steps: []ptyStep{{Expect: "who? "}, {Sleep: 0.4}, {Send: "bob\r"}, {Expect: "token? "}, {Sleep: 0.4}, {Send: "hunter2-SECRET\r"}},
	})
	if rep.Code != 0 || !rep.Restored || !strings.Contains(rep.TTY, "bob ***") {
		t.Fatalf("record: code %d tty %q", rep.Code, rep.TTY)
	}
	b, err := os.ReadFile(rec)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); strings.Contains(got, "hunter2") || !strings.Contains(got, `"op":"prompt-s","redacted":true`) || !strings.Contains(got, `"op":"prompt","value":"bob"`) {
		t.Errorf("recording:\n%s", got)
	}
	rep = runPTY(t, ptyRun{Argv: []string{bin, "--replay", rec, "--fixture", fixture, "-c", src}})
	if rep.Code != 2 || !strings.Contains(rep.TTY, "replay_secret 2") || strings.Contains(rep.TTY, "who? ") || strings.Contains(rep.TTY, "token? ") {
		t.Errorf("replay: code %d tty %q", rep.Code, rep.TTY)
	}
	// A recording with only a plain answer replays with no terminal at all.
	const plain = "let who = (term prompt \"who?\")\necho hi $who [ is_tty() ]\n"
	rec = filepath.Join(dir, "plain.jsonl")
	rep = runPTY(t, ptyRun{Argv: []string{bin, "--record", rec, "--fixture", fixture, "-c", plain}, Steps: []ptyStep{{Expect: "who? "}, {Sleep: 0.4}, {Send: "bob\r"}}})
	if rep.Code != 0 || !strings.Contains(rep.TTY, "hi bob true") {
		t.Fatalf("record plain: code %d tty %q", rep.Code, rep.TTY)
	}
	rep = runPTY(t, ptyRun{Argv: []string{bin, "--replay", rec, "--fixture", fixture, "-c", plain}, Stdin: "pipe", Stdout: "pipe", Stderr: "pipe"})
	if rep.Code != 0 || rep.Stdout != "hi bob true\n" || rep.TTY != "" {
		t.Errorf("replay plain: code %d stdout %q stderr %q tty %q", rep.Code, rep.Stdout, rep.Stderr, rep.TTY)
	}
}

// A no at term confirm, its Enter typed 100 ms later or after an "o",
// does not answer the next term prompt: the prompt waits for the person,
// and nothing typed reaches the terminal after sash ends.
func TestBinaryTermANoDoesNotAnswerTheNextPrompt(t *testing.T) {
	bin := ptyBin(t)
	const src = "term confirm \"delete the old one?\" || echo declined\nlet n = (term prompt \"name of unit?\")\necho got $n end\n"
	for name, no := range map[string][]ptyStep{
		"n, Enter 100 ms later": {{Send: "n"}, {Sleep: 0.1}, {Send: "\r"}},
		"no and Enter":          {{Send: "no\r"}},
		"n, o, Enter":           {{Send: "n"}, {Sleep: 0.15}, {Send: "o"}, {Sleep: 0.15}, {Send: "\r"}},
	} {
		steps := append([]ptyStep{{Expect: "delete the old one? [y + Enter / N] "}, {Sleep: 0.4}}, no...)
		steps = append(steps, ptyStep{Expect: "name of unit? "}, ptyStep{Sleep: 1.0}, ptyStep{Send: "nginx\r"}, ptyStep{Expect: " end"})
		rep := runPTY(t, ptyRun{Argv: []string{bin, "-c", src}, Steps: steps})
		if rep.Code != 0 || !rep.Restored || !strings.Contains(rep.TTY, "declined") || !strings.Contains(rep.TTY, "got nginx end") {
			t.Errorf("%s: code %d restored %v tty %q", name, rep.Code, rep.Restored, rep.TTY)
		}
	}
}

// What a script printed is on the screen before the question it leads
// to, escaped as the rest is, and is not printed twice; a pipe still gets
// the bytes as they are. The prompt does the same for a line.
func TestBinaryTermOutputComesBeforeTheQuestion(t *testing.T) {
	bin := ptyBin(t)
	const src = "echo \"about to wipe\" [ unbase64(\"G1sySg==\") ]\nterm confirm \"ok?\" && echo yes\n"
	rep := runPTY(t, ptyRun{
		Argv:  []string{bin, "-c", src},
		Steps: []ptyStep{{Expect: "ok? [y + Enter / N] "}, {Sleep: 0.4}, {Send: "y"}, {Sleep: 0.1}, {Send: "\r"}, {Expect: "yes"}},
	})
	wipe, ask := strings.Index(rep.TTY, "about to wipe <U+001B>[2J"), strings.Index(rep.TTY, "ok? [y + Enter / N]")
	if rep.Code != 0 || !rep.Restored || wipe < 0 || ask < 0 || wipe > ask || strings.Count(rep.TTY, "about to wipe") != 1 || strings.Contains(rep.TTY, "\x1b[2J") {
		t.Errorf("script: code %d tty %q", rep.Code, rep.TTY)
	}
	rep = runPTY(t, ptyRun{
		Argv: []string{bin, "-c", src}, Stdout: "pipe",
		Steps: []ptyStep{{Expect: "ok? [y + Enter / N] "}, {Sleep: 0.4}, {Send: "y"}, {Sleep: 0.1}, {Send: "\r"}},
	})
	if rep.Code != 0 || rep.Stdout != "about to wipe \x1b[2J\nyes\n" {
		t.Errorf("piped: code %d stdout %q", rep.Code, rep.Stdout)
	}
	rep = runPTY(t, ptyRun{
		Argv: []string{bin},
		Steps: []ptyStep{
			{Expect: "sash> "}, {Send: "echo before; let a = (term prompt \"name?\")\r"}, {Expect: "name? "}, {Sleep: 0.4}, {Send: "bob\r"},
			{Expect: "sash> "}, {Send: "\x04"},
		},
	})
	before, name := strings.Index(rep.TTY, "before\r\n"), strings.Index(rep.TTY, "name? ")
	if rep.Code != 0 || before < 0 || name < 0 || before > name || strings.Count(rep.TTY, "before\r\n") != 1 {
		t.Errorf("prompt: code %d tty %q", rep.Code, rep.TTY)
	}
}
