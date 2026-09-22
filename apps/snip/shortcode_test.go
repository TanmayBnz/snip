package main

import "testing"

func TestEncode(t *testing.T) {
	cases := []struct {
		id   int64
		want string
	}{
		{1, "1"},
		{9, "9"},
		{10, "a"},
		{35, "z"},
		{36, "A"},
		{61, "Z"},
		{62, "10"},
		{124, "20"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			got := Encode(c.id)
			if got != c.want {
				t.Errorf("Encode(%d) == %q, want %q", c.id, got, c.want)
			}
		})
	}
}

func TestEncode_Uniqueness(t *testing.T) {
	seen := map[string]bool{}
	for id := int64(1); id <= 5000; id++ {
		code := Encode(id)
		if seen[code] {
			t.Fatalf("Encode(%d) produced duplicate code %q", id, code)
		}
		seen[code] = true
	}
}
