package files

import "testing"

func TestDefaultReserveFitsSmallDiskAndRemainsNonzero(t *testing.T) {
	m, _, _ := testManager(t)
	if m.reserve < 16<<20 || m.reserve > (1<<30)/20 {
		t.Fatalf("small disk default reserve = %d", m.reserve)
	}
}
