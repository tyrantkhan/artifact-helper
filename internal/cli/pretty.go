package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/tyrantkhan/artifact-helper/internal/headless"
)

// isTerminal reports whether x, one of Env's streams, is a terminal. Claude Code's Bash tool gives commands
// none, so agents keep getting JSON while a person at a terminal gets the readable view.
func isTerminal(env *Env, x any) bool {
	if env.Terminal != nil {
		return env.Terminal(x)
	}
	f, ok := x.(*os.File)
	return ok && terminalFile(f)
}

// ui writes the readable view: colors and marks when the terminal takes them, plain text otherwise.
type ui struct {
	w     io.Writer
	in    *bufio.Reader
	color bool
}

func newUI(env *Env) *ui {
	term := env.Getenv("TERM")
	// Windows' own console takes escape codes only when a program turns them on; Windows Terminal and the
	// Unix-style shells announce themselves.
	color := env.Getenv("NO_COLOR") == "" && term != "dumb" &&
		(runtime.GOOS != "windows" || env.Getenv("WT_SESSION") != "" || term != "")
	return &ui{w: env.Stdout, in: bufio.NewReader(env.Stdin), color: color}
}

func (u *ui) paint(code, s string) string {
	if !u.color {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (u *ui) bold(s string) string  { return u.paint("1", s) }
func (u *ui) dim(s string) string   { return u.paint("2", s) }
func (u *ui) green(s string) string { return u.paint("32", s) }
func (u *ui) red(s string) string   { return u.paint("31", s) }
func (u *ui) cyan(s string) string  { return u.paint("36", s) }

func (u *ui) mark(ok bool) string {
	switch {
	case ok && u.color:
		return u.green("✓")
	case ok:
		return "+"
	case u.color:
		return u.red("✗")
	}
	return "x"
}

func (u *ui) bullet() string {
	if u.color {
		return u.dim("·")
	}
	return "-"
}

func (u *ui) title(s string) { fmt.Fprintf(u.w, "\n%s\n\n", u.bold(s)) }

func (u *ui) row(mark, label, value string) { fmt.Fprintf(u.w, "  %s %-13s %s\n", mark, label, value) }

// problem prints a refusal's message and fix under the rows.
func (u *ui) problem(err error) {
	rf, ok := headless.IsRefusal(err)
	if !ok {
		rf = &headless.Refusal{Message: err.Error()}
	}
	fmt.Fprintf(u.w, "\n  %s\n", u.red(rf.Message))
	if rf.Fix != "" {
		fmt.Fprintf(u.w, "  %s %s\n", u.bold("Fix:"), rf.Fix)
	}
	fmt.Fprintln(u.w)
}

var errNoAnswer = &headless.Refusal{Code: "no-answer", Message: "init asked a question, but its input ended",
	Fix: "run artifact-helper init in a terminal and answer each question, or pass --save and --claude-config-dir up front (and --plugin to install the plugin)"}

// confirm asks a yes-or-no question; an empty answer takes def.
func (u *ui) confirm(question string, def bool) (bool, error) {
	hint := "[y/N]"
	if def {
		hint = "[Y/n]"
	}
	for {
		fmt.Fprintf(u.w, "  %s %s ", question, u.dim(hint))
		answer, err := u.answer()
		if err != nil {
			return false, err
		}
		switch strings.ToLower(answer) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
	}
}

// choose asks for a number from 1 to n; an empty answer takes def.
func (u *ui) choose(question string, n, def int) (int, error) {
	for {
		fmt.Fprintf(u.w, "  %s %s ", question, u.dim("["+strconv.Itoa(def)+"]"))
		answer, err := u.answer()
		if err != nil {
			return 0, err
		}
		if answer == "" {
			return def, nil
		}
		if i, err := strconv.Atoi(answer); err == nil && i >= 1 && i <= n {
			return i, nil
		}
	}
}

func (u *ui) answer() (string, error) {
	line, err := u.in.ReadString('\n')
	if errors.Is(err, io.EOF) && line == "" {
		fmt.Fprintln(u.w)
		return "", errNoAnswer
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("reading the answer: %w", err)
	}
	return strings.TrimSpace(line), nil
}
