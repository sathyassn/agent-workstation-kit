package contract_test

import (
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
)

type specMatrixRow struct {
	command string
	scope   string
	runsAs  string
	modes   string
}

func parseSPC001CommandMatrix(t *testing.T) []specMatrixRow {
	t.Helper()

	raw, err := fs.ReadFile(os.DirFS("../.."), "project-management/specs/SPC-001.md")
	if err != nil {
		t.Fatalf("read SPC-001.md: %v", err)
	}

	lines := strings.Split(string(raw), "\n")
	inTable := false
	seenSep := false
	var rows []specMatrixRow
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inTable {
			if strings.HasPrefix(trimmed, "**Command matrix**") {
				inTable = true
			}
			continue
		}
		if trimmed == "" {
			if seenSep {
				break
			}
			continue
		}
		if !strings.HasPrefix(trimmed, "|") {
			if seenSep {
				break
			}
			continue
		}
		if strings.HasPrefix(trimmed, "|---") {
			seenSep = true
			continue
		}
		if !seenSep {
			continue
		}
		cells := splitMarkdownRow(trimmed)
		if len(cells) != 4 {
			t.Fatalf("unexpected column count in SPC-001 command matrix row %q: got %d", trimmed, len(cells))
		}
		rows = append(rows, specMatrixRow{
			command: stripBackticks(cells[0]),
			scope:   stripBackticks(cells[1]),
			runsAs:  stripBackticks(cells[2]),
			modes:   stripBackticks(cells[3]),
		})
	}
	if len(rows) == 0 {
		t.Fatal("failed to parse any rows from SPC-001 command matrix")
	}
	return rows
}

func splitMarkdownRow(line string) []string {
	inner := strings.TrimPrefix(strings.TrimSuffix(line, "|"), "|")
	parts := strings.Split(inner, "|")
	// Rejoin pipe-delimited tokens that appear inside backticks such as
	// `UNIT apply|revert` or `config get|set|explain|validate`.
	var cells []string
	var buf strings.Builder
	inCode := false
	for i, part := range parts {
		if buf.Len() > 0 {
			buf.WriteByte('|')
		}
		buf.WriteString(part)
		if strings.Count(part, "`")%2 == 1 {
			inCode = !inCode
		}
		if !inCode || i == len(parts)-1 {
			cells = append(cells, strings.TrimSpace(buf.String()))
			buf.Reset()
		}
	}
	return cells
}

func stripBackticks(s string) string {
	return strings.ReplaceAll(s, "`", "")
}

func formatModes(modes []contract.CommandMode) string {
	parts := make([]string, len(modes))
	for i, m := range modes {
		parts[i] = m.String()
	}
	return strings.Join(parts, "; ")
}

func TestCommandMatrixMatchesSPC001(t *testing.T) {
	t.Parallel()

	specRows := parseSPC001CommandMatrix(t)
	matrix := contract.CommandMatrix()

	if len(matrix) != len(specRows) {
		t.Fatalf("CommandMatrix has %d rows, SPC-001 has %d rows", len(matrix), len(specRows))
	}

	for i, spec := range specRows {
		got := matrix[i]
		if got.Command != spec.command {
			t.Errorf("row %d command = %q, want %q", i, got.Command, spec.command)
		}
		if got.Scope.String() != spec.scope {
			t.Errorf("row %d scope = %q, want %q", i, got.Scope, spec.scope)
		}
		if got.RunsAs.String() != spec.runsAs {
			t.Errorf("row %d runs_as = %q, want %q", i, got.RunsAs, spec.runsAs)
		}
		if gotModes := formatModes(got.Modes); gotModes != spec.modes {
			t.Errorf("row %d modes = %q, want %q", i, gotModes, spec.modes)
		}
	}
}

func TestCommandMatrixTypesAndCommandNames(t *testing.T) {
	t.Parallel()

	matrix := contract.CommandMatrix()
	if len(matrix) == 0 {
		t.Fatal("CommandMatrix is empty")
	}

	for i, row := range matrix {
		if !row.Scope.Valid() {
			t.Errorf("row %d has invalid Scope %q", i, row.Scope)
		}
		if !row.RunsAs.Valid() {
			t.Errorf("row %d has invalid RunsAs %q", i, row.RunsAs)
		}
		if len(row.Modes) == 0 {
			t.Errorf("row %d has no Modes", i)
		}
		for _, m := range row.Modes {
			if !m.Valid() {
				t.Errorf("row %d has invalid CommandMode %q", i, m)
			}
		}
		if len(row.Verbs) == 0 && len(row.NonUnits) == 0 {
			t.Errorf("row %d (%q) names neither a Verb nor a NonUnitCommand", i, row.Command)
		}
		for _, v := range row.Verbs {
			if !v.Valid() {
				t.Errorf("row %d has invalid Verb %q", i, v)
			}
		}
		for _, c := range row.NonUnits {
			if !c.Valid() {
				t.Errorf("row %d has invalid NonUnitCommand %q", i, c)
			}
		}

		// Verify every command entry listed in row.Command maps to a known Verb
		// or NonUnitCommand declared on the row.
		declared := make(map[string]bool, len(row.Verbs)+len(row.NonUnits))
		for _, v := range row.Verbs {
			declared[v.String()] = true
		}
		for _, c := range row.NonUnits {
			declared[c.String()] = true
		}

		extracted := extractCommandTokens(row.Command)
		if len(extracted) == 0 {
			t.Errorf("row %d (%q) extracted no command tokens", i, row.Command)
		}
		for _, tok := range extracted {
			isVerb := contract.Verb(tok).Valid()
			isNonUnit := contract.NonUnitCommand(tok).Valid()
			if !isVerb && !isNonUnit {
				t.Errorf("row %d token %q is neither a valid Verb nor a documented NonUnitCommand", i, tok)
			}
			if !declared[tok] {
				t.Errorf("row %d token %q is missing from row.Verbs / row.NonUnits", i, tok)
			}
		}
		if len(extracted) != len(declared) {
			t.Errorf("row %d declared %d commands (%v), but command text %q names %d (%v)",
				i, len(declared), declared, row.Command, len(extracted), extracted)
		}
	}
}

func extractCommandTokens(expr string) []string {
	// Strip parenthetical qualifier such as "(user units)" or "(root units)".
	if idx := strings.Index(expr, "("); idx >= 0 {
		expr = expr[:idx]
	}
	var tokens []string
	for _, part := range strings.Split(expr, ",") {
		fields := strings.Fields(strings.TrimSpace(part))
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "UNIT" && len(fields) >= 2 {
			tokens = append(tokens, strings.Split(fields[1], "|")...)
			continue
		}
		tokens = append(tokens, fields[0])
	}
	return tokens
}

func TestCommandEnumsAndIsolation(t *testing.T) {
	t.Parallel()

	for _, s := range contract.AllCommandScopes() {
		if !s.Valid() || s.String() == "" {
			t.Errorf("CommandScope %q should be valid", s)
		}
	}
	if contract.CommandScope("invalid").Valid() {
		t.Error("invalid CommandScope reported valid")
	}

	for _, r := range contract.AllRunsAs() {
		if !r.Valid() || r.String() == "" {
			t.Errorf("RunsAs %q should be valid", r)
		}
	}
	if contract.RunsAs("invalid").Valid() {
		t.Error("invalid RunsAs reported valid")
	}

	for _, m := range contract.AllCommandModes() {
		if !m.Valid() || m.String() == "" {
			t.Errorf("CommandMode %q should be valid", m)
		}
	}
	if contract.CommandMode("invalid").Valid() {
		t.Error("invalid CommandMode reported valid")
	}

	for _, c := range contract.AllNonUnitCommands() {
		if !c.Valid() || c.String() == "" {
			t.Errorf("NonUnitCommand %q should be valid", c)
		}
	}
	if contract.NonUnitCommand("invalid").Valid() {
		t.Error("invalid NonUnitCommand reported valid")
	}

	m := contract.CommandMatrix()
	orig := m[0].Command
	m[0].Command = "mutated"
	if contract.CommandMatrix()[0].Command != orig {
		t.Fatal("CommandMatrix returned shared backing slice")
	}
}
