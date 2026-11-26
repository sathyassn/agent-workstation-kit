// Package workflows checks the events and reported check names of the
// repository and Go workflows.
package workflows

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var (
	triggerKey    = regexp.MustCompile(`^  [a-z_]+:`)
	triggerEntry  = regexp.MustCompile(`^  ([a-z_]+):(?:\n    branches:(?: \[([^\n]+)\]|((?:\n      - [^\n]+)+)))?$`)
	branchItem    = regexp.MustCompile(`(?m)^      - (.+)$`)
	jobHeader     = regexp.MustCompile(`(?m)^  ([a-z0-9_-]+):\n`)
	jobHeaderLine = regexp.MustCompile(`^  [a-z0-9_-]+:$`)
	// Every job key must be plain and known, so a quoted or escaped key (such
	// as "strategy" or "runs-on") cannot hide a setting from this test.
	jobKey = regexp.MustCompile(`^    (name|runs-on|timeout-minutes|strategy|steps|if|needs|permissions|env|` +
		`continue-on-error|concurrency|environment|defaults|outputs|services|container):(?: (.*))?$`)
	// A job's strategy may only be the shape this test models: fail-fast and
	// a plain os list. Anything else (include or exclude in any spelling, flow
	// style, anchors, expressions) could change which jobs GitHub reports, so
	// the test refuses it instead of parsing it.
	strategyLine = regexp.MustCompile(`^(?:      fail-fast: (?:true|false)|      matrix:|        os:|          - [A-Za-z0-9._-]+)$`)
	osItem       = regexp.MustCompile(`^          - ([A-Za-z0-9._-]+)$`)
)

// job is what the test reads from one job: its keys with their inline
// values, and the os list of its strategy (nil when it has none).
type job struct {
	keys map[string]string
	os   []string
}

// parseJob reads a job body. It fails on any key line jobKey does not
// accept, a duplicate key, or a strategy line strategyLine does not accept.
// The os list comes only from the validated strategy block.
func parseJob(t *testing.T, filename, jobID, body string) job {
	t.Helper()
	j := job{keys: map[string]string{}}
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if line == "" || strings.HasPrefix(line, "     ") {
			continue
		}
		m := jobKey.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("%s: job %s has an unsupported key line %q", filename, jobID, line)
		}
		if _, seen := j.keys[m[1]]; seen {
			t.Fatalf("%s: job %s repeats key %q", filename, jobID, m[1])
		}
		j.keys[m[1]] = m[2]
		if m[1] != "strategy" {
			continue
		}
		if line != "    strategy:" {
			t.Fatalf("%s: job %s has an unsupported strategy line %q", filename, jobID, line)
		}
		j.os = []string{}
		for _, inner := range lines[i+1:] {
			if !strings.HasPrefix(inner, "      ") {
				break
			}
			if !strategyLine.MatchString(inner) {
				t.Fatalf("%s: job %s has an unsupported strategy line %q", filename, jobID, inner)
			}
			if item := osItem.FindStringSubmatch(inner); item != nil {
				j.os = append(j.os, item[1])
			}
		}
	}
	return j
}

// workflowSection returns the lines under the top-level key, up to the next
// top-level line, without blank and comment lines. It fails unless the key
// appears exactly once.
func workflowSection(t *testing.T, filename, key string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", filename)) //nolint:gosec // fixed workflow path
	if err != nil {
		t.Fatal(err)
	}
	var blocks [][]string
	var current []string
	inBlock := false
	for _, line := range strings.Split(string(data), "\n") {
		if line == key+":" {
			if inBlock {
				blocks = append(blocks, current)
			}
			current, inBlock = nil, true
			continue
		}
		if inBlock && line != "" && !strings.ContainsAny(line[:1], " \t\r\f\v#") {
			blocks = append(blocks, current)
			current, inBlock = nil, false
		}
		if inBlock {
			current = append(current, line)
		}
	}
	if inBlock {
		blocks = append(blocks, current)
	}
	if len(blocks) != 1 {
		t.Fatalf("%s: expected one block for %q, found %d", filename, key, len(blocks))
	}
	var kept []string
	for _, line := range blocks[0] {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

func TestTriggerMatrix(t *testing.T) {
	for _, filename := range []string{"ci.yml", "go.yml"} {
		t.Run(filename, func(t *testing.T) {
			// Read the literal 'on' key. Unknown syntax fails the test
			// instead of dropping a filter or event.
			block := workflowSection(t, filename, "on")
			var entries []string
			for _, line := range strings.Split(block, "\n") {
				if triggerKey.MatchString(line) || len(entries) == 0 {
					entries = append(entries, line)
				} else {
					entries[len(entries)-1] += "\n" + line
				}
			}
			triggers := map[string][]string{}
			for _, entry := range entries {
				match := triggerEntry.FindStringSubmatch(entry)
				if match == nil {
					t.Fatalf("unrecognised trigger entry:\n%s", entry)
				}
				event, inline, list := match[1], match[2], match[3]
				if _, seen := triggers[event]; seen {
					t.Fatalf("duplicate trigger %q", event)
				}
				var branches []string
				switch {
				case inline != "":
					for _, branch := range strings.Split(inline, ",") {
						branches = append(branches, strings.TrimSpace(branch))
					}
				case list != "":
					for _, item := range branchItem.FindAllStringSubmatch(list, -1) {
						branches = append(branches, item[1])
					}
				}
				triggers[event] = branches
			}

			// Exact event keys and branch allowlists also exclude every
			// other branch (including integration branches) and event.
			want := map[string][]string{
				"pull_request":      {"main"},
				"push":              {"main"},
				"workflow_dispatch": nil,
			}
			if len(triggers) != len(want) {
				t.Fatalf("triggers = %v, want %v", triggers, want)
			}
			for event, branches := range want {
				got, ok := triggers[event]
				if !ok || !slices.Equal(got, branches) {
					t.Fatalf("triggers = %v, want %v", triggers, want)
				}
			}
		})
	}
}

func TestReportedJobNames(t *testing.T) {
	expected := map[string][]string{
		"ci.yml": {
			"linux-public-scan (non-authoritative head self-test)",
			"macos-host-lock",
			"macos-public-scan (non-authoritative head self-test)",
			"repository-validation",
			"secret-scan",
		},
		"go.yml": {
			"Go (macos-latest)",
			"Go (ubuntu-24.04)",
			"Go fault injection (macos-latest)",
			"Go fault injection (ubuntu-24.04)",
		},
	}
	for filename, want := range expected {
		t.Run(filename, func(t *testing.T) {
			jobs := workflowSection(t, filename, "jobs")
			for _, line := range strings.Split(jobs, "\n") {
				if !strings.HasPrefix(line, "    ") && !jobHeaderLine.MatchString(line) {
					t.Fatalf("%s: unsupported line in jobs %q", filename, line)
				}
			}
			headers := jobHeader.FindAllStringSubmatchIndex(jobs, -1)
			var names []string
			for i, header := range headers {
				jobID := jobs[header[2]:header[3]]
				end := len(jobs)
				if i+1 < len(headers) {
					end = headers[i+1][0]
				}
				body := jobs[header[1]:end]
				j := parseJob(t, filename, jobID, body)
				template := jobID
				if name, ok := j.keys["name"]; ok {
					template = name
				}
				if !strings.Contains(template, "${{ matrix.os }}") {
					names = append(names, template)
					continue
				}
				// A job named per matrix.os must also run on it, or a
				// check reported for macOS could run on Linux.
				if runsOn := j.keys["runs-on"]; runsOn != "${{ matrix.os }}" {
					t.Fatalf("%s: job %s is named per matrix.os but runs on %q", filename, jobID, runsOn)
				}
				if len(j.os) == 0 {
					t.Fatalf("%s: job %s names matrix.os but has no os matrix", filename, jobID)
				}
				for _, osName := range j.os {
					names = append(names, strings.ReplaceAll(template, "${{ matrix.os }}", osName))
				}
			}
			slices.Sort(names)
			if !slices.Equal(names, want) {
				t.Fatalf("job names = %q, want %q", names, want)
			}
		})
	}
}
