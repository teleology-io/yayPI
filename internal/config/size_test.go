package config

import "testing"

func TestParseByteSize(t *testing.T) {
	cases := map[string]int64{"": 0, "1024": 1024, "4MB": 4 << 20, "4mb": 4 << 20, "512kb": 512 << 10, "1g": 1 << 30, "10 B": 10}
	for in, want := range cases {
		got, err := ParseByteSize(in)
		if err != nil || got != want {
			t.Errorf("ParseByteSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := ParseByteSize("lots"); err == nil {
		t.Error("expected error for garbage input")
	}
}
