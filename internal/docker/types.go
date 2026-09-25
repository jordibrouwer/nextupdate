// Package docker is a small client for the parts of the Docker Engine API
// nextupdate needs.
package docker

import (
	"context"
	"encoding/json"
)

type ContainerSummary struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	ImageID string            `json:"ImageID"`
	Labels  map[string]string `json:"Labels"`
	State   string            `json:"State"`
}

type Health struct {
	Status string `json:"Status"`
}

type ContainerState struct {
	Status  string  `json:"Status"`
	Running bool    `json:"Running"`
	Health  *Health `json:"Health,omitempty"`
}

type EndpointSettings struct {
	Aliases    []string          `json:"Aliases,omitempty"`
	IPAMConfig json.RawMessage   `json:"IPAMConfig,omitempty"`
	Links      []string          `json:"Links,omitempty"`
	DriverOpts map[string]string `json:"DriverOpts,omitempty"`
	MacAddress string            `json:"MacAddress,omitempty"`
}

type NetworkSettings struct {
	Networks map[string]EndpointSettings `json:"Networks"`
}

// ContainerJSON keeps Config as a generic map and HostConfig as raw JSON,
// so a recreate passes on every field, including ones this code never names.
type ContainerJSON struct {
	ID              string          `json:"Id"`
	Name            string          `json:"Name"`
	Image           string          `json:"Image"`
	RestartCount    int             `json:"RestartCount"`
	State           ContainerState  `json:"State"`
	Config          map[string]any  `json:"Config"`
	HostConfig      json.RawMessage `json:"HostConfig"`
	NetworkSettings NetworkSettings `json:"NetworkSettings"`
}

type ImageJSON struct {
	ID          string         `json:"Id"`
	RepoDigests []string       `json:"RepoDigests"`
	Config      map[string]any `json:"Config"`
}

type NetworkingConfig struct {
	EndpointsConfig map[string]EndpointSettings `json:"EndpointsConfig"`
}

type CreateSpec struct {
	Config           map[string]any
	HostConfig       json.RawMessage
	NetworkingConfig NetworkingConfig
}

type API interface {
	ListContainers(ctx context.Context) ([]ContainerSummary, error)
	InspectContainer(ctx context.Context, id string) (ContainerJSON, error)
	InspectImage(ctx context.Context, ref string) (ImageJSON, error)
	PullImage(ctx context.Context, ref string) error
	TagImage(ctx context.Context, id, repo, tag string) error
	CreateContainer(ctx context.Context, name string, spec CreateSpec) (string, error)
	StartContainer(ctx context.Context, id string) error
	StopContainer(ctx context.Context, id string) error
	RenameContainer(ctx context.Context, id, newName string) error
	RemoveContainer(ctx context.Context, id string) error
}
