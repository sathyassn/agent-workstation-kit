package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
	"github.com/sathyassn/agent-workstation-kit/internal/contract/contracttest"
	"github.com/sathyassn/agent-workstation-kit/internal/engine"
	"github.com/sathyassn/agent-workstation-kit/internal/platform"
	"github.com/sathyassn/agent-workstation-kit/internal/platform/darwin"
	"github.com/sathyassn/agent-workstation-kit/internal/platform/linux"
	"github.com/sathyassn/agent-workstation-kit/internal/platform/platformtest"
)

func newHostnameProvider(runner platform.CommandRunner) contract.StateProvider {
	if runtime.GOOS == "darwin" {
		return &darwin.HostnameProvider{Runner: runner}
	}
	return &linux.HostnameProvider{Runner: runner}
}

type fileAndStateUnit struct {
	root        string
	hostProv    contract.StateProvider
	newHostname string
	failAfter   error
	verifyOut   contract.Outcome
}

func (u *fileAndStateUnit) ID() contract.UnitID { return contract.UnitMachineIdentity }

func (u *fileAndStateUnit) Describe(context.Context) (contract.Description, error) {
	return contract.Description{
		Unit:           contract.UnitMachineIdentity,
		Scope:          contract.ScopeRoot,
		OwnedPaths:     []string{pathA},
		OwnedStateKeys: []contract.StateKey{platform.HostnameKeyStatic, platform.HostnameKeyPretty},
	}, nil
}

func statePlan() contract.Plan {
	return contract.Plan{ID: "plan-state", Unit: contract.UnitMachineIdentity, Digest: "digest-state"}
}

func (u *fileAndStateUnit) Plan(context.Context, contract.Inputs) (contract.Plan, error) {
	return statePlan(), nil
}

func (u *fileAndStateUnit) Apply(ctx context.Context, tx contract.Tx, _ contract.Plan) error {
	owner := contract.Owner{UID: os.Getuid(), GID: os.Getgid()}
	if err := tx.WriteFile(ctx, pathA, []byte("mutated-a\n"), 0o644, owner); err != nil {
		return err
	}
	if err := tx.SetState(ctx, u.hostProv, platform.HostnameKeyStatic, contract.State{Value: u.newHostname}); err != nil {
		return err
	}
	if err := tx.SetState(ctx, u.hostProv, platform.HostnameKeyPretty, contract.State{Value: "Mutated Lab Host"}); err != nil {
		return err
	}
	return u.failAfter
}

func (u *fileAndStateUnit) Verify(context.Context, contract.Plan) (contract.Result, error) {
	out := u.verifyOut
	if out == "" {
		out = contract.OutcomeVerified
	}
	return contract.Result{Unit: contract.UnitMachineIdentity, Outcome: out}, nil
}

func (u *fileAndStateUnit) Revert(context.Context, contract.Tx, contract.Receipt) error { return nil }
func (u *fileAndStateUnit) Status(context.Context) (contract.Status, error) {
	return contract.Status{Unit: contract.UnitMachineIdentity}, nil
}

// TestRestoreReturnsFilesAndStateToManifest is AC-1: a failed apply or an
// interrupted apply restores both declared files and provider state to the
// manifest.
func TestRestoreReturnsFilesAndStateToManifest(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	runner := platformtest.NewHostnameRunner(platformtest.HostnameState{
		Static:    "lab-ws-orig",
		Pretty:    "Lab Original",
		Transient: "lab-ws-orig",
		Local:     "lab-ws-orig",
	})
	hostProv := newHostnameProvider(runner)
	providers := map[contract.StateProviderName]contract.StateProvider{
		contract.ProviderHostname: hostProv,
	}

	e, _ := w.engine(t, func(o *engine.Options) { o.Providers = providers })
	failErr := errors.New("post-state failure")
	u := &fileAndStateUnit{
		root:        w.root,
		hostProv:    hostProv,
		newHostname: "lab-ws-mutated",
		failAfter:   failErr,
	}

	err := e.Apply(ctx, u, statePlan())
	if !errors.Is(err, engine.ErrApplyFailed) || !errors.Is(err, failErr) {
		t.Fatalf("Apply err = %v, want ErrApplyFailed wrapping %v", err, failErr)
	}
	w.assertState(t, originalState(), "files after failed state apply")

	gotStatic, err := hostProv.Read(ctx, platform.HostnameKeyStatic)
	if err != nil || gotStatic.Value != "lab-ws-orig" {
		t.Fatalf("hostname.static after restore = %+v, %v; want lab-ws-orig", gotStatic, err)
	}
	gotPretty, err := hostProv.Read(ctx, platform.HostnameKeyPretty)
	if err != nil || gotPretty.Value != "Lab Original" {
		t.Fatalf("hostname.pretty after restore = %+v, %v; want Lab Original", gotPretty, err)
	}
}

// TestRestoreValidatorsAndMismatchesFailClosed covers Part 4:
// - tampered file backup -> ErrBackupMismatch, exit 4, intent unfinished
// - tampered state backup -> ErrBackupMismatch, exit 4, intent unfinished
// - state provider that fails to restore -> ErrRestoreMismatch, exit 4, intent unfinished
// - missing provider during resume -> ErrRestoreMismatch, exit 4, intent unfinished
// - failing restore validator -> ErrRestoreValidation, exit 4, intent unfinished
func TestRestoreValidatorsAndMismatchesFailClosed(t *testing.T) {
	ctx := context.Background()

	t.Run("tampered file backup fails closed exit 4", func(t *testing.T) {
		w := newWorld(t)
		u := &testUnit{root: w.root}
		w.abandon(t, engine.PointMutation, u)
		rec := w.record(t)
		// Corrupt the backed-up file 0 (pathA) on disk.
		if err := os.WriteFile(filepath.Join(w.journal, "backups", string(rec.RunID), "0"), []byte("corrupted"), 0o600); err != nil {
			t.Fatal(err)
		}
		e, _ := w.engine(t)
		_, err := e.Resume(ctx, resolverFor(u), engine.ResumeOptions{RestoreOnly: true})
		if !errors.Is(err, engine.ErrBackupMismatch) || engine.ExitCode(err) != contract.ExitRecoveryBlocks {
			t.Fatalf("Resume err = %v (exit %d), want ErrBackupMismatch and exit 4", err, engine.ExitCode(err))
		}
		if r := w.record(t); r == nil || r.Status != engine.StatusRestoring {
			t.Fatalf("record = %+v, want StatusRestoring (unfinished)", r)
		}
		if rc := w.receipt(t, rec.RunID); rc != nil {
			t.Fatalf("receipt written despite failed restore: %+v", rc)
		}
	})

	t.Run("tampered state backup fails closed exit 4", func(t *testing.T) {
		w := newWorld(t)
		runner := platformtest.NewHostnameRunner(platformtest.HostnameState{
			Static: "lab-ws-orig", Pretty: "Lab Orig", Transient: "lab-ws-orig", Local: "lab-ws-orig",
		})
		hostProv := newHostnameProvider(runner)
		u := &fileAndStateUnit{root: w.root, hostProv: hostProv, newHostname: "lab-ws-mutated"}
		providers := map[contract.StateProviderName]contract.StateProvider{contract.ProviderHostname: hostProv}
		eCrash, self := w.engine(t, func(o *engine.Options) {
			o.Providers = providers
			o.Fault = crashAt(engine.PointApplied)
		})
		if !runCrashing(func() { _ = eCrash.Apply(ctx, u, statePlan()) }) {
			t.Fatal("apply did not reach PointApplied")
		}
		w.table.Kill(self)

		rec := w.record(t)
		if err := os.WriteFile(filepath.Join(w.journal, "backups", string(rec.RunID), "state-0"), []byte(`{"version":1,"static":"tampered"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		e, _ := w.engine(t, func(o *engine.Options) { o.Providers = providers })
		resolve := func(context.Context, contract.UnitID) (contract.Unit, contract.Plan, error) {
			return u, statePlan(), nil
		}
		_, err := e.Resume(ctx, resolve, engine.ResumeOptions{RestoreOnly: true})
		if !errors.Is(err, engine.ErrBackupMismatch) || engine.ExitCode(err) != contract.ExitRecoveryBlocks {
			t.Fatalf("Resume err = %v (exit %d), want ErrBackupMismatch and exit 4", err, engine.ExitCode(err))
		}
		if r := w.record(t); r == nil || r.Status != engine.StatusRestoring {
			t.Fatalf("record = %+v, want StatusRestoring", r)
		}
	})

	t.Run("missing state provider on resume fails closed exit 4", func(t *testing.T) {
		w := newWorld(t)
		runner := platformtest.NewHostnameRunner(platformtest.HostnameState{
			Static: "lab-ws-orig", Pretty: "Lab Orig", Transient: "lab-ws-orig", Local: "lab-ws-orig",
		})
		hostProv := newHostnameProvider(runner)
		u := &fileAndStateUnit{root: w.root, hostProv: hostProv, newHostname: "lab-ws-mutated"}
		providers := map[contract.StateProviderName]contract.StateProvider{contract.ProviderHostname: hostProv}
		eCrash, self := w.engine(t, func(o *engine.Options) {
			o.Providers = providers
			o.Fault = crashAt(engine.PointApplied)
		})
		if !runCrashing(func() { _ = eCrash.Apply(ctx, u, statePlan()) }) {
			t.Fatal("apply did not reach PointApplied")
		}
		w.table.Kill(self)

		// Resume with no Providers registered.
		e, _ := w.engine(t)
		resolve := func(context.Context, contract.UnitID) (contract.Unit, contract.Plan, error) {
			return u, statePlan(), nil
		}
		_, err := e.Resume(ctx, resolve, engine.ResumeOptions{RestoreOnly: true})
		if !errors.Is(err, engine.ErrRestoreMismatch) || engine.ExitCode(err) != contract.ExitRecoveryBlocks {
			t.Fatalf("Resume err = %v (exit %d), want ErrRestoreMismatch and exit 4", err, engine.ExitCode(err))
		}
		if r := w.record(t); r == nil || r.Status != engine.StatusRestoring {
			t.Fatalf("record = %+v, want StatusRestoring", r)
		}
	})

	t.Run("state provider silent no-op restore fails verification exit 4", func(t *testing.T) {
		w := newWorld(t)
		inner := contracttest.NewFakeStateProvider(contract.ProviderHostname, map[contract.StateKey]contract.State{
			platform.HostnameKeyStatic: {Value: "lab-ws-orig"},
			platform.HostnameKeyPretty: {Value: "Lab Orig"},
		})
		broken := &silentNoOpRestoreProvider{FakeStateProvider: inner}
		providers := map[contract.StateProviderName]contract.StateProvider{contract.ProviderHostname: broken}
		e, _ := w.engine(t, func(o *engine.Options) { o.Providers = providers })
		u := &fileAndStateUnit{root: w.root, hostProv: broken, newHostname: "lab-ws-mutated", failAfter: errors.New("boom")}
		err := e.Apply(ctx, u, statePlan())
		if !errors.Is(err, engine.ErrRestoreMismatch) || engine.ExitCode(err) != contract.ExitRecoveryBlocks {
			t.Fatalf("Apply err = %v (exit %d), want ErrRestoreMismatch and exit 4", err, engine.ExitCode(err))
		}
		if r := w.record(t); r == nil || r.Status != engine.StatusRestoring {
			t.Fatalf("record = %+v, want StatusRestoring", r)
		}
	})

	t.Run("failing restore validator fails closed exit 4 then succeeding validator completes", func(t *testing.T) {
		w := newWorld(t)
		valErr := errors.New("sshd -t failed")
		failVal := true
		validator := engine.NewValidator("sshd-test", func(context.Context) error {
			if failVal {
				return valErr
			}
			return nil
		})
		e, self1 := w.engine(t, func(o *engine.Options) { o.Validators = []engine.RestoreValidator{validator} })
		u := &testUnit{root: w.root, afterRun: func(context.Context) error { return errNotRestored }}
		err := e.Apply(ctx, u, testPlan())
		if !errors.Is(err, engine.ErrRestoreValidation) || !errors.Is(err, valErr) || engine.ExitCode(err) != contract.ExitRecoveryBlocks {
			t.Fatalf("Apply err = %v (exit %d), want ErrRestoreValidation wrapping %v and exit 4", err, engine.ExitCode(err), valErr)
		}
		if r := w.record(t); r == nil || r.Status != engine.StatusRestoring {
			t.Fatalf("record = %+v, want StatusRestoring (unfinished)", r)
		}
		// The first wskit process exited with code 4.
		w.table.Kill(self1)

		// While intent is unfinished, a fresh Apply is blocked with exit 4.
		e2, _ := w.engine(t, func(o *engine.Options) { o.Validators = []engine.RestoreValidator{validator} })
		if err := e2.Apply(ctx, &testUnit{root: w.root}, testPlan()); !errors.Is(err, engine.ErrRecoveryPending) || engine.ExitCode(err) != contract.ExitRecoveryBlocks {
			t.Fatalf("new Apply while restoring = %v (exit %d), want ErrRecoveryPending and exit 4", err, engine.ExitCode(err))
		}

		// Once the validator passes, Resume finishes the restore and closes the intent.
		failVal = false
		res, err := e2.Resume(ctx, resolverFor(&testUnit{root: w.root}), engine.ResumeOptions{RestoreOnly: true})
		if err != nil || res.Action != engine.ResumeRestored {
			t.Fatalf("Resume after validator fixed = %+v, %v", res, err)
		}
		if r := w.record(t); r == nil || r.Status != engine.StatusIdle {
			t.Fatalf("record after successful validator = %+v, want StatusIdle", r)
		}
	})
}

type silentNoOpRestoreProvider struct {
	*contracttest.FakeStateProvider
}

func (p *silentNoOpRestoreProvider) Restore(_ context.Context, _ contract.Snapshot) error {
	return nil
}

type packageDBUnit struct {
	path string
}

func (u packageDBUnit) ID() contract.UnitID { return contract.UnitPackages }
func (u packageDBUnit) Describe(context.Context) (contract.Description, error) {
	return contract.Description{
		Unit:       contract.UnitPackages,
		Scope:      contract.ScopeRoot,
		OwnedPaths: []string{u.path},
	}, nil
}
func (u packageDBUnit) Plan(context.Context, contract.Inputs) (contract.Plan, error) {
	return contract.Plan{ID: "plan-pkgdb", Unit: contract.UnitPackages, Digest: "digest-pkgdb"}, nil
}
func (u packageDBUnit) Apply(ctx context.Context, tx contract.Tx, _ contract.Plan) error {
	return tx.WriteFile(ctx, u.path, []byte("overwritten"), 0o600, contract.Owner{UID: os.Getuid(), GID: os.Getgid()})
}
func (u packageDBUnit) Verify(context.Context, contract.Plan) (contract.Result, error) {
	return contract.Result{Unit: contract.UnitPackages, Outcome: contract.OutcomeVerified}, nil
}
func (u packageDBUnit) Revert(context.Context, contract.Tx, contract.Receipt) error { return nil }
func (u packageDBUnit) Status(context.Context) (contract.Status, error) {
	return contract.Status{Unit: contract.UnitPackages}, nil
}

// TestRecoveryNeverRestoresPackageDBFiles is AC-2:
//  1. Declaring any package-database path in OwnedPaths is refused before
//     backup or intent (exit 2).
//  2. Even if a manifest on disk contains a package-database path, restore
//     refuses to write or unlink it, fails closed (exit 4, intent unfinished),
//     and leaves the database file byte-for-byte untouched.
//  3. Package mutations via tx.Package are undone exclusively through the
//     package-manager StateProvider's Restore.
func TestRecoveryNeverRestoresPackageDBFiles(t *testing.T) {
	ctx := context.Background()

	forbiddenPaths := []string{
		"/var/lib/dpkg/status",
		"/var/lib/dpkg/info/zsh.list",
		"/var/lib/apt/extended_states",
		"/var/cache/apt/pkgcache.bin",
		"/var/cache/debconf/config.dat",
		"/var/lib/ucf/hashfile",
		"/var/log/apt/history.log",
		"/opt/homebrew/Cellar/zsh/5.9/bin/zsh",
		"/opt/homebrew/Caskroom/app/1.0/app",
		"/opt/homebrew/var/homebrew/locks/update",
		"/usr/local/Cellar/zsh/5.9/bin/zsh",
		"/var/db/receipts/com.example.pkg.bom",
		"/private/var/db/receipts/com.example.pkg.bom",
		"/Library/Receipts/example.bom",
		"/Library/Apple/System/Library/Receipts/com.apple.pkg.bom",
		"/System/Volumes/Data/opt/homebrew/Cellar/zsh/5.9/bin/zsh",
		"/System/Volumes/Data/private/var/db/receipts/com.example.pkg.bom",
	}
	if runtime.GOOS == "darwin" {
		forbiddenPaths = append(forbiddenPaths,
			"/opt/homebrew/cellar/zsh/5.9/bin/zsh",
			"/OPT/HOMEBREW/Caskroom/app/1.0/app",
			"/system/volumes/data/opt/homebrew/cellar/zsh/5.9/bin/zsh",
			"/library/apple/system/library/receipts/com.apple.pkg.bom",
		)
	}

	for _, p := range forbiddenPaths {
		t.Run("apply refuses "+p, func(t *testing.T) {
			w := newWorld(t)
			realPath := filepath.Join(w.root, p)
			if err := os.MkdirAll(filepath.Dir(realPath), 0o750); err != nil {
				t.Fatal(err)
			}
			const orig = "package-db-original\n"
			if err := os.WriteFile(realPath, []byte(orig), 0o600); err != nil {
				t.Fatal(err)
			}
			e, _ := w.engine(t)
			u := packageDBUnit{path: p}
			plan, _ := u.Plan(ctx, contract.Inputs{})
			err := e.Apply(ctx, u, plan)
			if !errors.Is(err, engine.ErrPackageDBPath) || engine.ExitCode(err) != contract.ExitRefused {
				t.Fatalf("Apply(%q) err = %v (exit %d), want ErrPackageDBPath and exit 2", p, err, engine.ExitCode(err))
			}
			if r := w.record(t); r != nil {
				t.Fatalf("intent written for forbidden package-db path: %+v", r)
			}
			got, err := os.ReadFile(realPath) //nolint:gosec // test temp root
			if err != nil || string(got) != orig {
				t.Fatalf("package-db file changed: %q, %v", got, err)
			}
		})
	}

	t.Run("restore refuses manifest containing package-db path", func(t *testing.T) {
		w := newWorld(t)
		u := &testUnit{root: w.root}
		w.abandon(t, engine.PointMutation, u)
		rec := w.record(t)

		pkgDBPath := "/var/lib/dpkg/status"
		realDB := filepath.Join(w.root, pkgDBPath)
		if err := os.MkdirAll(filepath.Dir(realDB), 0o750); err != nil {
			t.Fatal(err)
		}
		const liveContent = "live-dpkg-status-never-overwrite\n"
		if err := os.WriteFile(realDB, []byte(liveContent), 0o600); err != nil {
			t.Fatal(err)
		}

		// Inject a package-DB entry into the run's manifest on disk.
		mPath := filepath.Join(w.journal, "backups", string(rec.RunID), "manifest.json")
		raw, err := os.ReadFile(mPath) //nolint:gosec // test temp journal
		if err != nil {
			t.Fatal(err)
		}
		var m engine.Manifest
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		m.Entries = append(m.Entries, engine.ManifestEntry{
			Path: pkgDBPath,
			Kind: engine.KindFile,
			Mode: 0o644,
			UID:  os.Getuid(),
			GID:  os.Getgid(),
			Data: "0",
		})
		updated, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(mPath, updated, 0o600); err != nil {
			t.Fatal(err)
		}

		e, _ := w.engine(t)
		_, err = e.Resume(ctx, resolverFor(u), engine.ResumeOptions{RestoreOnly: true})
		if !errors.Is(err, engine.ErrPackageDBPath) || !errors.Is(err, engine.ErrRestoreMismatch) || engine.ExitCode(err) != contract.ExitRecoveryBlocks {
			t.Fatalf("Resume err = %v (exit %d), want ErrPackageDBPath + ErrRestoreMismatch and exit 4", err, engine.ExitCode(err))
		}
		if r := w.record(t); r == nil || r.Status != engine.StatusRestoring {
			t.Fatalf("record = %+v, want StatusRestoring (unfinished)", r)
		}
		got, err := os.ReadFile(realDB) //nolint:gosec // test temp root
		if err != nil || string(got) != liveContent {
			t.Fatalf("dpkg status file was modified during recovery: %q, %v", got, err)
		}
	})

	t.Run("package revert goes through package manager provider only", func(t *testing.T) {
		w := newWorld(t)
		pkgKey := contract.StateKey("package:zsh")
		pkgProv := &recordingPackageProvider{
			FakeStateProvider: contracttest.NewFakeStateProvider(contract.ProviderPackages, map[contract.StateKey]contract.State{
				pkgKey: {Value: `{"name":"zsh","present":false}`},
			}),
		}
		providers := map[contract.StateProviderName]contract.StateProvider{
			contract.ProviderPackages: pkgProv,
		}
		e, _ := w.engine(t, func(o *engine.Options) { o.Providers = providers })
		u := &pkgUnit{pkgName: "zsh", failErr: errors.New("verify will fail")}
		plan := contract.Plan{ID: "plan-pkg", Unit: contract.UnitPackages, Digest: "digest-pkg"}
		err := e.Apply(ctx, u, plan)
		if !errors.Is(err, engine.ErrApplyFailed) {
			t.Fatalf("Apply err = %v, want ErrApplyFailed", err)
		}
		if pkgProv.restores != 1 {
			t.Fatalf("package provider Restore calls = %d, want 1", pkgProv.restores)
		}
		got, err := pkgProv.Read(ctx, pkgKey)
		if err != nil || got.Value != `{"name":"zsh","present":false}` {
			t.Fatalf("package state after restore = %+v, %v; want absent", got, err)
		}
	})
}

type recordingPackageProvider struct {
	*contracttest.FakeStateProvider
	restores int
}

func (p *recordingPackageProvider) Restore(ctx context.Context, s contract.Snapshot) error {
	p.restores++
	return p.FakeStateProvider.Restore(ctx, s)
}

type pkgUnit struct {
	pkgName string
	failErr error
}

func (u *pkgUnit) ID() contract.UnitID { return contract.UnitPackages }
func (u *pkgUnit) Describe(context.Context) (contract.Description, error) {
	return contract.Description{
		Unit:           contract.UnitPackages,
		Scope:          contract.ScopeRoot,
		OwnedStateKeys: []contract.StateKey{contract.StateKey("package:" + u.pkgName)},
	}, nil
}
func (u *pkgUnit) Plan(context.Context, contract.Inputs) (contract.Plan, error) {
	return contract.Plan{ID: "plan-pkg", Unit: contract.UnitPackages, Digest: "digest-pkg"}, nil
}
func (u *pkgUnit) Apply(ctx context.Context, tx contract.Tx, _ contract.Plan) error {
	if err := tx.Package(ctx, contract.PackageState{Name: u.pkgName, Present: true, Version: "5.9"}); err != nil {
		return err
	}
	return u.failErr
}
func (u *pkgUnit) Verify(context.Context, contract.Plan) (contract.Result, error) {
	return contract.Result{Unit: contract.UnitPackages, Outcome: contract.OutcomeVerified}, nil
}
func (u *pkgUnit) Revert(context.Context, contract.Tx, contract.Receipt) error { return nil }
func (u *pkgUnit) Status(context.Context) (contract.Status, error) {
	return contract.Status{Unit: contract.UnitPackages}, nil
}

// TestSetStateRefusesUnregisteredProvider is M2: SetState refuses a provider
// not registered (or a different instance under the same name) before any
// provider call.
func TestSetStateRefusesUnregisteredProvider(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		register bool
	}{
		{"provider not registered", false},
		{"different instance with same name", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			unreg := &countingStateProvider{
				FakeStateProvider: contracttest.NewFakeStateProvider(contract.ProviderHostname, map[contract.StateKey]contract.State{
					platform.HostnameKeyStatic: {Value: "lab-ws-orig"},
				}),
			}
			var providers map[contract.StateProviderName]contract.StateProvider
			if tc.register {
				other := contracttest.NewFakeStateProvider(contract.ProviderHostname, map[contract.StateKey]contract.State{
					platform.HostnameKeyStatic: {Value: "lab-ws-orig"},
				})
				providers = map[contract.StateProviderName]contract.StateProvider{contract.ProviderHostname: other}
			}
			e, _ := w.engine(t, func(o *engine.Options) { o.Providers = providers })
			u := &fileAndStateUnit{root: w.root, hostProv: unreg, newHostname: "lab-ws-new"}
			err := e.Apply(ctx, u, statePlan())
			if !errors.Is(err, engine.ErrUnregisteredProvider) || engine.ExitCode(err) != contract.ExitRefused {
				t.Fatalf("Apply err = %v (exit %d), want ErrUnregisteredProvider (exit 2)", err, engine.ExitCode(err))
			}
			if unreg.snapshots != 0 || unreg.writes != 0 || unreg.reads != 0 {
				t.Fatalf("unregistered provider called: snapshots=%d writes=%d reads=%d; want 0", unreg.snapshots, unreg.writes, unreg.reads)
			}
		})
	}

	t.Run("registered provider recovers cleanly after kill post-Write", func(t *testing.T) {
		w := newWorld(t)
		runner := platformtest.NewHostnameRunner(platformtest.HostnameState{
			Static: "lab-ws-orig", Pretty: "Lab Orig", Transient: "lab-ws-orig", Local: "lab-ws-orig",
		})
		hostProv := newHostnameProvider(runner)
		providers := map[contract.StateProviderName]contract.StateProvider{contract.ProviderHostname: hostProv}
		u := &fileAndStateUnit{root: w.root, hostProv: hostProv, newHostname: "lab-ws-mutated"}
		// Crash right after the SetState mutation.
		muts := 0
		eCrash, self := w.engine(t, func(o *engine.Options) {
			o.Providers = providers
			o.Fault = func(p engine.Point) {
				if p == engine.PointMutation {
					muts++
					if muts == 2 { // 1 = WriteFile, 2 = first SetState
						panic(crashSentinel{})
					}
				}
			}
		})
		if !runCrashing(func() { _ = eCrash.Apply(ctx, u, statePlan()) }) {
			t.Fatal("apply did not crash after SetState Write")
		}
		w.table.Kill(self)

		// Verify hostname was mutated before the crash, then Resume restores it.
		if got, err := hostProv.Read(ctx, platform.HostnameKeyStatic); err != nil || got.Value != "lab-ws-mutated" {
			t.Fatalf("hostname after crash = %+v, %v; want lab-ws-mutated", got, err)
		}
		e, _ := w.engine(t, func(o *engine.Options) { o.Providers = providers })
		resolve := func(context.Context, contract.UnitID) (contract.Unit, contract.Plan, error) {
			return u, statePlan(), nil
		}
		res, err := e.Resume(ctx, resolve, engine.ResumeOptions{RestoreOnly: true})
		if err != nil || res.Action != engine.ResumeRestored {
			t.Fatalf("Resume = %+v, %v; want restored", res, err)
		}
		if got, err := hostProv.Read(ctx, platform.HostnameKeyStatic); err != nil || got.Value != "lab-ws-orig" {
			t.Fatalf("hostname after Resume = %+v, %v; want lab-ws-orig", got, err)
		}
	})
}

type countingStateProvider struct {
	*contracttest.FakeStateProvider
	reads     int
	writes    int
	snapshots int
	restores  int
	readErr   error
}

func (p *countingStateProvider) Read(ctx context.Context, key contract.StateKey) (contract.State, error) {
	p.reads++
	if p.readErr != nil {
		return contract.State{}, p.readErr
	}
	return p.FakeStateProvider.Read(ctx, key)
}

func (p *countingStateProvider) Write(ctx context.Context, key contract.StateKey, want contract.State) error {
	p.writes++
	return p.FakeStateProvider.Write(ctx, key, want)
}

func (p *countingStateProvider) Snapshot(ctx context.Context) (contract.Snapshot, error) {
	p.snapshots++
	return p.FakeStateProvider.Snapshot(ctx)
}

func (p *countingStateProvider) Restore(ctx context.Context, s contract.Snapshot) error {
	p.restores++
	return p.FakeStateProvider.Restore(ctx, s)
}

// TestRestoreWriteFailureExits4 is M3: a file-write failure inside restoreOne
// (such as EACCES in a read-only parent folder or a target replaced by a
// folder) exits 4 and leaves the intent StatusRestoring.
func TestRestoreWriteFailureExits4(t *testing.T) {
	ctx := context.Background()

	t.Run("atomicWriteAt failure exits 4", func(t *testing.T) {
		w := newWorld(t)
		parentDir := filepath.Join(w.root, filepath.Dir(pathA))
		e, _ := w.engine(t)
		u := &testUnit{root: w.root, afterRun: func(context.Context) error {
			// Make target directory read-only so restoreOne's createTemp fails.
			if err := os.Chmod(parentDir, 0o500); err != nil { //nolint:gosec // test temp directory made read-only to inject write error
				return err
			}
			return errNotRestored
		}}
		t.Cleanup(func() { _ = os.Chmod(parentDir, 0o700) }) //nolint:gosec // restore test temp directory permissions for cleanup

		err := e.Apply(ctx, u, testPlan())
		if !errors.Is(err, engine.ErrRestoreFailed) || engine.ExitCode(err) != contract.ExitRecoveryBlocks {
			t.Fatalf("Apply err = %v (exit %d), want ErrRestoreFailed and exit 4", err, engine.ExitCode(err))
		}
		if r := w.record(t); r == nil || r.Status != engine.StatusRestoring {
			t.Fatalf("record = %+v, want StatusRestoring", r)
		}
	})

	t.Run("absent target became a folder exits 4", func(t *testing.T) {
		w := newWorld(t)
		e, _ := w.engine(t)
		u := &testUnit{root: w.root, afterRun: func(context.Context) error {
			// pathD was absent before apply; make it a folder so restoreOne fails.
			if err := os.Mkdir(filepath.Join(w.root, pathD), 0o700); err != nil {
				return err
			}
			return errNotRestored
		}}
		err := e.Apply(ctx, u, testPlan())
		if !errors.Is(err, engine.ErrRestoreFailed) || engine.ExitCode(err) != contract.ExitRecoveryBlocks {
			t.Fatalf("Apply err = %v (exit %d), want ErrRestoreFailed and exit 4", err, engine.ExitCode(err))
		}
		if r := w.record(t); r == nil || r.Status != engine.StatusRestoring {
			t.Fatalf("record = %+v, want StatusRestoring", r)
		}
	})
}

// TestServiceAndUserGroupReturnUnsupported is M5: Tx.Service and Tx.UserGroup
// return ErrUnsupported after the declared-key check even when unit-masks,
// launchd or accounts providers are registered.
func TestServiceAndUserGroupReturnUnsupported(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	providers := map[contract.StateProviderName]contract.StateProvider{
		contract.ProviderUnitMasks: contracttest.NewFakeStateProvider(contract.ProviderUnitMasks, nil),
		contract.ProviderLaunchd:   contracttest.NewFakeStateProvider(contract.ProviderLaunchd, nil),
		contract.ProviderAccounts:  contracttest.NewFakeStateProvider(contract.ProviderAccounts, nil),
	}
	for _, tc := range []struct {
		name string
		key  contract.StateKey
		op   func(context.Context, contract.Tx) error
	}{
		{
			name: "Service declared",
			key:  "service:ssh",
			op: func(ctx context.Context, tx contract.Tx) error {
				return tx.Service(ctx, contract.ServiceState{Name: "ssh", Enabled: true, Running: true})
			},
		},
		{
			name: "UserGroup declared",
			key:  "user-group:agent-01:users",
			op: func(ctx context.Context, tx contract.Tx) error {
				return tx.UserGroup(ctx, contract.UserGroupState{User: "agent-01", Group: "users", Present: true})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := w.engine(t, func(o *engine.Options) { o.Providers = providers })
			u := &declaredOpUnit{key: tc.key, op: tc.op}
			err := e.Apply(ctx, u, testPlan())
			if !errors.Is(err, engine.ErrUnsupported) {
				t.Fatalf("Apply err = %v, want ErrUnsupported", err)
			}
		})
	}
}

type declaredOpUnit struct {
	key contract.StateKey
	op  func(context.Context, contract.Tx) error
}

func (u *declaredOpUnit) ID() contract.UnitID { return contract.UnitShell }
func (u *declaredOpUnit) Describe(context.Context) (contract.Description, error) {
	return contract.Description{
		Unit:           contract.UnitShell,
		Scope:          contract.ScopeUser,
		OwnedStateKeys: []contract.StateKey{u.key},
	}, nil
}
func (u *declaredOpUnit) Plan(context.Context, contract.Inputs) (contract.Plan, error) {
	return testPlan(), nil
}
func (u *declaredOpUnit) Apply(ctx context.Context, tx contract.Tx, _ contract.Plan) error {
	return u.op(ctx, tx)
}
func (u *declaredOpUnit) Verify(context.Context, contract.Plan) (contract.Result, error) {
	return contract.Result{Unit: contract.UnitShell, Outcome: contract.OutcomeVerified}, nil
}
func (u *declaredOpUnit) Revert(context.Context, contract.Tx, contract.Receipt) error { return nil }
func (u *declaredOpUnit) Status(context.Context) (contract.Status, error) {
	return contract.Status{Unit: contract.UnitShell}, nil
}

// TestRestoreValidatesWholeManifestBeforeReplayingState is L3: when a manifest
// contains a forbidden package-DB entry, restore fails before replaying any
// state provider snapshot.
func TestRestoreValidatesWholeManifestBeforeReplayingState(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	hostProv := &countingStateProvider{
		FakeStateProvider: contracttest.NewFakeStateProvider(contract.ProviderHostname, map[contract.StateKey]contract.State{
			platform.HostnameKeyStatic: {Value: "lab-ws-orig"},
			platform.HostnameKeyPretty: {Value: "Lab Orig"},
		}),
	}
	providers := map[contract.StateProviderName]contract.StateProvider{contract.ProviderHostname: hostProv}
	u := &fileAndStateUnit{root: w.root, hostProv: hostProv, newHostname: "lab-ws-mutated"}
	eCrash, self := w.engine(t, func(o *engine.Options) {
		o.Providers = providers
		o.Fault = crashAt(engine.PointApplied)
	})
	if !runCrashing(func() { _ = eCrash.Apply(ctx, u, statePlan()) }) {
		t.Fatal("apply did not reach PointApplied")
	}
	w.table.Kill(self)

	rec := w.record(t)
	mPath := filepath.Join(w.journal, "backups", string(rec.RunID), "manifest.json")
	raw, err := os.ReadFile(mPath) //nolint:gosec // test temp journal
	if err != nil {
		t.Fatal(err)
	}
	var m engine.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m.Entries = append(m.Entries, engine.ManifestEntry{
		Path: "/var/lib/dpkg/status",
		Kind: engine.KindFile,
		Mode: 0o644,
		UID:  os.Getuid(),
		GID:  os.Getgid(),
		Data: "0",
	})
	updated, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mPath, updated, 0o600); err != nil {
		t.Fatal(err)
	}

	restoresBefore := hostProv.restores
	e, _ := w.engine(t, func(o *engine.Options) { o.Providers = providers })
	resolve := func(context.Context, contract.UnitID) (contract.Unit, contract.Plan, error) {
		return u, statePlan(), nil
	}
	_, err = e.Resume(ctx, resolve, engine.ResumeOptions{RestoreOnly: true})
	if !errors.Is(err, engine.ErrPackageDBPath) || engine.ExitCode(err) != contract.ExitRecoveryBlocks {
		t.Fatalf("Resume err = %v (exit %d), want ErrPackageDBPath and exit 4", err, engine.ExitCode(err))
	}
	if hostProv.restores != restoresBefore {
		t.Fatalf("state provider Restore called %d times despite invalid manifest; want 0", hostProv.restores-restoresBefore)
	}
}

// TestSetStateFailsBeforeWriteOnReadError is L4: a non-not-found Read error
// aborts SetState before Write is called, while ErrStateKeyNotFound proceeds
// with an empty before digest.
func TestSetStateFailsBeforeWriteOnReadError(t *testing.T) {
	ctx := context.Background()
	readBoom := errors.New("injected read failure")
	w := newWorld(t)
	hostProv := &countingStateProvider{
		FakeStateProvider: contracttest.NewFakeStateProvider(contract.ProviderHostname, map[contract.StateKey]contract.State{
			platform.HostnameKeyStatic: {Value: "lab-ws-orig"},
		}),
		readErr: readBoom,
	}
	providers := map[contract.StateProviderName]contract.StateProvider{contract.ProviderHostname: hostProv}
	e, _ := w.engine(t, func(o *engine.Options) { o.Providers = providers })
	u := &declaredOpUnit{
		key: platform.HostnameKeyStatic,
		op: func(ctx context.Context, tx contract.Tx) error {
			return tx.SetState(ctx, hostProv, platform.HostnameKeyStatic, contract.State{Value: "lab-ws-new"})
		},
	}
	err := e.Apply(ctx, u, testPlan())
	if !errors.Is(err, readBoom) {
		t.Fatalf("Apply err = %v, want %v", err, readBoom)
	}
	if hostProv.writes != 0 {
		t.Fatalf("Write called %d times after Read failure; want 0", hostProv.writes)
	}

	t.Run("not-found Read proceeds with empty Before digest", func(t *testing.T) {
		w2 := newWorld(t)
		p2 := &countingStateProvider{
			FakeStateProvider: contracttest.NewFakeStateProvider(contract.ProviderHostname, map[contract.StateKey]contract.State{}),
			readErr:           platform.ErrStateKeyNotFound,
		}
		e2, _ := w2.engine(t, func(o *engine.Options) {
			o.Providers = map[contract.StateProviderName]contract.StateProvider{contract.ProviderHostname: p2}
		})
		u2 := &declaredOpUnit{
			key: platform.HostnameKeyStatic,
			op: func(ctx context.Context, tx contract.Tx) error {
				return tx.SetState(ctx, p2, platform.HostnameKeyStatic, contract.State{Value: "lab-ws-new"})
			},
		}
		if err := e2.Apply(ctx, u2, testPlan()); err != nil {
			t.Fatalf("Apply with not-found Read: %v", err)
		}
		rec := w2.record(t)
		rc := w2.receipt(t, rec.RunID)
		if rc == nil || len(rc.Changes) != 1 || rc.Changes[0].Before != "" {
			t.Fatalf("receipt = %+v, want 1 change with empty Before digest", rc)
		}
	})
}
