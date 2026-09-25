package classify

import (
	"strings"
	"testing"

	"github.com/jordibrouwer/nextupdate/internal/changelog"
)

func TestClassify(t *testing.T) {
	rel := func(tag, body string) changelog.Release { return changelog.Release{Tag: tag, Body: body} }
	cases := []struct {
		name     string
		oldV     string
		newV     string
		releases []changelog.Release
		flagged  []string
		breaking bool
		reason   string
	}{
		{"patch is fine", "1.4.0", "1.4.1", []changelog.Release{rel("v1.4.1", "Fixes a typo.")}, nil, false, ""},
		{"major jump", "1.9.0", "2.0.0", nil, nil, true, "Major version"},
		{"downgrade", "2.0.0", "1.9.0", nil, nil, true, "older than"},
		{"keyword in notes", "1.4.0", "1.5.0", []changelog.Release{rel("v1.5.0", "BREAKING: config keys renamed")}, nil, true, `mentions "breaking"`},
		{"migration keyword", "1.4.0", "1.5.0", []changelog.Release{rel("v1.5.0", "Run the database Migration first")}, nil, true, `mentions "migration"`},
		{"flagged version in range", "1.4.0", "1.6.0", nil, []string{"1.5.0"}, true, "community mapping"},
		{"flagged version already passed", "1.5.0", "1.6.0", nil, []string{"1.5.0"}, false, ""},
		{"flagged, old unknown, equals new", "", "1.5.0", nil, []string{"v1.5.0"}, true, "community mapping"},
		{"unknown versions, quiet notes", "", "", nil, nil, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Classify(c.oldV, c.newV, c.releases, c.flagged)
			if got.Breaking != c.breaking {
				t.Fatalf("breaking = %v, want %v (%v)", got.Breaking, c.breaking, got.Reasons)
			}
			if c.breaking && !strings.Contains(strings.Join(got.Reasons, "|"), c.reason) {
				t.Fatalf("reasons %v do not contain %q", got.Reasons, c.reason)
			}
			if !c.breaking && len(got.Reasons) != 0 {
				t.Fatalf("reasons on a clean update: %v", got.Reasons)
			}
		})
	}
}

func TestOneReasonPerRelease(t *testing.T) {
	got := Classify("1.0.0", "1.1.0", []changelog.Release{{Tag: "v1.1.0", Body: "breaking and deprecated and migration"}}, nil)
	if len(got.Reasons) != 1 {
		t.Fatalf("want one reason for one release, got %v", got.Reasons)
	}
}
