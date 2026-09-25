package semver

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		want Version
		ok   bool
	}{
		{"1.2.3", Version{1, 2, 3, ""}, true},
		{"v1.2.3", Version{1, 2, 3, ""}, true},
		{"2", Version{2, 0, 0, ""}, true},
		{"2.1", Version{2, 1, 0, ""}, true},
		{"1.2.3-rc.1", Version{1, 2, 3, "rc.1"}, true},
		{"1.2.3+build5", Version{1, 2, 3, ""}, true},
		{"", Version{}, false},
		{"latest", Version{}, false},
		{"1.2.3.4", Version{}, false},
		{"1.x.3", Version{}, false},
		{"-1.2.3", Version{}, false},
	}
	for _, c := range cases {
		got, ok := Parse(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("Parse(%q) = %+v, %v; want %+v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestCompare(t *testing.T) {
	v := func(s string) Version { x, _ := Parse(s); return x }
	cases := []struct {
		a, b string
		want int
	}{
		{"1.2.3", "1.2.3", 0},
		{"1.2.4", "1.2.3", 1},
		{"1.10.0", "1.9.0", 1},
		{"2.0.0", "10.0.0", -1},
		{"1.2.3-rc.1", "1.2.3", -1},
		{"1.2.3", "1.2.3-rc.1", 1},
		{"1.2.3-alpha", "1.2.3-beta", -1},
	}
	for _, c := range cases {
		if got := Compare(v(c.a), v(c.b)); got != c.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestDiff(t *testing.T) {
	v := func(s string) Version { x, _ := Parse(s); return x }
	cases := []struct {
		from, to string
		want     Change
	}{
		{"1.2.3", "1.2.3", None},
		{"1.2.3", "1.2.4", Patch},
		{"1.2.3", "1.3.0", Minor},
		{"1.2.3", "2.0.0", Major},
		{"1.2.3", "1.2.2", Downgrade},
		{"1.2.3", "1.2.4-rc.1", Patch},
		{"0.4.1", "0.4.2", Patch},
		{"0.4.1", "0.5.0", Major},
		{"0.9.0", "1.0.0", Major},
	}
	for _, c := range cases {
		if got := Diff(v(c.from), v(c.to)); got != c.want {
			t.Errorf("Diff(%s, %s) = %s, want %s", c.from, c.to, got, c.want)
		}
	}
}

func TestString(t *testing.T) {
	if got := (Version{1, 2, 3, "rc.1"}).String(); got != "1.2.3-rc.1" {
		t.Errorf("got %q", got)
	}
	if got := (Version{1, 2, 3, ""}).String(); got != "1.2.3" {
		t.Errorf("got %q", got)
	}
}
