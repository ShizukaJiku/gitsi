package module

import "testing"

func TestIndexOnlyTakesShortPositiveNumbers(t *testing.T) {
	for arg, want := range map[string]int{"1": 1, "12": 12, "999": 999} {
		if n, ok := Index(arg); !ok || n != want {
			t.Errorf("%q: %d %v", arg, n, ok)
		}
	}
	for _, arg := range []string{"", "0", "1000", "1a", "a1b2c3d4", "-1", "12345678"} {
		if _, ok := Index(arg); ok {
			t.Errorf("%q is not a list number", arg)
		}
	}
}
