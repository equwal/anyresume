package app

import (
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/AIToolSharing/anyresume/internal/herdr"
	"github.com/AIToolSharing/anyresume/internal/session"
	"pgregory.net/rapid"
)

func randomSession(t *rapid.T) session.Session {
	return session.Session{
		ID:         rapid.StringMatching(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`).Draw(t, "id"),
		Title:      rapid.String().Draw(t, "title"),
		LastActive: time.Unix(rapid.Int64Range(0, 4e9).Draw(t, "time"), 0),
		Archived:   rapid.Bool().Draw(t, "archived"),
	}
}

// TestRowIndexRoundTrip checks that every title, also with tabs and line
// breaks, gives one fzf row and that the row gives back its index.
func TestRowIndexRoundTrip(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		s := randomSession(rt)
		i := rapid.IntRange(0, 1<<20).Draw(rt, "index")
		row := fzfRow(i, s, rapid.Bool().Draw(rt, "live"))
		if strings.ContainsAny(displayRow(s, false), "\t\r\n") {
			rt.Fatalf("displayRow has a tab or a line break: %q", displayRow(s, false))
		}
		got, err := rowIndex(row + "\n")
		if err != nil || got != i {
			rt.Fatalf("rowIndex(%q) = %d, %v; want %d", row, got, err, i)
		}
	})
}

var agentNameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// TestAgentNameIsValid checks the herdr name rule for every title and ID.
func TestAgentNameIsValid(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		s := session.Session{ID: rapid.String().Draw(rt, "id"), Title: rapid.String().Draw(rt, "title")}
		if name := agentName(s); !agentNameRE.MatchString(name) {
			rt.Fatalf("agentName(%q, %q) = %q, not a valid herdr agent name", s.Title, s.ID, name)
		}
	})
}

func TestAgentNameExamples(t *testing.T) {
	cases := map[string]string{
		"Deploy Android APKs to Play Store": "deploy-android-apks-to-pla-2d5b",
		"四字熟語 deck card updates":            "deck-card-updates-2d5b",
		"2026 plan":                         "chat-2026-plan-2d5b",
		"":                                  "chat-2d5b",
	}
	for title, want := range cases {
		if got := agentName(session.Session{ID: "2d5b72c7-8fb9", Title: title}); got != want {
			t.Errorf("agentName(%q) = %q, want %q", title, got, want)
		}
	}
}

// TestTabLabel checks that a label is one short, non-empty line.
func TestTabLabel(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		l := tabLabel(rapid.String().Draw(rt, "title"))
		if l == "" || utf8.RuneCountInString(l) > tabLabelMax {
			rt.Fatalf("tabLabel = %q", l)
		}
		for _, r := range l {
			if unicode.IsControl(r) {
				rt.Fatalf("tabLabel %q has a control character", l)
			}
		}
	})
}

func TestPromptRE(t *testing.T) {
	for _, s := range []string{`PS C:\Users\a>`, `PS C:\Users\a> `, `C:\work>`, "user@host:~$ ", "% ", "root# ", "C:\\a>\r\n"} {
		if !promptRE.MatchString(strings.TrimRight(s, "\r\n")) {
			t.Errorf("promptRE does not match the empty prompt %q", s)
		}
	}
	for _, s := range []string{`C:\work>anyresume.exe resume 1234`, `PS C:\> claude`, "$ ls", ""} {
		if promptRE.MatchString(s) {
			t.Errorf("promptRE matches %q, which is not an empty prompt", s)
		}
	}
}

func TestPromptActionFor(t *testing.T) {
	id := "00089cd3-6588-4ee5-8f8e-f388a5bf6f10"
	cmd := `C:\Users\A~1\plugins\new\bin\anyresume.exe resume ` + id
	cases := []struct {
		text string
		want promptAction
	}{
		{"", waitForShell},
		{"\r\n  \n", waitForShell},
		{`C:\work>`, typeCommand},
		{"C:\\work>\r\n", typeCommand},
		{`C:\work>` + cmd, leaveAlone},
		{`C:\work>C:\Users\A~1\plugins\old\bin\anyresume.exe resume ` + id, replaceCommand},
		{`C:\work>dir`, leaveAlone},
		{`C:\work>anyresume.exe resume 11111111-6588-4ee5-8f8e-f388a5bf6f10`, leaveAlone},
	}
	for _, c := range cases {
		if got := promptActionFor(c.text, id, cmd); got != c.want {
			t.Errorf("promptActionFor(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

// TestPromptActionReplacesOnlyOldCommands checks any prompt and any old
// program path: an old resume command for the session is replaced, and the
// current one stays.
func TestPromptActionReplacesOnlyOldCommands(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		id := rapid.StringMatching(`[0-9a-f]{8}-[0-9a-f]{4}`).Draw(rt, "id")
		prompt := rapid.String().Draw(rt, "prompt") + ">"
		cmd := rapid.StringN(1, 80, -1).Draw(rt, "newPath") + " resume " + id
		old := rapid.StringN(1, 80, -1).Draw(rt, "oldPath") + " resume " + id
		if got := promptActionFor(prompt+cmd, id, cmd); got != leaveAlone {
			rt.Fatalf("current command: got %v, want leaveAlone", got)
		}
		if strings.HasSuffix(prompt+old, cmd) {
			return
		}
		if got := promptActionFor(prompt+old, id, cmd); got != replaceCommand {
			rt.Fatalf("old command %q: got %v, want replaceCommand", prompt+old, got)
		}
	})
}

func TestPlanImport(t *testing.T) {
	day := time.Date(2026, 9, 26, 12, 0, 0, 0, time.Local)
	sessions := []session.Session{
		{ID: "running", LastActive: day},
		{ID: "imported", LastActive: day},
		{ID: "closed-tab", LastActive: day.AddDate(0, 0, -1)},
		{ID: "new", LastActive: day.AddDate(0, 0, -2)},
	}
	st := state{Tabs: map[string]importedTab{
		"imported":   {Tab: "w4:t1", Pane: "w4:p1"},
		"closed-tab": {Tab: "w4:t2", Pane: "w4:p2"},
	}}
	snap := herdr.Snapshot{Panes: []herdr.Pane{
		{ID: "w1:p1", AgentSession: &herdr.AgentSession{Value: "running"}},
		{ID: "w4:p1"},
	}}

	got := planImport(sessions, st, snap, nil, false)
	want := []importStep{
		{kind: stepRetype, session: sessions[1], tab: importedTab{Tab: "w4:t1", Pane: "w4:p1"}},
		{kind: stepCreate, session: sessions[2], workspace: "chats 09-25"},
		{kind: stepCreate, session: sessions[3], workspace: "chats 09-24"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("planImport =\n%+v\nwant\n%+v", got, want)
	}

	refresh := planImport(sessions, st, snap, nil, true)
	if len(refresh) != 1 || refresh[0].kind != stepRetype {
		t.Fatalf("planImport with refresh = %+v, want only the retype step", refresh)
	}
}

// TestPlanImportSkipsForkedSessions is the regression test for v0.1.1: a
// herdr pane ran "claude --resume <id> --fork-session", and import still
// made a second tab for <id>.
func TestPlanImportSkipsForkedSessions(t *testing.T) {
	sessions := []session.Session{{ID: "2d5b72c7-8fb9-4dad-9c7a-c2b1224a8644", LastActive: time.Now()}}
	snap := herdr.Snapshot{Panes: []herdr.Pane{{ID: "w2:p1", Agent: "claude", AgentSession: &herdr.AgentSession{Value: "1a00dd3b-4912-405c-9d95-f07ae324d05b"}}}}
	inPanes := map[string]bool{"--resume": true, "2d5b72c7-8fb9-4dad-9c7a-c2b1224a8644": true, "--fork-session": true}
	st := state{Tabs: map[string]importedTab{}}
	if steps := planImport(sessions, st, snap, inPanes, false); len(steps) != 0 {
		t.Fatalf("planImport = %+v, want no step for a session that a pane runs as a copy", steps)
	}
	if steps := planImport(sessions, st, snap, nil, false); len(steps) != 1 {
		t.Fatalf("without the pane arguments, planImport = %+v, want one new tab", steps)
	}
}

// TestStateRoundTrip checks that save and loadState keep every record.
func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	rapid.Check(t, func(rt *rapid.T) {
		tabs := rapid.MapOf(rapid.String(), rapid.Custom(func(t *rapid.T) importedTab {
			return importedTab{Tab: rapid.String().Draw(t, "tab"), Pane: rapid.String().Draw(t, "pane")}
		})).Draw(rt, "tabs")
		path := filepath.Join(dir, "sub", "state.json")
		if err := (state{Version: 1, Tabs: tabs}).save(path); err != nil {
			rt.Fatalf("save: %v", err)
		}
		got, err := loadState(path)
		if err != nil {
			rt.Fatalf("loadState: %v", err)
		}
		if len(tabs) == 0 && len(got.Tabs) == 0 {
			return
		}
		if !reflect.DeepEqual(got.Tabs, tabs) {
			rt.Fatalf("loadState = %v, want %v", got.Tabs, tabs)
		}
	})
}

func TestLoadStateMissingFile(t *testing.T) {
	st, err := loadState(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil || st.Tabs == nil || len(st.Tabs) != 0 {
		t.Fatalf("loadState(missing) = %+v, %v", st, err)
	}
}

func TestFindSession(t *testing.T) {
	sessions := []session.Session{{ID: "aaaaaaaa-1"}, {ID: "aaaaaaaa-2"}, {ID: "bbbbbbbb-1"}}
	if s, err := findSession(sessions, "bbbbbbbb"); err != nil || s.ID != "bbbbbbbb-1" {
		t.Fatalf("findSession(unique prefix) = %v, %v", s, err)
	}
	if _, err := findSession(sessions, "aaaaaaaa"); err == nil {
		t.Fatal("findSession(ambiguous prefix) gave no error")
	}
	if _, err := findSession(sessions, "bbb"); err == nil {
		t.Fatal("findSession(short prefix) gave no error")
	}
	if s, err := findSession(sessions, "aaaaaaaa-2"); err != nil || s.ID != "aaaaaaaa-2" {
		t.Fatalf("findSession(exact) = %v, %v", s, err)
	}
}

func TestResumeCommandRemote(t *testing.T) {
	t.Setenv(remoteEnv, "ssh -t -p 2201 jose@127.0.0.1 /home/jose/.local/bin/anyresume")
	got, err := resumeCommand("0123abcd-0000-0000-0000-000000000000")
	if err != nil {
		t.Fatal(err)
	}
	want := "ssh -t -p 2201 jose@127.0.0.1 /home/jose/.local/bin/anyresume resume 0123abcd-0000-0000-0000-000000000000"
	if got != want {
		t.Fatalf("resumeCommand = %q, want %q", got, want)
	}
}
