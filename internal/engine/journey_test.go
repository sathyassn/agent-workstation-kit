package engine_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/engine"
	"github.com/sathyassn/agent-workstation-kit/internal/platform"
	"github.com/sathyassn/agent-workstation-kit/internal/platform/platformtest"
)

const (
	envJourneyDir    = "WSKIT_TEST_JOURNEY_DIR"
	journeyHostFile  = "hostname-state.json"
	journeyKilled    = "journey-killed"
	journeyOrigFile  = "original-journey-content\n"
	journeyNewFile   = "mutated-journey-content\n"
	journeyOrigHost  = "lab-ws-orig"
	journeyNewHost   = "lab-ws-mutated"
	journeyOrigLabel = "Lab Journey Original"
	journeyNewLabel  = "Lab Journey Mutated"
	journeyOrigMode  = os.FileMode(0o640)
)

// journeyUnit changes both a file (carrying xattrs and an ACL before the
// apply) and the machine hostname through tx.SetState. When requireOriginal
// is set, Apply verifies that both the file (including xattr and ACL) and the
// hostname match their original values before mutating, proving that Resume
// restored both before reapplying.
type journeyUnit struct {
	t               *testing.T
	root            string
	hostProv        contract.StateProvider
	requireOriginal bool
	wantXattr       []byte
	wantACL         string
}

func (u *journeyUnit) ID() contract.UnitID { return contract.UnitMachineIdentity }

func (u *journeyUnit) Describe(context.Context) (contract.Description, error) {
	return contract.Description{
		Unit:           contract.UnitMachineIdentity,
		Scope:          contract.ScopeRoot,
		OwnedPaths:     []string{pathA},
		OwnedStateKeys: []contract.StateKey{platform.HostnameKeyStatic, platform.HostnameKeyPretty},
	}, nil
}

func journeyPlan() contract.Plan {
	return contract.Plan{ID: "plan-journey", Unit: contract.UnitMachineIdentity, Digest: "digest-journey"}
}

func (u *journeyUnit) Plan(context.Context, contract.Inputs) (contract.Plan, error) {
	return journeyPlan(), nil
}

func (u *journeyUnit) Apply(ctx context.Context, tx contract.Tx, _ contract.Plan) error {
	if u.requireOriginal {
		target := filepath.Join(u.root, pathA)
		data, err := os.ReadFile(target) //nolint:gosec // test temp root
		if err != nil || string(data) != journeyOrigFile {
			return errNotRestored
		}
		if gotXA, ok := getXattrValue(u.t, target, xattrName()); !ok || !bytes.Equal(gotXA, u.wantXattr) {
			return errNotRestored
		}
		if gotACL := readACLText(u.t, target); gotACL != u.wantACL {
			return errNotRestored
		}
		gotHost, err := u.hostProv.Read(ctx, platform.HostnameKeyStatic)
		if err != nil || gotHost.Value != journeyOrigHost {
			return errNotRestored
		}
	}
	owner := contract.Owner{UID: os.Getuid(), GID: os.Getgid()}
	if err := tx.WriteFile(ctx, pathA, []byte(journeyNewFile), 0o600, owner); err != nil {
		return err
	}
	if err := tx.SetState(ctx, u.hostProv, platform.HostnameKeyStatic, contract.State{Value: journeyNewHost}); err != nil {
		return err
	}
	return tx.SetState(ctx, u.hostProv, platform.HostnameKeyPretty, contract.State{Value: journeyNewLabel})
}

func (u *journeyUnit) Verify(ctx context.Context, _ contract.Plan) (contract.Result, error) {
	data, err := os.ReadFile(filepath.Join(u.root, pathA)) //nolint:gosec // test temp root
	if err != nil || string(data) != journeyNewFile {
		return contract.Result{Unit: contract.UnitMachineIdentity, Outcome: contract.OutcomeFailed}, nil
	}
	gotHost, err := u.hostProv.Read(ctx, platform.HostnameKeyStatic)
	if err != nil || gotHost.Value != journeyNewHost {
		return contract.Result{Unit: contract.UnitMachineIdentity, Outcome: contract.OutcomeFailed}, nil
	}
	return contract.Result{Unit: contract.UnitMachineIdentity, Outcome: contract.OutcomeVerified}, nil
}

func (u *journeyUnit) Revert(context.Context, contract.Tx, contract.Receipt) error { return nil }
func (u *journeyUnit) Status(context.Context) (contract.Status, error) {
	return contract.Status{Unit: contract.UnitMachineIdentity}, nil
}

// TestHelperJourneyChild is the child process for the AC-5 kill-and-resume
// journey test. It runs Apply and SIGKILLs itself at PointApplied (immediately
// after mutating both the file and the hostname).
func TestHelperJourneyChild(t *testing.T) {
	dir := os.Getenv(envJourneyDir)
	if dir == "" {
		t.Skip("child process of the AC-5 journey harness")
	}
	w := worldAt(dir)
	runner, err := platformtest.NewFileHostnameRunner(filepath.Join(dir, journeyHostFile), platformtest.HostnameState{})
	if err != nil {
		t.Fatal(err)
	}
	hostProv := newHostnameProvider(runner)
	providers := map[contract.StateProviderName]contract.StateProvider{
		contract.ProviderHostname: hostProv,
	}
	e, _ := w.engine(t, func(o *engine.Options) {
		o.Providers = providers
		o.Fault = func(p engine.Point) {
			if p == engine.PointApplied {
				_ = os.WriteFile(filepath.Join(dir, journeyKilled), []byte(p), 0o600) //nolint:gosec // parent test temp dir
				_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
				select {}
			}
		}
	})
	u := &journeyUnit{root: w.root, hostProv: hostProv}
	_ = e.Apply(context.Background(), u, journeyPlan())
	t.Fatal("child process did not hit PointApplied SIGKILL")
}

// TestJourneyKillAndResumeRestoresFileWithACLAndXattrAndHostname is AC-5:
// a unit changes a file with ACLs and extended attributes and a hostname
// value, the run is SIGKILLed after both changes, and the next start restores
// both exactly.
func TestJourneyKillAndResumeRestoresFileWithACLAndXattrAndHostname(t *testing.T) {
	for _, restoreOnly := range []bool{true, false} {
		name := "restore-only"
		if !restoreOnly {
			name = "restore-and-reapply"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			w := newWorld(t)
			target := filepath.Join(w.root, pathA)
			if err := os.WriteFile(target, []byte(journeyOrigFile), journeyOrigMode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(target, journeyOrigMode); err != nil {
				t.Fatal(err)
			}
			wantXattr := []byte("journey-xattr-payload")
			setXattrValue(t, target, xattrName(), wantXattr)
			setACL(t, target)
			wantACL := readACLText(t, target)
			if wantACL == "" {
				t.Fatal("ACL fixture did not attach an ACL")
			}

			hostFile := filepath.Join(w.dir, journeyHostFile)
			runner, err := platformtest.NewFileHostnameRunner(hostFile, platformtest.HostnameState{
				Static:    journeyOrigHost,
				Pretty:    journeyOrigLabel,
				Transient: journeyOrigHost,
				Local:     journeyOrigHost,
			})
			if err != nil {
				t.Fatal(err)
			}

			// Spawn the child process that mutates both file and hostname then SIGKILLs itself.
			// #nosec G204,G702 -- re-runs this test binary.
			cmd := exec.Command(os.Args[0], "-test.run=^TestHelperJourneyChild$", "-test.count=1")
			cmd.Env = append(os.Environ(), envJourneyDir+"="+w.dir)
			out, runErr := cmd.CombinedOutput()
			var exitErr *exec.ExitError
			if !errors.As(runErr, &exitErr) {
				t.Fatalf("child did not exit with signal: %v\n%s", runErr, out)
			}
			ws, ok := exitErr.Sys().(syscall.WaitStatus)
			if !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL { //nolint:misspell // Go API name
				t.Fatalf("child did not die by SIGKILL: %v\n%s", runErr, out)
			}
			if killedAt, err := os.ReadFile(filepath.Join(w.dir, journeyKilled)); err != nil || engine.Point(killedAt) != engine.PointApplied { //nolint:gosec // test temp dir
				t.Fatalf("child killed marker = %q, %v; want %s", killedAt, err, engine.PointApplied)
			}

			// Prove both the file and hostname were mutated before the kill.
			if gotData, err := os.ReadFile(target); err != nil || string(gotData) != journeyNewFile { //nolint:gosec // test temp root
				t.Fatalf("file after kill = %q, %v; want %q", gotData, err, journeyNewFile)
			}
			if _, hasXA := getXattrValue(t, target, xattrName()); hasXA {
				t.Fatal("xattr was still present after WriteFile before kill")
			}
			if gotACL := readACLText(t, target); gotACL != "" {
				t.Fatalf("ACL was still present after WriteFile before kill: %q", gotACL)
			}
			hostProv := newHostnameProvider(runner)
			if gotHost, err := hostProv.Read(ctx, platform.HostnameKeyStatic); err != nil || gotHost.Value != journeyNewHost {
				t.Fatalf("hostname after kill = %+v, %v; want %q", gotHost, err, journeyNewHost)
			}

			// Next start: resume in a fresh engine.
			providers := map[contract.StateProviderName]contract.StateProvider{
				contract.ProviderHostname: hostProv,
			}
			e, _ := w.engine(t, func(o *engine.Options) { o.Providers = providers })
			u := &journeyUnit{
				t:               t,
				root:            w.root,
				hostProv:        hostProv,
				requireOriginal: true,
				wantXattr:       wantXattr,
				wantACL:         wantACL,
			}
			resolve := func(context.Context, contract.UnitID) (contract.Unit, contract.Plan, error) {
				return u, journeyPlan(), nil
			}
			res, err := e.Resume(ctx, resolve, engine.ResumeOptions{RestoreOnly: restoreOnly})
			if err != nil {
				t.Fatalf("Resume(RestoreOnly=%v): %v", restoreOnly, err)
			}

			if restoreOnly {
				if res.Action != engine.ResumeRestored {
					t.Fatalf("Resume action = %s, want %s", res.Action, engine.ResumeRestored)
				}
				// Verify exact file content, mode, xattr, ACL and hostname restored.
				gotData, err := os.ReadFile(target) //nolint:gosec // test temp root
				if err != nil || string(gotData) != journeyOrigFile {
					t.Fatalf("restored file content = %q, %v; want %q", gotData, err, journeyOrigFile)
				}
				info, err := os.Stat(target)
				if err != nil || info.Mode().Perm() != journeyOrigMode {
					t.Fatalf("restored file mode = %v, %v; want %v", info.Mode().Perm(), err, journeyOrigMode)
				}
				gotXA, hasXA := getXattrValue(t, target, xattrName())
				if !hasXA || !bytes.Equal(gotXA, wantXattr) {
					t.Fatalf("restored xattr = %q (ok=%v), want %q", gotXA, hasXA, wantXattr)
				}
				if gotACL := readACLText(t, target); gotACL != wantACL {
					t.Fatalf("restored ACL =\n%q\nwant:\n%q", gotACL, wantACL)
				}
				gotStatic, err := hostProv.Read(ctx, platform.HostnameKeyStatic)
				if err != nil || gotStatic.Value != journeyOrigHost {
					t.Fatalf("restored hostname.static = %+v, %v; want %q", gotStatic, err, journeyOrigHost)
				}
				gotPretty, err := hostProv.Read(ctx, platform.HostnameKeyPretty)
				if err != nil || gotPretty.Value != journeyOrigLabel {
					t.Fatalf("restored hostname.pretty = %+v, %v; want %q", gotPretty, err, journeyOrigLabel)
				}
				return
			}

			if res.Action != engine.ResumeReapplied {
				t.Fatalf("Resume action = %s, want %s", res.Action, engine.ResumeReapplied)
			}
			if gotData, err := os.ReadFile(target); err != nil || string(gotData) != journeyNewFile { //nolint:gosec // test temp root
				t.Fatalf("reapplied file = %q, %v; want %q", gotData, err, journeyNewFile)
			}
			if gotStatic, err := hostProv.Read(ctx, platform.HostnameKeyStatic); err != nil || gotStatic.Value != journeyNewHost {
				t.Fatalf("reapplied hostname.static = %+v, %v; want %q", gotStatic, err, journeyNewHost)
			}
		})
	}
}
