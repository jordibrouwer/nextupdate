package api

import (
	"context"
	"sync"
	"time"
)

type Job struct {
	Key   string    `json:"key"`
	Kind  string    `json:"kind"`
	Since time.Time `json:"since"`
}

type Failure struct {
	Key   string    `json:"key"`
	Kind  string    `json:"kind"`
	Error string    `json:"error"`
	At    time.Time `json:"at"`
}

// Jobs runs background work, one job per key at a time, and remembers the
// last failures so the UI can show why something did not happen.
type Jobs struct {
	mu      sync.Mutex
	running map[string]Job
	failed  []Failure
	wg      sync.WaitGroup
}

// Start runs fn in the background. It returns false when a job with the same
// key is already running.
func (j *Jobs) Start(ctx context.Context, key, kind string, fn func(context.Context) error) bool {
	j.mu.Lock()
	if j.running == nil {
		j.running = map[string]Job{}
	}
	if _, busy := j.running[key]; busy {
		j.mu.Unlock()
		return false
	}
	j.running[key] = Job{Key: key, Kind: kind, Since: time.Now()}
	j.wg.Add(1)
	j.mu.Unlock()

	go func() {
		defer j.wg.Done()
		err := fn(ctx)
		j.mu.Lock()
		defer j.mu.Unlock()
		delete(j.running, key)
		if err != nil {
			j.failed = append(j.failed, Failure{Key: key, Kind: kind, Error: err.Error(), At: time.Now()})
			if len(j.failed) > 20 {
				j.failed = j.failed[len(j.failed)-20:]
			}
		}
	}()
	return true
}

func (j *Jobs) Running() []Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]Job, 0, len(j.running))
	for _, r := range j.running {
		out = append(out, r)
	}
	return out
}

func (j *Jobs) Recent() []Failure {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]Failure(nil), j.failed...)
}

func (j *Jobs) Wait() { j.wg.Wait() }
