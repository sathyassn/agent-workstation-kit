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
	triggerKey   = regexp.MustCompile(`^  [a-z_]+:`)
	triggerEntry = regexp.MustCompile(`^  ([a-z_]+):(?:\n    branches:(?: \[([^\n]+)\]|((?:\n      - [^\n]+)+)))?$`)
	branchItem   = regexp.MustCompile(`(?m)^      - (.+)$`)
	jobHeader    = regexp.MustCompile(`(?m)^  ([a-z0-9_-]+):\n`)
	jobName      = regexp.MustCompile(`(?m)^    name: (.+)$`)
	jobOSMatrix  = regexp.MustCompile(`(?m)^        os:\n((?:          - [^\n]+\n)+)`)
	// include and exclude change which matrix jobs GitHub reports; this test
	// does not model them, so it refuses them rather than miss a dropped job.
	matrixModifier = regexp.MustCompile(`(?m)^\s+(include|exclude):`)
)

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
			headers := jobHeader.FindAllStringSubmatchIndex(jobs, -1)
			var names []string
			for i, header := range headers {
				jobID := jobs[header[2]:header[3]]
				end := len(jobs)
				if i+1 < len(headers) {
					end = headers[i+1][0]
				}
				body := jobs[header[1]:end]
				if modifier := matrixModifier.FindStringSubmatch(body); modifier != nil {
					t.Fatalf("%s: job %s uses matrix %s, which this test does not support", filename, jobID, modifier[1])
				}
				template := jobID
				if name := jobName.FindStringSubmatch(body); name != nil {
					template = name[1]
				}
				if !strings.Contains(template, "${{ matrix.os }}") {
					names = append(names, template)
					continue
				}
				matrix := jobOSMatrix.FindStringSubmatch(body)
				if matrix == nil {
					t.Fatalf("%s: job %s names matrix.os but has no os matrix", filename, jobID)
				}
				for _, line := range strings.Split(strings.TrimSuffix(matrix[1], "\n"), "\n") {
					names = append(names, strings.ReplaceAll(template, "${{ matrix.os }}", strings.TrimPrefix(line, "          - ")))
				}
			}
			slices.Sort(names)
			if !slices.Equal(names, want) {
				t.Fatalf("job names = %q, want %q", names, want)
			}
		})
	}
}
