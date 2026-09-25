package semver

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		want Version
		ok   bool
	}{
		{"1.2.3", Version{Major: 1, Minor: 2, Patch: 3, Pre: ""}, true},
		{"v1.2.3", Version{Major: 1, Minor: 2, Patch: 3, Pre: ""}, true},
		{"2", Version{Major: 2, Minor: 0, Patch: 0, Pre: ""}, true},
		{"2.1", Version{Major: 2, Minor: 1, Patch: 0, Pre: ""}, true},
		{"1.2.3-rc.1", Version{Major: 1, Minor: 2, Patch: 3, Pre: "rc.1"}, true},
		{"1.2.3+build5", Version{Major: 1, Minor: 2, Patch: 3, Pre: ""}, true},
		{"", Version{}, false},
		{"latest", Version{}, false},
		{"1.2.3.4", Version{Major: 1, Minor: 2, Patch: 3, Rev: 4, hasRev: true}, true},
		{"1.x.3", Version{}, false},
		{"-1.2.3", Version{}, false},
		// linuxserver.io style: four numbers and an image build suffix
		{"4.0.20.3014-ls325", Version{Major: 4, Minor: 0, Patch: 20, Rev: 3014, Build: 325, hasRev: true}, true},
		{"v1.26.3-r0-ls345", Version{Major: 1, Minor: 26, Patch: 3, Build: 345}, true},
		{"2024.05.1-ls7", Version{Major: 2024, Minor: 5, Patch: 1, Build: 7}, true},
		{"1.2.3-ls", Version{Major: 1, Minor: 2, Patch: 3, Pre: "ls"}, true}, // no build number: an ordinary pre-release tag
		{"1.2.3-ls5-rc.1", Version{Major: 1, Minor: 2, Patch: 3, Pre: "ls5-rc.1"}, true},
		{"1.2.3.4.5", Version{}, false},
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
		{"4.0.20.3014-ls325", "4.0.20.3014-ls326", -1},
		{"4.0.20.3014-ls326", "4.0.20.3014-ls325", 1},
		{"4.0.20.3014-ls999", "4.0.20.3020-ls1", -1}, // the fourth number outranks the build
		{"4.0.20.3014", "4.0.20.3014-ls5", -1},
		{"4.0.20.3014-ls5", "4.0.20.3014-ls5", 0},
		{"1.2.3", "1.2.3.0", 0},
		{"4.0.9", "4.0.20.3014", -1},
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
		{"4.0.20.3014-ls325", "4.0.20.3014-ls326", Patch}, // the image was rebuilt
		{"4.0.20.3014-ls325", "4.0.21.3020-ls326", Patch},
		{"4.0.20.3014-ls325", "4.1.0.1-ls1", Minor},
		{"4.0.20.3014-ls325", "5.0.0.1-ls1", Major},
		{"4.0.20.3020-ls9", "4.0.20.3014-ls325", Downgrade},
	}
	for _, c := range cases {
		if got := Diff(v(c.from), v(c.to)); got != c.want {
			t.Errorf("Diff(%s, %s) = %s, want %s", c.from, c.to, got, c.want)
		}
	}
}

func TestString(t *testing.T) {
	for _, in := range []string{"4.0.20.3014-ls325", "1.2.3-ls7", "1.2.3.4", "1.2.3-rc.1"} {
		v, ok := Parse(in)
		if !ok || v.String() != in {
			t.Errorf("Parse(%q).String() = %q", in, v.String())
		}
	}
	if v, _ := Parse("1.26.3-r0-ls345"); v.String() != "1.26.3-ls345" {
		t.Errorf("the alpine revision is dropped when printing, got %q", v.String())
	}
	if got := (Version{Major: 1, Minor: 2, Patch: 3, Pre: "rc.1"}).String(); got != "1.2.3-rc.1" {
		t.Errorf("got %q", got)
	}
	if got := (Version{Major: 1, Minor: 2, Patch: 3, Pre: ""}).String(); got != "1.2.3" {
		t.Errorf("got %q", got)
	}
}
