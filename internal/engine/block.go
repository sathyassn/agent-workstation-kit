package engine

import (
	"fmt"
	"slices"
	"strings"

	"github.com/sathyassn/agent-workstation-kit/internal/contract"
)

// A managed block is the lines from its Begin marker to its End marker. Each
// marker is one whole line that holds the block ID as a word, so RemoveBlock
// finds the block from its ID alone. Everything outside the markers stays
// byte-identical.

func validateBlock(b contract.ManagedBlock) error {
	id := string(b.ID)
	for _, m := range []string{b.Begin, b.End} {
		if m == "" || strings.ContainsAny(m, "\r\n") || !slices.Contains(strings.Fields(m), id) {
			return fmt.Errorf("%w: marker %q must be one line holding the word %q", ErrBlockMarkers, m, id)
		}
	}
	if id == "" || b.Begin == b.End {
		return fmt.Errorf("%w: block %q needs an ID and distinct markers", ErrBlockMarkers, id)
	}
	for _, line := range strings.Split(b.Body, "\n") {
		if slices.Contains(strings.Fields(line), id) {
			return fmt.Errorf("%w: body of block %q repeats its ID", ErrBlockMarkers, id)
		}
	}
	return nil
}

// markerLines returns the indexes of lines that hold id as a word.
func markerLines(lines []string, id contract.BlockID) []int {
	var idx []int
	for i, l := range lines {
		if slices.Contains(strings.Fields(l), string(id)) {
			idx = append(idx, i)
		}
	}
	return idx
}

// ensureBlock returns content with block b in place: replaced if present,
// appended if not.
func ensureBlock(content string, b contract.ManagedBlock) (string, error) {
	if err := validateBlock(b); err != nil {
		return "", err
	}
	body := b.Body
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	block := b.Begin + "\n" + body + b.End + "\n"
	lines := strings.SplitAfter(content, "\n")
	switch idx := markerLines(lines, b.ID); len(idx) {
	case 0:
		if content != "" && !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		return content + block, nil
	case 2:
		if strings.TrimRight(lines[idx[0]], "\r\n") != b.Begin || strings.TrimRight(lines[idx[1]], "\r\n") != b.End {
			return "", fmt.Errorf("%w: block %q markers differ from the requested ones", ErrBlockMarkers, b.ID)
		}
		end := lines[idx[1]]
		if !strings.HasSuffix(end, "\n") {
			block = strings.TrimSuffix(block, "\n")
		}
		return strings.Join(lines[:idx[0]], "") + block + strings.Join(lines[idx[1]+1:], ""), nil
	default:
		return "", fmt.Errorf("%w: block %q has %d marker lines", ErrBlockMarkers, b.ID, len(idx))
	}
}

// removeBlock returns content without block id. A missing block is no
// change.
func removeBlock(content string, id contract.BlockID) (string, error) {
	lines := strings.SplitAfter(content, "\n")
	switch idx := markerLines(lines, id); len(idx) {
	case 0:
		return content, nil
	case 2:
		return strings.Join(lines[:idx[0]], "") + strings.Join(lines[idx[1]+1:], ""), nil
	default:
		return "", fmt.Errorf("%w: block %q has %d marker lines", ErrBlockMarkers, id, len(idx))
	}
}
