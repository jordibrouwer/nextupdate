package policy

import (
	"strings"
	"testing"

	"github.com/jordibrouwer/nextupdate/internal/store"
)

func TestDecide(t *testing.T) {
	info := func(oldV, newV string, breaking bool, reasons ...string) store.Info {
		return store.Info{OldVersion: oldV, NewVersion: newV, Breaking: breaking, Reasons: reasons}
	}
	cases := []struct {
		name   string
		policy string
		image  string
		info   store.Info
		want   Action
		why    string
	}{
		{"never", store.PolicyNever, "app:1", info("1.0.0", "1.0.1", false), Skip, "never"},
		{"notify", store.PolicyNotify, "app:1", info("1.0.0", "1.0.1", false), Notify, "notify"},
		{"empty policy behaves as notify", "", "app:1", info("1.0.0", "1.0.1", false), Notify, "notify"},
		{"auto patch", store.PolicyAuto, "app:1", info("1.0.0", "1.0.1", false), Update, ""},
		{"auto minor", store.PolicyAuto, "app:1", info("1.0.0", "1.1.0", false), Update, ""},
		{"auto same version, new digest", store.PolicyAuto, "app:1", info("1.0.0", "1.0.0", false), Update, ""},
		{"auto major", store.PolicyAuto, "app:1", info("1.0.0", "2.0.0", false), Notify, "major"},
		{"auto breaking", store.PolicyAuto, "app:1", info("1.0.0", "1.1.0", true, "Release v1.1.0 mentions \"breaking\"."), Notify, "Breaking"},
		{"auto unknown version", store.PolicyAuto, "app:latest", info("", "", false), Notify, "unknown"},
		{"auto protected", store.PolicyAuto, "postgres:16", info("16.1.0", "16.2.0", false), Notify, "protected"},
		// linuxserver.io versions: four numbers and a build suffix
		{"auto linuxserver rebuild", store.PolicyAuto, "lscr.io/linuxserver/sonarr:latest", info("4.0.20.3014-ls325", "4.0.20.3014-ls326", false), Update, ""},
		{"auto linuxserver patch", store.PolicyAuto, "lscr.io/linuxserver/sonarr:latest", info("4.0.20.3014-ls325", "4.0.21.3020-ls326", false), Update, ""},
		{"auto linuxserver major", store.PolicyAuto, "lscr.io/linuxserver/sonarr:latest", info("4.0.20.3014-ls325", "5.0.0.1-ls1", false), Notify, "major"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, why := Decide(store.Settings{Container: "app", Policy: c.policy}, c.info, c.image)
			if got != c.want || !strings.Contains(why, c.why) {
				t.Fatalf("got %v %q, want %v containing %q", got, why, c.want, c.why)
			}
		})
	}
}

func TestProtected(t *testing.T) {
	for _, img := range []string{"postgres:16", "docker.io/library/mariadb", "ghcr.io/tecnativa/docker-socket-proxy:latest", "redis", "mongo:7"} {
		if !Protected(img) {
			t.Errorf("%s should be protected", img)
		}
	}
	for _, img := range []string{"linuxserver/sonarr", "ghcr.io/immich-app/immich-server", "nginx", "not a ref!"} {
		if Protected(img) {
			t.Errorf("%s should not be protected", img)
		}
	}
}
