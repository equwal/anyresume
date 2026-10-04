// Package app implements the anyresume commands.
package app

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/AIToolSharing/anyresume/internal/herdr"
	"github.com/AIToolSharing/anyresume/internal/session"
)

// PluginID is the id in herdr-plugin.toml.
const PluginID = "aitoolsharing.anyresume"

const usage = `anyresume - resume any Claude Code session from one list (unofficial)

Usage:
  anyresume [pick] [--loop] [--query TEXT]   choose a session with fzf and resume it
  anyresume list [--json]                    print all sessions, newest first
  anyresume open <session-id>                resume a session in herdr or in this terminal
  anyresume resume <session-id>              resume a session in this terminal
  anyresume import [--refresh] [--limit N]   make a herdr tab for each session
  anyresume popup                            open the picker as a herdr popup
  anyresume version

In herdr (HERDR_ENV=1), a session opens in a herdr tab. In other terminals,
it opens in the current terminal. Each session resumes in its own folder,
whatever folder you start anyresume in.
`

// env holds the locations and the herdr client that the commands use.
type env struct {
	configDir  string
	desktopDir string
	// socket and statePath are set only in a herdr session.
	socket    string
	statePath string
	herdr     *herdr.Client
	stdout    io.Writer
	stderr    io.Writer
}

// Run runs one command and returns the process exit code.
func Run(args []string, version string) int {
	cmd := "pick"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "version":
		fmt.Println(version)
		return 0
	case "help":
		fmt.Print(usage)
		return 0
	}
	e, err := newEnv()
	if err == nil {
		switch cmd {
		case "pick":
			err = e.pick(args)
		case "list":
			err = e.list(args)
		case "open":
			err = e.openByID(args, false)
		case "resume":
			err = e.openByID(args, true)
		case "import":
			err = e.importTabs(args)
		case "popup":
			err = e.popup()
		default:
			fmt.Fprintf(os.Stderr, "anyresume: unknown command %q\n\n%s", cmd, usage)
			return 2
		}
	}
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "anyresume:", err)
		return 1
	}
	return 0
}

func newEnv() (*env, error) {
	configDir, err := session.ConfigDir()
	if err != nil {
		return nil, err
	}
	desktopDir, err := session.DesktopDir()
	if err != nil {
		return nil, err
	}
	userConfig, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	e := &env{
		configDir:  configDir,
		desktopDir: desktopDir,
		stdout:     os.Stdout,
		stderr:     os.Stderr,
	}
	if os.Getenv("HERDR_ENV") != "1" {
		return e, nil
	}
	e.herdr = herdr.New()
	// herdr gives panes and plugin commands the socket path of their
	// session. anyresume keeps one record of imported tabs for each session.
	e.socket = os.Getenv("HERDR_SOCKET_PATH")
	if e.socket == "" {
		return e, nil
	}
	e.statePath = statePath(userConfig, e.socket)
	// HERDR_SESSION names a named session. The old single record belongs to
	// the default session.
	if os.Getenv("HERDR_SESSION") == "" {
		if err := migrateLegacyState(legacyStatePath(userConfig), e.statePath); err != nil {
			return nil, err
		}
	}
	return e, nil
}

// sessions returns the Claude Code sessions and the Codex, Copilot CLI, and
// Antigravity sessions of this user, newest first.
func (e *env) sessions() ([]session.Session, error) {
	if r := os.Getenv(remoteEnv); r != "" {
		return remoteSessions(r)
	}
	s, err := session.Discover(e.configDir, e.desktopDir)
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	s = append(s, session.DiscoverOthers(home)...)
	sort.SliceStable(s, func(i, j int) bool { return s[i].LastActive.After(s[j].LastActive) })
	return s, nil
}

// remoteEnv names the variable for remote host mode. Its value is the
// command that runs anyresume on another machine, for example
// "ssh -t -p 2201 jose@127.0.0.1 /home/jose/.local/bin/anyresume". Then the
// sessions come from "list --json" on that machine, and each tab resumes
// its session there.
const remoteEnv = "ANYRESUME_REMOTE"

// remoteSessions runs "list --json" through the remote command r.
func remoteSessions(r string) ([]session.Session, error) {
	argv := strings.Fields(r)
	// -t asks for a terminal, which a pipe does not have.
	argv = slices.DeleteFunc(argv, func(a string) bool { return a == "-t" })
	cmd := exec.Command(argv[0], append(argv[1:], "list", "--json")...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list sessions on the remote host: %w", err)
	}
	var s []session.Session
	return s, json.Unmarshal(out, &s)
}

// holders returns the live holders of sessions. The registry is optional:
// on an error, the result is empty.
func (e *env) holders() map[string]session.Holder {
	h, err := session.Holders(e.configDir)
	if err != nil {
		return map[string]session.Holder{}
	}
	return h
}

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(fs.Output(), usage) }
	return fs
}

func (e *env) pick(args []string) error {
	fs := newFlags("pick")
	loop := fs.Bool("loop", false, "show the list again after each choice")
	query := fs.String("query", "", "start with this search text")
	if err := fs.Parse(args); err != nil {
		return err
	}
	for {
		sessions, err := e.sessions()
		if err != nil {
			return err
		}
		s, ok, err := pick(sessions, e.holders(), *query)
		if err != nil || !ok {
			return err
		}
		if err := e.open(s); err != nil {
			if !*loop {
				return err
			}
			fmt.Fprintln(e.stderr, "anyresume:", err)
			time.Sleep(5 * time.Second)
		}
		if !*loop {
			return nil
		}
		*query = ""
	}
}

func (e *env) list(args []string) error {
	fs := newFlags("list")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	sessions, err := e.sessions()
	if err != nil {
		return err
	}
	held := e.holders()
	if *asJSON {
		type row struct {
			session.Session
			Live bool `json:"live"`
		}
		rows := make([]row, len(sessions))
		for i, s := range sessions {
			_, live := held[s.ID]
			rows[i] = row{s, live}
		}
		enc := json.NewEncoder(e.stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	for _, s := range sessions {
		_, live := held[s.ID]
		fmt.Fprintf(e.stdout, "%s  %s\n", displayRow(s, live), s.ID)
	}
	return nil
}

// openByID resumes the session with the ID or with a unique ID prefix of
// eight or more characters. With here, it resumes in this terminal.
func (e *env) openByID(args []string, here bool) error {
	if len(args) != 1 {
		return errors.New("give one session ID")
	}
	sessions, err := e.sessions()
	if err != nil {
		return err
	}
	s, err := findSession(sessions, args[0])
	if err != nil {
		return err
	}
	if here {
		return e.resumeHere(s)
	}
	return e.open(s)
}

func findSession(sessions []session.Session, id string) (session.Session, error) {
	var found []session.Session
	for _, s := range sessions {
		if s.ID == id {
			return s, nil
		}
		if len(id) >= 8 && strings.HasPrefix(s.ID, id) {
			found = append(found, s)
		}
	}
	switch len(found) {
	case 0:
		return session.Session{}, fmt.Errorf("found no session with the ID %q", id)
	case 1:
		return found[0], nil
	}
	return session.Session{}, fmt.Errorf("%d sessions start with %q; give more of the ID", len(found), id)
}

// open resumes s: in a herdr tab when anyresume runs in herdr, else in this
// terminal.
func (e *env) open(s session.Session) error {
	if e.herdr != nil {
		return e.openInHerdr(s)
	}
	return e.resumeHere(s)
}

// resumeHere resumes s in this terminal, in the folder of s.
func (e *env) resumeHere(s session.Session) error {
	if h, ok := e.holders()[s.ID]; ok {
		return heldError(s, h)
	}
	return runAgent(sessionDir(s), s.Argv())
}

// sessionDir returns the folder of s. When the folder is gone, it returns
// the home folder: Claude Code finds a session by its ID from any folder.
func sessionDir(s session.Session) string {
	if s.Cwd != "" {
		if info, err := os.Stat(s.Cwd); err == nil && info.IsDir() {
			return s.Cwd
		}
	}
	home, _ := os.UserHomeDir()
	return home
}

// resumeCommand returns the shell command that resumes the session with the
// ID: the path of this program and "resume <id>". Session IDs are UUIDs, so
// the ID needs no quotes.
func resumeCommand(id string) (string, error) {
	if r := os.Getenv(remoteEnv); r != "" {
		return r + " resume " + id, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return quoteExe(exe) + " resume " + id, nil
}

func heldError(s session.Session, h session.Holder) error {
	where := "another terminal"
	if h.Entrypoint == "claude-desktop" {
		where = "the Claude desktop app"
	}
	return fmt.Errorf("%q is open in %s (PID %d). Close it there, then try again. Two processes that write one session split the conversation", cleanTitle(s.Title), where, h.PID)
}

func (e *env) popup() error {
	if e.herdr == nil {
		return errors.New("popup needs herdr: run it in a herdr pane or as the plugin action")
	}
	return e.herdr.OpenPluginPane(PluginID, "picker")
}
