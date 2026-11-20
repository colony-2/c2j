package probe

import "testing"

func TestCallsC(t *testing.T) {
	if got := Answer(7); got != 42 {
		t.Fatalf("C answer = %d, want 42", got)
	}
}
