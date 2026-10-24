package timefmt

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"
)

var ref = time.Date(2026, 10, 3, 21, 8, 7, 123456789, time.UTC)

func TestRender(t *testing.T) {
	berlin, _ := time.LoadLocation("Europe/Berlin")
	cases := []struct {
		spec string
		loc  *time.Location
		want any
	}{
		{"rfc3339", nil, "2026-10-03T21:08:07Z"},
		{"rfc3339_ms", nil, "2026-10-03T21:08:07.123Z"},
		{"rfc3339_nano", nil, "2026-10-03T21:08:07.123456789Z"},
		{"rfc3339", berlin, "2026-10-03T23:08:07+02:00"},
		{"unix", nil, int64(1791061687)},
		{"unix_ms", nil, int64(1791061687123)},
		{"date", berlin, "2026-10-03"},
		{"YYYY-MM-DD HH:mm:ss", nil, "2026-10-03 21:08:07"},
		{"D MMM YYYY, h:mm A", nil, "3 Oct 2026, 9:08 PM"},
		{"dddd, MMMM D", nil, "Saturday, October 3"},
		{"DD/MM/YY HH:mm:ss.SSS ZZ", berlin, "03/10/26 23:08:07.123 +0200"},
		{"YYYY-MM-DD[T]HH:mm:ssZ", nil, "2026-10-03T21:08:07Z"},
		{"[Week of] YYYY-MM-DD", berlin, "Week of 2026-10-03"},
	}
	for _, c := range cases {
		f, err := New(c.spec, c.loc)
		if err != nil {
			t.Fatalf("%s: %v", c.spec, err)
		}
		if got := f.Render(ref); got != c.want {
			t.Errorf("%s: got %#v, want %#v", c.spec, got, c.want)
		}
	}
}

func TestParseRoundTrips(t *testing.T) {
	second := ref.Truncate(time.Second)
	cases := []struct {
		spec string
		want time.Time
	}{
		{"rfc3339", second},
		{"rfc3339_ms", ref.Truncate(time.Millisecond)},
		{"unix", second},
		{"unix_ms", ref.Truncate(time.Millisecond)},
		{"YYYY-MM-DD HH:mm:ss", second},
		{"D MMM YYYY, h:mm:ss A", second},
		{"DD/MM/YY HH:mm:ss.SSS ZZ", ref.Truncate(time.Millisecond)},
		{"date", time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		f, _ := New(c.spec, nil)
		out := f.Render(ref)
		if n, ok := out.(int64); ok {
			out = json.Number(strconv.FormatInt(n, 10))
		}
		got, ok := f.Parse(out)
		if !ok || !got.Equal(c.want) {
			t.Errorf("%s: Parse(%v) = %v, %v; want %v", c.spec, out, got, ok, c.want)
		}
	}
}

func TestParseRejects(t *testing.T) {
	f, _ := New("YYYY-MM-DD HH:mm:ss", nil)
	for _, s := range []string{"2026-02-30 10:00:00", "2026-10-03", "2026-10-03 10:00:00 extra", "26-10-03 10:00:00"} {
		if _, ok := f.Parse(s); ok {
			t.Errorf("accepted %q", s)
		}
	}
	for _, spec := range []string{"not a format", "unixms", "rfc3339x"} {
		if _, err := New(spec, nil); err == nil {
			t.Errorf("%q should be refused", spec)
		}
	}
	if _, err := New("YYYY-[MM", nil); err == nil {
		t.Error("an unclosed literal should be refused")
	}
}

func TestFromDriver(t *testing.T) {
	want := time.Date(2026, 10, 23, 22, 38, 7, 0, time.UTC)
	for _, v := range []any{
		"2026-10-23 22:38:07",           // SQLite CURRENT_TIMESTAMP
		"2026-10-23 22:38:07+00:00",     // modernc time.Time
		"2026-10-23 22:38:07 +0000 UTC", // time.Time String(), stored by older yayPI
		[]byte("2026-10-23 22:38:07"),
		"2026-10-23T22:38:07Z",
		want,
	} {
		got, ok := FromDriver(v)
		if !ok || !got.Equal(want) {
			t.Errorf("FromDriver(%#v) = %v, %v", v, got, ok)
		}
	}
	if _, ok := FromDriver(int64(5)); ok {
		t.Error("an integer is not a driver time")
	}
}
