// Package registry compares the digest a container runs with the digest a
// registry serves, without pulling.
package registry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	"github.com/jordibrouwer/nextupdate/internal/docker"
)

// ErrNotPublished means the registry does not serve the image: a local
// build, a private repository without credentials, or a removed tag. Docker
// Hub answers 401 for a repository that does not exist, so these cannot be
// told apart.
var ErrNotPublished = errors.New("image not available in registry")

const (
	LabelVersion = "org.opencontainers.image.version"
	LabelSource  = "org.opencontainers.image.source"
)

// Platform is the OS and CPU a local image was built for. The zero value
// means "this machine".
type Platform struct {
	OS      string
	Arch    string
	Variant string
}

// PlatformOf reads the platform of a local image.
func PlatformOf(img docker.ImageJSON) Platform {
	return Platform{OS: img.Os, Arch: img.Architecture, Variant: img.Variant}
}

type Checker interface {
	RemoteDigest(ctx context.Context, ref string) (string, error)
	RemoteLabels(ctx context.Context, ref string, p Platform) (map[string]string, error)
}

type Remote struct{ opts []remote.Option }

func NewRemote(opts ...remote.Option) *Remote {
	return &Remote{opts: append([]remote.Option{remote.WithAuthFromKeychain(authn.DefaultKeychain)}, opts...)}
}

// RemoteDigest does a manifest HEAD request. On Docker Hub a HEAD does not
// count toward the pull rate limit.
func (r *Remote) RemoteDigest(ctx context.Context, ref string) (string, error) {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", ref, err)
	}
	desc, err := remote.Head(parsed, append([]remote.Option{remote.WithContext(ctx)}, r.opts...)...)
	if err != nil {
		var terr *transport.Error
		if errors.As(err, &terr) {
			switch terr.StatusCode {
			case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
				return "", fmt.Errorf("head %s: %w", ref, ErrNotPublished)
			}
		}
		return "", fmt.Errorf("head %s: %w", ref, err)
	}
	return desc.Digest.String(), nil
}

// RemoteLabels reads the labels of the image config a registry serves. It
// downloads the manifest and the config blob, so call it only once an
// update is known.
func (r *Remote) RemoteLabels(ctx context.Context, ref string, p Platform) (map[string]string, error) {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", ref, err)
	}
	// A multi-platform image has one config per platform, so ask for the one the
	// container runs. The library's default is amd64, which is wrong on arm hosts.
	plat := v1.Platform{OS: "linux", Architecture: runtime.GOARCH}
	if p.OS != "" {
		plat.OS = p.OS
	}
	if p.Arch != "" {
		plat.Architecture = p.Arch
		if p.Arch == "arm" { // only 32-bit arm builds carry a variant that matters; Docker's arm64 "v8" is not in index entries
			plat.Variant = p.Variant
		}
	}
	img, err := remote.Image(parsed, append([]remote.Option{remote.WithContext(ctx), remote.WithPlatform(plat)}, r.opts...)...)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", ref, err)
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", ref, err)
	}
	if cfg == nil || cfg.Config.Labels == nil {
		return map[string]string{}, nil
	}
	return cfg.Config.Labels, nil
}

// LocalLabels returns the labels of a local image.
func LocalLabels(img docker.ImageJSON) map[string]string {
	out := map[string]string{}
	m, _ := img.Config["Labels"].(map[string]any)
	for k, v := range m {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// LocalDigest returns the registry digest of a local image for the
// repository of ref. A locally built image has none.
func LocalDigest(img docker.ImageJSON, ref string) (string, bool) {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return "", false
	}
	repo := parsed.Context().Name()
	for _, rd := range img.RepoDigests {
		d, err := name.NewDigest(rd)
		if err != nil {
			continue
		}
		if d.Context().Name() == repo {
			return d.DigestStr(), true
		}
	}
	return "", false
}
