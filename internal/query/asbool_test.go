package query

import "testing"

func TestAsBool(t *testing.T) {
	cases := []struct {
		in   any
		want any
	}{
		{int64(1), true}, {int64(0), false}, {1, true}, {[]byte("1"), true}, {"0", false},
		{"true", true}, {true, true}, {nil, nil}, {"maybe", "maybe"},
	}
	for _, c := range cases {
		if got := AsBool(c.in); got != c.want {
			t.Errorf("AsBool(%#v) = %#v, want %#v", c.in, got, c.want)
		}
	}
}
