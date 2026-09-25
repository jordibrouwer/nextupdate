package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
)

const apiVersion = "v1.44"

var ErrNotFound = errors.New("not found")
var ErrConflict = errors.New("conflict")

type Client struct {
	hc   *http.Client
	base string
}

var _ API = (*Client)(nil)

func New(host string) (*Client, error) {
	if host == "" {
		host = "unix:///var/run/docker.sock"
	}
	u, err := url.Parse(host)
	if err != nil {
		return nil, fmt.Errorf("parse DOCKER_HOST: %w", err)
	}
	switch u.Scheme {
	case "unix":
		sock := u.Path
		tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}}
		return &Client{hc: &http.Client{Transport: tr}, base: "http://docker"}, nil
	case "tcp", "http":
		return &Client{hc: &http.Client{}, base: "http://" + u.Host}, nil
	default:
		return nil, fmt.Errorf("unsupported DOCKER_HOST scheme %q", u.Scheme)
	}
}

// do sends a request. out is nil (discard body), a func(io.Reader) error
// (stream the body), or a pointer to decode JSON into.
func (c *Client) do(ctx context.Context, method, path string, q url.Values, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	u := c.base + "/" + apiVersion + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("docker %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotModified:
		return nil
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("docker %s %s: %w", method, path, ErrNotFound)
	case resp.StatusCode == http.StatusConflict:
		var e struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return fmt.Errorf("docker %s %s: %s: %w", method, path, e.Message, ErrConflict)
	case resp.StatusCode >= 300:
		var e struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return fmt.Errorf("docker %s %s: %d %s", method, path, resp.StatusCode, e.Message)
	}
	switch o := out.(type) {
	case nil:
		_, err := io.Copy(io.Discard, resp.Body)
		return err
	case func(io.Reader) error:
		return o(resp.Body)
	default:
		return json.NewDecoder(resp.Body).Decode(o)
	}
}

func (c *Client) ListContainers(ctx context.Context) ([]ContainerSummary, error) {
	var out []ContainerSummary
	err := c.do(ctx, http.MethodGet, "/containers/json", url.Values{"all": {"1"}}, nil, &out)
	return out, err
}

func (c *Client) InspectContainer(ctx context.Context, id string) (ContainerJSON, error) {
	var out ContainerJSON
	err := c.do(ctx, http.MethodGet, "/containers/"+id+"/json", nil, nil, &out)
	return out, err
}

func (c *Client) InspectImage(ctx context.Context, ref string) (ImageJSON, error) {
	var out ImageJSON
	err := c.do(ctx, http.MethodGet, "/images/"+ref+"/json", nil, nil, &out)
	return out, err
}

func (c *Client) PullImage(ctx context.Context, ref string) error {
	repo, tag := SplitRef(ref)
	q := url.Values{"fromImage": {repo}, "tag": {tag}}
	return c.do(ctx, http.MethodPost, "/images/create", q, nil, func(r io.Reader) error {
		dec := json.NewDecoder(r)
		for {
			var m struct {
				Error string `json:"error"`
			}
			if err := dec.Decode(&m); err == io.EOF {
				return nil
			} else if err != nil {
				return fmt.Errorf("read pull stream: %w", err)
			}
			if m.Error != "" {
				return fmt.Errorf("pull %s: %s", ref, m.Error)
			}
		}
	})
}

func (c *Client) TagImage(ctx context.Context, id, repo, tag string) error {
	return c.do(ctx, http.MethodPost, "/images/"+id+"/tag", url.Values{"repo": {repo}, "tag": {tag}}, nil, nil)
}

func (c *Client) CreateContainer(ctx context.Context, name string, spec CreateSpec) (string, error) {
	body := make(map[string]any, len(spec.Config)+2)
	for k, v := range spec.Config {
		body[k] = v
	}
	if len(spec.HostConfig) > 0 {
		body["HostConfig"] = spec.HostConfig
	}
	body["NetworkingConfig"] = spec.NetworkingConfig
	var out struct {
		ID string `json:"Id"`
	}
	err := c.do(ctx, http.MethodPost, "/containers/create", url.Values{"name": {name}}, body, &out)
	return out.ID, err
}

func (c *Client) StartContainer(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/containers/"+id+"/start", nil, nil, nil)
}

func (c *Client) StopContainer(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/containers/"+id+"/stop", url.Values{"t": {"10"}}, nil, nil)
}

func (c *Client) RenameContainer(ctx context.Context, id, newName string) error {
	return c.do(ctx, http.MethodPost, "/containers/"+id+"/rename", url.Values{"name": {newName}}, nil, nil)
}

func (c *Client) RemoveContainer(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/containers/"+id, url.Values{"force": {"1"}}, nil, nil)
}

func (c *Client) RemoveImage(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/images/"+id, nil, nil, nil)
}
