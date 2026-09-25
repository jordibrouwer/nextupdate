// Package verify decides whether a freshly started container works.
package verify

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jordibrouwer/nextupdate/internal/docker"
)

type Check struct {
	Window      time.Duration
	Interval    time.Duration
	HTTPURL     string
	MaxRestarts int
}

type Result struct {
	OK     bool
	Reason string
}

func fail(format string, a ...any) Result { return Result{Reason: fmt.Sprintf(format, a...)} }

func Verify(ctx context.Context, api docker.API, id string, c Check) Result {
	if c.MaxRestarts <= 0 {
		c.MaxRestarts = 3
	}
	if c.Interval <= 0 {
		c.Interval = time.Second
	}
	deadline := time.Now().Add(c.Window)
	for {
		st, err := api.InspectContainer(ctx, id)
		if err != nil {
			return fail("inspect: %v", err)
		}
		if st.RestartCount >= c.MaxRestarts {
			return fail("crashloop: %d restarts", st.RestartCount)
		}
		if !st.State.Running && st.State.Status != "restarting" && st.State.Status != "created" {
			return fail("container exited (status %s)", st.State.Status)
		}
		if h := st.State.Health; h != nil {
			if h.Status == "healthy" {
				break
			}
			if h.Status == "unhealthy" {
				return fail("healthcheck unhealthy")
			}
		}
		if time.Now().After(deadline) {
			if st.State.Health != nil {
				return fail("healthcheck not healthy within %s", c.Window)
			}
			break
		}
		select {
		case <-ctx.Done():
			return fail("cancelled: %v", ctx.Err())
		case <-time.After(c.Interval):
		}
	}
	if c.HTTPURL != "" {
		return checkHTTP(ctx, c.HTTPURL)
	}
	return Result{OK: true}
}

func checkHTTP(ctx context.Context, url string) Result {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fail("http check: %v", err)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return fail("http check: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fail("http check: status %d", resp.StatusCode)
	}
	return Result{OK: true}
}
