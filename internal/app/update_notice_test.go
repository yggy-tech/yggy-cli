package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type countingUpdater struct {
	fakeUpdater
	lookups *int
}

func (c countingUpdater) Latest(ctx context.Context) (string, error) {
	*c.lookups++
	return c.fakeUpdater.Latest(ctx)
}

func noticeApp(t *testing.T, latest string, interactive bool, input string) (*App, *[]string, *[][]string, *int) {
	t.Helper()
	installed, restarted, lookups := &[]string{}, &[][]string{}, new(int)
	a, _, _ := updateApp("0.3.0", countingUpdater{fakeUpdater{latest: latest, installed: installed}, lookups})
	a.ConfigPath = filepath.Join(t.TempDir(), "config.json")
	a.Interactive = interactive
	a.In = strings.NewReader(input)
	a.Restart = func(exe string, args []string) int {
		*restarted = append(*restarted, append([]string{exe}, args...))
		return 7
	}
	return a, installed, restarted, lookups
}

func TestANewerReleaseIsNamedOnAnyCommand(t *testing.T) {
	a, installed, _, _ := noticeApp(t, "0.4.0", false, "")
	a.Run([]string{"context"})
	if !strings.Contains(a.Err.(interface{ String() string }).String(), "yggy 0.4.0 is available (you have 0.3.0). Run yggy update") {
		t.Fatalf("stderr %q", a.Err.(interface{ String() string }).String())
	}
	if len(*installed) != 0 {
		t.Fatalf("installed %v without a terminal", *installed)
	}
}

func TestInATerminalTheUpdateRunsAndTheCommandRunsAgainOnIt(t *testing.T) {
	a, installed, restarted, _ := noticeApp(t, "0.4.0", true, "\n")
	if code := a.Run([]string{"auth", "login"}); code != 7 {
		t.Fatalf("code %d", code)
	}
	if len(*installed) != 1 || (*installed)[0] != "0.4.0@/usr/local/bin/yggy" {
		t.Fatalf("installed %v", *installed)
	}
	if len(*restarted) != 1 || strings.Join((*restarted)[0], " ") != "/usr/local/bin/yggy auth login" {
		t.Fatalf("restarted %v", *restarted)
	}
}

func TestDecliningTheUpdateRunsTheCommandAsItIs(t *testing.T) {
	a, installed, restarted, _ := noticeApp(t, "0.4.0", true, "n\n")
	a.Run([]string{"context"})
	if len(*installed) != 0 || len(*restarted) != 0 {
		t.Fatalf("installed %v restarted %v", *installed, *restarted)
	}
}

func TestTheReleaseIsLookedUpOncePerDay(t *testing.T) {
	a, _, _, lookups := noticeApp(t, "0.3.0", false, "")
	day := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	a.Now = func() time.Time { return day }
	a.Run([]string{"context"})
	a.Run([]string{"context"})
	if *lookups != 1 {
		t.Fatalf("lookups %d, want 1", *lookups)
	}
	day = day.Add(25 * time.Hour)
	a.Run([]string{"context"})
	if *lookups != 2 {
		t.Fatalf("lookups %d after a day, want 2", *lookups)
	}
}

func TestNoNoticeOnUpdateVersionOrInCI(t *testing.T) {
	a, _, _, lookups := noticeApp(t, "0.4.0", false, "")
	a.Run([]string{"version"})
	a.Env = func(key string) string {
		if key == "CI" {
			return "true"
		}
		return ""
	}
	a.Run([]string{"context"})
	if *lookups != 0 {
		t.Fatalf("lookups %d", *lookups)
	}
}
