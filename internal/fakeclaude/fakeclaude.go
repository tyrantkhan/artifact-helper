// Package fakeclaude stands in for the claude binary in tests, on every system. Install copies the running
// test binary to a temporary claude with a spec file beside it; when that copy runs, Main answers as the
// spec says. A shell script cannot do this on Windows.
package fakeclaude

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	specName = "fake-claude.json"
	// runStart begins each invocation in the record, so Runs splits runs without guessing at paths.
	runStart = "\x1e"
)

// Spec says how the fake answers.
type Spec struct {
	Version  string // printed for --version
	Auth     string // printed for auth, such as auth status
	InfoCode int    // exit code of --version and auth
	// Outputs holds the stdout of a run, chosen by the "Task: <op>." marker that starts the prompt (the last
	// argument), falling back to the "" key.
	Outputs map[string]string
	Code    int           // exit code of a run
	Sleep   time.Duration // how long a run waits before answering
	// Files are created by every run in its working directory, by slash-separated relative path, unless they
	// already exist.
	Files map[string]string
	// Record, when set, is the file each invocation appends its working directory and arguments to.
	Record string
	// EnvRecord, when set, is the file each invocation appends one line to: EnvName=value, or EnvName alone
	// when the variable is not in its environment.
	EnvRecord string
	EnvName   string
	// Stderr is printed on stderr by every invocation, auth status and plugin commands included.
	Stderr string
	// Plugin holds the stdout of each claude plugin command, keyed by its arguments after "plugin" joined with
	// spaces, such as "list --json". A command not listed exits 1.
	Plugin map[string]string
}

// Main answers as the fake claude and exits when the running binary is a copy Install made; otherwise it
// returns. Call it first in TestMain.
func Main() {
	exe, err := os.Executable()
	if err != nil {
		panic(fmt.Sprintf("fakeclaude: cannot find the running binary: %v", err))
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(exe), specName))
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		fail(err)
	}
	var s Spec
	if err := json.Unmarshal(data, &s); err != nil {
		fail(err)
	}
	code, err := s.run(os.Args[1:])
	if err != nil {
		fail(err)
	}
	// Not os.Exit: under -race, a clean os.Exit waits a second for late race reports, on every fake run.
	// Nothing here is buffered, so skipping the exit hooks loses nothing.
	syscall.Exit(code)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "fakeclaude:", err)
	os.Exit(125)
}

func (s Spec) run(args []string) (int, error) {
	if s.Record != "" {
		if err := s.record(args); err != nil {
			return 0, err
		}
	}
	if s.EnvRecord != "" {
		if err := s.recordEnv(); err != nil {
			return 0, err
		}
	}
	if s.Stderr != "" {
		fmt.Fprintln(os.Stderr, s.Stderr)
	}
	if len(args) > 0 {
		switch args[0] {
		case "--version":
			fmt.Println(s.Version)
			return s.InfoCode, nil
		case "auth":
			fmt.Println(s.Auth)
			return s.InfoCode, nil
		case "plugin":
			out, ok := s.Plugin[strings.Join(args[1:], " ")]
			if !ok {
				fmt.Fprintln(os.Stderr, "fakeclaude: no answer for plugin", strings.Join(args[1:], " "))
				return 1, nil
			}
			fmt.Println(out)
			return 0, nil
		}
	}
	// claude -p offers no Artifact tool unless CLAUDE_CODE_ARTIFACT is on, so neither does the fake.
	if v := strings.ToLower(os.Getenv("CLAUDE_CODE_ARTIFACT")); v != "1" && v != "true" && v != "yes" && v != "on" {
		return 0, errors.New("CLAUDE_CODE_ARTIFACT is not on, so claude -p would offer no Artifact tool")
	}
	time.Sleep(s.Sleep)
	if len(s.Files) > 0 {
		if err := inTempDir(); err != nil {
			return 0, err
		}
	}
	for name, content := range s.Files {
		p := filepath.FromSlash(name)
		if _, err := os.Stat(p); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return 0, err
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return 0, err
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return 0, err
		}
	}
	var prompt string
	if len(args) > 0 {
		prompt = args[len(args)-1]
	}
	out := s.Outputs[""]
	for op, o := range s.Outputs {
		if op != "" && strings.HasPrefix(prompt, "Task: "+op+".") {
			out = o
			break
		}
	}
	fmt.Println(out)
	return s.Code, nil
}

// inTempDir refuses a run whose working directory is outside the temporary folder: there the Files it writes
// would land in the source tree, as they once did.
func inTempDir() error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	real := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return p
	}
	rel, err := filepath.Rel(real(os.TempDir()), real(cwd))
	if err != nil || !filepath.IsLocal(rel) {
		return fmt.Errorf("refusing to write test files in %s, outside the temporary folder", cwd)
	}
	return nil
}

func (s Spec) recordEnv() error {
	line := s.EnvName
	if v, ok := os.LookupEnv(s.EnvName); ok {
		line += "=" + v
	}
	f, err := os.OpenFile(s.EnvRecord, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = io.WriteString(f, line+"\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func (s Spec) record(args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.Record, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = io.WriteString(f, runStart+strings.Join(append([]string{cwd}, args...), "\x00")+"\x00")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// Install copies the running test binary to a claude in a new temporary directory, writes spec beside it, and
// returns the copy's path.
func Install(t testing.TB, spec Spec) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	// A hard link is instant; copying covers a temporary directory on another file system.
	if lerr := os.Link(exe, bin); lerr != nil {
		data, err := os.ReadFile(exe)
		if err == nil {
			err = os.WriteFile(bin, data, 0o755)
		}
		if err != nil {
			t.Fatalf("install the fake claude: link: %v; copy: %v", lerr, err)
		}
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, specName), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return bin
}

// Runs reads a record back as one list per invocation: the working directory, then the arguments.
func Runs(t testing.TB, record string) [][]string {
	t.Helper()
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	var runs [][]string
	for _, r := range strings.Split(string(data), runStart)[1:] {
		runs = append(runs, strings.Split(strings.TrimSuffix(r, "\x00"), "\x00"))
	}
	return runs
}

// ToolRun is the stdout of a stream-json run that made one call to tool: the tool returned data, its structured
// result (JSON, "null" for none), and text, what the model was shown, marked as an error when isError. The
// run then ended with result, its final result line.
func ToolRun(tool, data, text string, isError bool, result string) string {
	return ToolCall(tool, `{}`, data, text, isError, result)
}

// ToolCall is ToolRun with the call's input, such as {"action":"publish"}.
func ToolCall(tool, input, data, text string, isError bool, result string) string {
	use, err := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_use", "id": "toolu_1", "name": tool, "input": json.RawMessage(input)},
	}}})
	if err != nil {
		panic(err)
	}
	res, err := json.Marshal(map[string]any{"type": "user", "tool_use_result": json.RawMessage(data), "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "is_error": isError, "content": []any{map[string]any{"type": "text", "text": text}}},
	}}})
	if err != nil {
		panic(err)
	}
	return string(use) + "\n" + string(res) + "\n" + result
}

// Call is one tool call for Calls: the tool, its input, its own data, the text the model was shown, and
// whether the tool refused it.
type Call struct {
	Tool, Input, Data, Text string
	IsError                 bool
}

// Calls is a stream-json run that makes calls, in order, then ends with the result line result.
func Calls(result string, calls ...Call) string {
	var b strings.Builder
	for i, c := range calls {
		id := fmt.Sprintf("toolu_%d", i+1)
		use, err := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
			map[string]any{"type": "tool_use", "id": id, "name": c.Tool, "input": json.RawMessage(c.Input)},
		}}})
		if err != nil {
			panic(err)
		}
		res, err := json.Marshal(map[string]any{"type": "user", "tool_use_result": json.RawMessage(c.Data), "message": map[string]any{"content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": id, "is_error": c.IsError, "content": []any{map[string]any{"type": "text", "text": c.Text}}},
		}}})
		if err != nil {
			panic(err)
		}
		b.WriteString(string(use) + "\n" + string(res) + "\n")
	}
	return b.String() + result
}
