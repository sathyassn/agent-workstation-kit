// Package release cross-builds the release targets and inspects the binaries
// without running them, so no kit binary carries the faultinject code.
package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// envRunReleaseCheck turns on TestReleaseBinariesHaveNoFaultInjection. The
// check builds with `go build`, which the test cache cannot see, so it runs
// only when asked, always with -count=1 (make go-check and the Go workflow).
const envRunReleaseCheck = "WSKIT_TEST_RELEASE_BINARIES"

var (
	releaseTargets   = [][2]string{{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "arm64"}}
	requiredBinaries = []string{"agentctl", "wskit"}
	faultSymbol      = regexp.MustCompile(`/internal/engine\.(?:defaultFault\..+|parseKillAt(?:\..+)?)$`)
	faultEnvDecl     = regexp.MustCompile(`(?m)^const EnvFaultKillAt = "([A-Za-z_][A-Za-z0-9_]*)"$`)
)

var (
	// errFaultInjection: a release binary contains fault-injection evidence.
	errFaultInjection = errors.New("fault injection in binary")
	// errFaultSymbol: the symbol table contains a fault-injection symbol.
	errFaultSymbol = fmt.Errorf("%w: symbol", errFaultInjection)
)

// faultEnvName reads the environment variable name the faultinject build uses.
func faultEnvName(source []byte) (string, error) {
	match := faultEnvDecl.FindSubmatch(source)
	if match == nil {
		return "", errors.New("cannot read EnvFaultKillAt from internal/engine/fault_inject.go")
	}
	return string(match[1]), nil
}

// checkSymbols looks for main.main and for any fault-injection symbol in
// `go tool nm` output.
func checkSymbols(nm []byte, label string) error {
	lines := bytes.Split(nm, []byte("\n"))
	hasMain := false
	for _, line := range lines {
		fields := strings.Fields(string(line))
		if len(fields) >= 2 && fields[len(fields)-2] == "T" && fields[len(fields)-1] == "main.main" {
			hasMain = true
			break
		}
	}
	if !hasMain {
		return fmt.Errorf("%s has no main.main symbol", label)
	}
	for _, line := range lines {
		if faultSymbol.Match(line) {
			fields := strings.Fields(string(line))
			return fmt.Errorf("%w: %s contains fault-injection symbol %s", errFaultSymbol, label, fields[len(fields)-1])
		}
	}
	return nil
}

// checkBuildTags reads `go version -m -json` output and refuses a binary the
// linker was given a faultinject build for.
func checkBuildTags(info []byte, label string) error {
	var parsed struct {
		Settings []struct{ Key, Value string }
	}
	if err := json.Unmarshal(info, &parsed); err != nil {
		return fmt.Errorf("%s: read build info: %w", label, err)
	}
	for _, setting := range parsed.Settings {
		if setting.Key != "-tags" {
			continue
		}
		for _, tag := range strings.Split(setting.Value, ",") {
			if tag == "faultinject" {
				return fmt.Errorf("%w: %s was built with the faultinject tag", errFaultInjection, label)
			}
		}
	}
	return nil
}

// checker runs the go tool against one module root.
type checker struct {
	root     string
	goBin    string
	env      []string // the caller's environment, for inspection
	buildEnv []string // offline, local-toolchain environment for builds
	envName  string
}

func (c checker) output(ctx context.Context, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.goBin, args...) //nolint:gosec // GOROOT go binary, fixed arguments
	cmd.Dir = c.root
	cmd.Env = env
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

func (c checker) checkBinary(ctx context.Context, binary, label string) error {
	nm, err := c.output(ctx, c.env, "tool", "nm", binary)
	if err != nil {
		return err
	}
	if err := checkSymbols(nm, label); err != nil {
		return err
	}
	data, err := os.ReadFile(binary) //nolint:gosec // binary built under t.TempDir
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	if bytes.Contains(data, []byte(c.envName)) {
		return fmt.Errorf("%w: %s contains fault-injection environment name %s", errFaultInjection, label, c.envName)
	}
	// A command can leave the entire engine hook unreachable. Its build
	// settings still tell us if the linker was given a fault-injection build.
	info, err := c.output(ctx, c.env, "version", "-m", "-json", binary)
	if err != nil {
		return err
	}
	return checkBuildTags(info, label)
}

func withEnv(base []string, kv ...string) []string {
	return append(append([]string{}, base...), kv...)
}

func newChecker(t *testing.T) checker {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root not found from the test directory: %v", err)
	}
	source, err := os.ReadFile(filepath.Join(root, "internal", "engine", "fault_inject.go")) //nolint:gosec // fixed path in this module
	if err != nil {
		t.Fatal(err)
	}
	envName, err := faultEnvName(source)
	if err != nil {
		t.Fatal(err)
	}
	env := os.Environ()
	// Keep the user's checksum settings so Go can verify a downloaded toolchain.
	cmd := exec.CommandContext(t.Context(), "go", "env", "GOROOT")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go env GOROOT: %v", err)
	}
	goroot := strings.TrimSpace(string(out))
	// make go-check and CI populate the module cache before these builds.
	buildEnv := withEnv(env,
		"CGO_ENABLED=0",
		"GOROOT="+goroot,
		"GOTOOLCHAIN=local",
		"GOPROXY=off",
		"GONOPROXY=none",
	)
	return checker{
		root:     root,
		goBin:    filepath.Join(goroot, "bin", "go"),
		env:      env,
		buildEnv: buildEnv,
		envName:  envName,
	}
}

// TestReleaseBinariesHaveNoFaultInjection builds every release target, after a
// positive control proves the check still detects a tagged build.
func TestReleaseBinariesHaveNoFaultInjection(t *testing.T) {
	if os.Getenv(envRunReleaseCheck) != "1" {
		t.Skipf("set %s=1 and -count=1 to run; make go-check does", envRunReleaseCheck)
	}
	c := newChecker(t)
	ctx := t.Context()
	tmp := t.TempDir()

	t.Run("positive control", func(t *testing.T) {
		hostOut, err := c.output(ctx, c.buildEnv, "env", "-json", "GOHOSTOS", "GOHOSTARCH")
		if err != nil {
			t.Fatal(err)
		}
		var host struct{ GOHOSTOS, GOHOSTARCH string }
		if err := json.Unmarshal(hostOut, &host); err != nil {
			t.Fatal(err)
		}
		binary := filepath.Join(tmp, "engine.test")
		hostEnv := withEnv(c.buildEnv, "GOOS="+host.GOHOSTOS, "GOARCH="+host.GOHOSTARCH)
		if _, err := c.output(ctx, hostEnv, "test", "-c", "-tags", "faultinject", "-ldflags=", "-o", binary, "./internal/engine"); err != nil {
			t.Fatal(err)
		}
		err = c.checkBinary(ctx, binary, "tagged engine test binary")
		switch {
		case errors.Is(err, errFaultSymbol):
			t.Logf("PASS positive control: %v", err)
		case err == nil:
			t.Fatal("positive control did not detect a fault-injection symbol")
		default:
			t.Fatalf("positive control did not detect a fault-injection symbol: %v", err)
		}
	})
	if t.Failed() {
		return
	}

	for _, target := range releaseTargets {
		goos, goarch := target[0], target[1]
		t.Run(goos+"-"+goarch, func(t *testing.T) {
			out := filepath.Join(tmp, goos+"-"+goarch)
			if err := os.Mkdir(out, 0o750); err != nil {
				t.Fatal(err)
			}
			targetEnv := withEnv(c.buildEnv, "GOOS="+goos, "GOARCH="+goarch)
			if _, err := c.output(ctx, targetEnv, "build", "./..."); err != nil {
				t.Fatal(err)
			}
			if _, err := c.output(ctx, targetEnv, "build", "-o", out, "./cmd/..."); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(out)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, entry := range entries {
				if entry.Type().IsRegular() {
					names = append(names, entry.Name())
				}
			}
			slices.Sort(names)
			if len(names) == 0 {
				t.Fatalf("%s/%s release output is empty", goos, goarch)
			}
			var missing []string
			for _, want := range requiredBinaries {
				if !slices.Contains(names, want) {
					missing = append(missing, want)
				}
			}
			if len(missing) > 0 {
				t.Fatalf("%s/%s release output is missing %s", goos, goarch, strings.Join(missing, ", "))
			}
			for _, name := range names {
				label := goos + "/" + goarch + " " + name
				if err := c.checkBinary(ctx, filepath.Join(out, name), label); err != nil {
					t.Fatal(err)
				}
				t.Logf("PASS %s has no fault-injection symbol, environment name or build tag", label)
			}
		})
	}
}
