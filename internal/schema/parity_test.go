package schema_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sathyassn/agent-workstation-kit/internal/schema"
)

func repoRootDir(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	return abs
}

func runPythonFleetctlValidate(t *testing.T, repoRoot, profilePath string, ready bool) (int, []string, string) {
	t.Helper()
	pythonBin, err := exec.LookPath("python3")
	if err != nil {
		t.Fatalf("python3 is required for parity testing: %v", err)
	}
	args := []string{filepath.Join(repoRoot, "scripts", "fleetctl.py"), "validate", profilePath}
	if ready {
		args = append(args, "--ready")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, pythonBin, args...) //nolint:gosec // fixed repo script path
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("run python fleetctl validate %s: %v", profilePath, err)
		}
	}
	var paths []string
	for _, line := range strings.Split(stderr.String(), "\n") {
		if !strings.HasPrefix(line, "ERROR ") {
			continue
		}
		rest := strings.TrimPrefix(line, "ERROR ")
		p, _, ok := strings.Cut(rest, ":")
		if ok {
			paths = append(paths, strings.TrimSpace(p))
		}
	}
	slices.Sort(paths)
	paths = slices.Compact(paths)
	return exitCode, paths, stderr.String()
}

// TestPythonGoProfileValidatorParity covers AC-1: runs BOTH scripts/fleetctl.py
// validate and schema.ValidateFile on every fixture in testdata/profiles/ and
// config/profiles/*.example.toml in both draft and ready modes, proving they
// accept and reject the exact same fixtures and report matching issue paths.
func TestPythonGoProfileValidatorParity(t *testing.T) {
	t.Parallel()

	repoRoot := repoRootDir(t)
	var fixtures []struct {
		rel      string
		abs      string
		category string // "draft", "ready", "invalid", "example"
	}

	addGlob := func(dir, category string) {
		t.Helper()
		matches, err := filepath.Glob(filepath.Join(repoRoot, filepath.FromSlash(dir), "*.toml"))
		if err != nil {
			t.Fatalf("glob %s: %v", dir, err)
		}
		if len(matches) == 0 {
			t.Fatalf("no fixtures found in %s", dir)
		}
		slices.Sort(matches)
		for _, m := range matches {
			rel, _ := filepath.Rel(repoRoot, m)
			fixtures = append(fixtures, struct {
				rel      string
				abs      string
				category string
			}{rel: filepath.ToSlash(rel), abs: m, category: category})
		}
	}

	addGlob("testdata/profiles/valid/draft", "draft")
	addGlob("testdata/profiles/valid/ready", "ready")
	addGlob("testdata/profiles/invalid", "invalid")
	addGlob("config/profiles", "example")

	for _, fx := range fixtures {
		for _, ready := range []bool{false, true} {
			mode := "draft"
			if ready {
				mode = "ready"
			}
			t.Run(fx.rel+"/"+mode, func(t *testing.T) {
				t.Parallel()

				pyExit, pyPaths, pyStderr := runPythonFleetctlValidate(t, repoRoot, fx.abs, ready)
				goIssues, goErr := schema.ValidateFile(fx.abs, ready)

				pyAccepted := pyExit == 0
				goAccepted := goErr == nil && len(goIssues) == 0

				if pyAccepted != goAccepted {
					t.Fatalf("parity mismatch on %s (ready=%t):\n  python exit=%d stderr=%q\n  go err=%v issues=%v",
						fx.rel, ready, pyExit, pyStderr, goErr, goIssues)
				}

				switch fx.category {
				case "ready":
					if !goAccepted {
						t.Fatalf("expected ready fixture %s to be accepted (ready=%t), got err=%v issues=%v",
							fx.rel, ready, goErr, goIssues)
					}
				case "draft", "example":
					if !ready && !goAccepted {
						t.Fatalf("expected draft fixture %s to be accepted in draft mode, got err=%v issues=%v",
							fx.rel, goErr, goIssues)
					}
					if ready && goAccepted {
						t.Fatalf("expected draft fixture %s to be rejected in ready mode", fx.rel)
					}
				case "invalid":
					if goAccepted {
						t.Fatalf("expected invalid fixture %s to be rejected (ready=%t)", fx.rel, ready)
					}
				}

				// When both rejected a parseable TOML file, assert identical issue path sets.
				if !goAccepted && goErr == nil {
					goPaths := make([]string, len(goIssues))
					for i, iss := range goIssues {
						goPaths[i] = iss.Path
					}
					slices.Sort(goPaths)
					goPaths = slices.Compact(goPaths)
					if !slices.Equal(pyPaths, goPaths) {
						t.Fatalf("issue path mismatch on %s (ready=%t):\n  python paths: %v\n  go paths:     %v\n  python stderr: %s",
							fx.rel, ready, pyPaths, goPaths, pyStderr)
					}
				}
			})
		}
	}
}

func runPythonValidateFleet(t *testing.T, repoRoot, fleetDir string) (int, string) {
	t.Helper()
	pythonBin, err := exec.LookPath("python3")
	if err != nil {
		t.Fatalf("python3 is required for fleet parity testing: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, pythonBin, filepath.Join(repoRoot, "scripts", "validate-fleet.py"), fleetDir) //nolint:gosec // fixed repo script path
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	if err == nil {
		return 0, stdout.String() + stderr.String()
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), stdout.String() + stderr.String()
	}
	t.Fatalf("run python validate-fleet.py: %v", err)
	return 1, ""
}

// TestPythonGoFleetValidatorParity compares scripts/validate-fleet.py against
// schema.ValidateFleetDir across valid and invalid fleet layouts.
func TestPythonGoFleetValidatorParity(t *testing.T) {
	t.Parallel()

	repoRoot := repoRootDir(t)
	verBytes, err := os.ReadFile(filepath.Join(repoRoot, "VERSION")) //nolint:gosec // repo VERSION file
	if err != nil {
		t.Fatalf("read VERSION: %v", err)
	}
	kitVersion := strings.TrimSpace(string(verBytes))

	ready1Bytes, err := os.ReadFile(filepath.Join(repoRoot, "testdata", "profiles", "valid", "ready", "work-linux-ready.toml")) //nolint:gosec // repo fixture
	if err != nil {
		t.Fatalf("read ready1: %v", err)
	}
	ready2Bytes, err := os.ReadFile(filepath.Join(repoRoot, "testdata", "profiles", "valid", "ready", "work-linux-mise-pin.toml")) //nolint:gosec // repo fixture
	if err != nil {
		t.Fatalf("read ready2: %v", err)
	}
	ready1 := string(ready1Bytes)
	ready2 := string(ready2Bytes)

	writeTemp := func(t *testing.T, p, content string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil { //nolint:gosec // test temp dir
			t.Fatalf("write %s: %v", p, err)
		}
	}

	setupFleet := func(t *testing.T, mutate func(t *testing.T, fleetRoot string)) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "machines"), 0o750); err != nil {
			t.Fatalf("mkdir machines: %v", err)
		}
		writeTemp(t, filepath.Join(dir, "kit.lock"), kitVersion+"\n")
		writeTemp(t, filepath.Join(dir, "machines", "acme-ws-001.toml"), ready1)
		writeTemp(t, filepath.Join(dir, "machines", "acme-ws-002.toml"), ready2)
		if mutate != nil {
			mutate(t, dir)
		}
		return dir
	}

	cases := []struct {
		name       string
		wantAccept bool
		mutate     func(t *testing.T, dir string)
	}{
		{name: "valid two-host fleet", wantAccept: true},
		{
			name:       "duplicate display name case-insensitive",
			wantAccept: false,
			mutate: func(t *testing.T, dir string) {
				dup := strings.Replace(ready2, `display_name = "Beacon"`, `display_name = "atlas"`, 1)
				writeTemp(t, filepath.Join(dir, "machines", "acme-ws-002.toml"), dup)
			},
		},
		{
			name:       "duplicate uuid",
			wantAccept: false,
			mutate: func(t *testing.T, dir string) {
				dup := strings.Replace(ready2, `22222222-2222-4222-8222-222222222222`, `11111111-1111-4111-8111-111111111111`, 1)
				writeTemp(t, filepath.Join(dir, "machines", "acme-ws-002.toml"), dup)
			},
		},
		{
			name:       "duplicate asset tag",
			wantAccept: false,
			mutate: func(t *testing.T, dir string) {
				dup := strings.Replace(ready2, `asset_tag = "ACME-10002"`, `asset_tag = "acme-10001"`, 1)
				writeTemp(t, filepath.Join(dir, "machines", "acme-ws-002.toml"), dup)
			},
		},
		{
			name:       "inconsistent shared provider principal",
			wantAccept: false,
			mutate: func(t *testing.T, dir string) {
				bad := strings.Replace(ready2, `github_identity = "app"`, `github_identity = "machine-user"`, 1)
				writeTemp(t, filepath.Join(dir, "machines", "acme-ws-002.toml"), bad)
			},
		},
		{
			name:       "retired hostname reused",
			wantAccept: false,
			mutate: func(t *testing.T, dir string) {
				writeTemp(t, filepath.Join(dir, "retired-hostnames.txt"), "acme-ws-001\n")
			},
		},
		{
			name:       "filename mismatch",
			wantAccept: false,
			mutate: func(t *testing.T, dir string) {
				if err := os.Rename(
					filepath.Join(dir, "machines", "acme-ws-002.toml"),
					filepath.Join(dir, "machines", "wrong-name.toml"),
				); err != nil {
					t.Fatalf("rename: %v", err)
				}
			},
		},
		{
			name:       "wrong kit.lock version",
			wantAccept: false,
			mutate: func(t *testing.T, dir string) {
				writeTemp(t, filepath.Join(dir, "kit.lock"), "0.0.1\n")
			},
		},
		{
			name:       "misplaced root toml",
			wantAccept: false,
			mutate: func(t *testing.T, dir string) {
				writeTemp(t, filepath.Join(dir, "misplaced.toml"), "schema_version = 3\n")
			},
		},
		{
			name:       "nested profile under machines",
			wantAccept: false,
			mutate: func(t *testing.T, dir string) {
				nested := filepath.Join(dir, "machines", "pilot")
				if err := os.MkdirAll(nested, 0o750); err != nil {
					t.Fatalf("mkdir nested: %v", err)
				}
				writeTemp(t, filepath.Join(nested, "acme-ws-003.toml"), "schema_version = 3\n")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := setupFleet(t, tc.mutate)
			pyExit, pyOut := runPythonValidateFleet(t, repoRoot, dir)
			goIssues, goErr := schema.ValidateFleetDir(dir, kitVersion)

			pyAccepted := pyExit == 0
			goAccepted := goErr == nil && len(goIssues) == 0

			if pyAccepted != goAccepted || goAccepted != tc.wantAccept {
				t.Fatalf("fleet parity mismatch (%s): wantAccept=%t pythonExit=%d (%q) goErr=%v goIssues=%v",
					tc.name, tc.wantAccept, pyExit, pyOut, goErr, goIssues)
			}
		})
	}
}

func FuzzValidateTOML(f *testing.F) {
	f.Add([]byte("schema_version = 3\n"))
	f.Add([]byte("schema_version = [\n"))
	f.Add([]byte("[machine]\nhostname = \"acme-ws-001\"\n"))
	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = schema.ValidateTOML(data, false)
		_, _ = schema.ValidateTOML(data, true)
	})
}
