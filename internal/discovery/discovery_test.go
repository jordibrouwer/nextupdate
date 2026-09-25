package discovery

import (
	"context"
	"testing"

	"github.com/jordibrouwer/nextupdate/internal/docker"
	"github.com/jordibrouwer/nextupdate/internal/dockertest"
)

func TestDiscover(t *testing.T) {
	f := dockertest.New()
	f.AddImage("app:latest", docker.ImageJSON{ID: "sha256:app"})
	f.AddImage("web:1", docker.ImageJSON{ID: "sha256:web"})
	f.AddContainer("c1", "app", "app:latest", nil, true)
	f.AddContainer("c2", "stack-web-1", "web:1", map[string]string{
		LabelProject: "stack", LabelService: "web",
		LabelWorkdir: "/srv/stack", LabelFiles: "/srv/stack/compose.yml,/srv/stack/override.yml",
	}, true)
	f.AddContainer("c3", "app"+OldSuffix, "app:latest", nil, false)
	// Tag moved away from the running image: list shows the ID, inspect keeps the ref.
	f.AddContainer("c4", "moved", "app:latest", nil, true)

	got, err := Discover(context.Background(), &movedTagAPI{Fake: f, id: "c4"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 containers (old copy skipped), got %+v", got)
	}
	byName := map[string]Container{}
	for _, c := range got {
		byName[c.Name] = c
	}
	if c := byName["app"]; c.Source != SourceRun || c.Image != "app:latest" || c.ImageID != "sha256:app" {
		t.Fatalf("app: %+v", c)
	}
	web := byName["stack-web-1"]
	if web.Source != SourceCompose || web.ComposeProject != "stack" || web.ComposeService != "web" ||
		web.ComposeWorkdir != "/srv/stack" || len(web.ComposeFiles) != 2 {
		t.Fatalf("web: %+v", web)
	}
	if c := byName["moved"]; c.Image != "app:latest" {
		t.Fatalf("moved tag not resolved from inspect: %+v", c)
	}
}

// movedTagAPI reports an image ID in the list for one container, as Docker
// does when the tag now points at a newer image.
type movedTagAPI struct {
	*dockertest.Fake
	id string
}

func (m *movedTagAPI) ListContainers(ctx context.Context) ([]docker.ContainerSummary, error) {
	list, err := m.Fake.ListContainers(ctx)
	for i := range list {
		if list[i].ID == m.id {
			list[i].Image = list[i].ImageID
		}
	}
	return list, err
}
