//go:build portly_evidence

package stopper

import (
	"testing"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// TestEvidenceBuildActsOnlyOnItsOwnChildren keeps this machine safe.
func TestEvidenceBuildActsOnlyOnItsOwnChildren(t *testing.T) {
	snapshot := world()
	change(&snapshot, shellPID, func(p *domain.Process) { p.Exe = "/tmp/go-build/e2e.test" })
	child := candidate{target: domain.StopTarget{Kind: domain.KindProcess, Root: snapshot.Processes[appPID].ProcessID}}
	if !startedByTest(snapshot, child) {
		t.Error("a child of the launching process is allowed")
	}
	change(&snapshot, shellPID, func(p *domain.Process) { p.Exe = `C:\Temp\go-build\e2e.test.exe` })
	if !startedByTest(snapshot, child) {
		t.Error("a child of a Windows test binary is allowed")
	}
	change(&snapshot, shellPID, func(p *domain.Process) { p.Exe = `C:\Windows\System32\notepad.exe` })
	if startedByTest(snapshot, child) {
		t.Error("a child of another Windows program was allowed")
	}
	change(&snapshot, shellPID, func(p *domain.Process) { p.Exe = "/tmp/go-build/e2e.test" })
	stranger := developer(700, 1)
	add(&snapshot, stranger)
	if startedByTest(snapshot, candidate{target: domain.StopTarget{Kind: domain.KindProcess, Root: stranger.ProcessID}}) {
		t.Error("a stranger was allowed")
	}
	for name, edit := range map[string]func(*domain.Process){
		"a shell":        func(p *domain.Process) { p.Exe = "/bin/bash" },
		"a user manager": func(p *domain.Process) { p.UserManager = true },
	} {
		changed := world()
		change(&changed, shellPID, func(p *domain.Process) { p.Exe = "/tmp/go-build/e2e.test"; edit(p) })
		if startedByTest(changed, child) {
			t.Errorf("launched by %s", name)
		}
	}
	change(&snapshot, 500, func(p *domain.Process) { p.PPID = 1 })
	if startedByTest(snapshot, child) {
		t.Error("everything descends from PID 1")
	}
}

// TestEvidenceBuildNeedsTheTestLabel keeps other containers safe.
func TestEvidenceBuildNeedsTheTestLabel(t *testing.T) {
	target := domain.StopTarget{Kind: domain.KindContainer}
	for label, want := range map[string]bool{testProject: true, "": false, "pbrms": false} {
		got := startedByTest(domain.Snapshot{}, candidate{target: target, container: domain.Container{ComposeProject: label}})
		if got != want {
			t.Errorf("%q: %v", label, got)
		}
	}
}
