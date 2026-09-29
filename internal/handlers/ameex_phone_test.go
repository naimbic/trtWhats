package handlers

import "testing"

// TRT #63: Ameex requires the Moroccan local format 0XXXXXXXXX.
func TestAmeexLocalPhone(t *testing.T) {
	cases := map[string]string{
		"212721050753":   "0721050753",
		"+212 7 21 05 07 53": "0721050753",
		"00212612345678": "0612345678",
		"0612345678":     "0612345678",
		"612345678":      "0612345678",
		"":               "",
	}
	for in, want := range cases {
		if got := ameexLocalPhone(in); got != want {
			t.Errorf("ameexLocalPhone(%q) = %q, want %q", in, got, want)
		}
	}
}
