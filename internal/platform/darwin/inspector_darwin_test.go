package darwin

import "testing"

func TestNewContainmentChecksBinaries(t *testing.T) {
	if _, err := NewContainment(privateDir(t)); err != nil {
		t.Fatalf("NewContainment on this host: %v", err)
	}
}
