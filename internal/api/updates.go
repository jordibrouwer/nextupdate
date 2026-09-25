package api

import (
	"context"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jordibrouwer/nextupdate/internal/changelog"
	"github.com/jordibrouwer/nextupdate/internal/store"
)

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func (s *Server) registerUpdates() {
	s.mux.HandleFunc("GET /api/updates", s.protected(s.handleUpdates))
	s.mux.HandleFunc("GET /api/containers", s.protected(s.handleContainers))
	s.mux.HandleFunc("GET /api/updates/{name}/changelog", s.protected(s.handleChangelog))
	s.mux.HandleFunc("GET /api/history", s.protected(s.handleHistory))
	s.mux.HandleFunc("GET /api/jobs", s.protected(s.handleJobs))
	s.mux.HandleFunc("POST /api/check", s.protected(s.handleCheck))
	s.mux.HandleFunc("POST /api/updates/{name}/apply", s.protected(s.handleApply))
	s.mux.HandleFunc("POST /api/containers/{name}/rollback", s.protected(s.handleRollback))
	s.mux.HandleFunc("PUT /api/containers/{name}/settings", s.protected(s.handleSaveSettings))
}

func (s *Server) busy() map[string]bool {
	m := map[string]bool{}
	for _, j := range s.jobs.Running() {
		m[j.Key] = true
	}
	return m
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (s *Server) handleUpdates(w http.ResponseWriter, r *http.Request) {
	avail, err := s.d.Store.ListAvailable()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read the updates.")
		return
	}
	infos, err := s.d.Store.ListInfo()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read the updates.")
		return
	}
	info := map[string]store.Info{}
	for _, i := range infos {
		info[i.Container] = i
	}
	busy := s.busy()
	out := make([]map[string]any, 0, len(avail))
	for _, a := range avail {
		i := info[a.Container]
		settings, err := s.d.Store.GetSettings(a.Container)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Could not read the settings.")
			return
		}
		out = append(out, map[string]any{
			"container": a.Container, "image": a.Image, "oldVersion": i.OldVersion, "newVersion": i.NewVersion,
			"repo": i.Repo, "breaking": i.Breaking, "reasons": nonNil(i.Reasons), "policy": settings.Policy,
			"detectedAt": a.DetectedAt, "busy": busy[a.Container],
		})
	}
	sort.SliceStable(out, func(x, y int) bool {
		bx, by := out[x]["breaking"].(bool), out[y]["breaking"].(bool)
		if bx != by {
			return bx
		}
		return out[x]["container"].(string) < out[y]["container"].(string)
	})
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleContainers(w http.ResponseWriter, r *http.Request) {
	list, err := s.d.Engine.Containers(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "Could not reach Docker.")
		return
	}
	avail, err := s.d.Store.ListAvailable()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read the updates.")
		return
	}
	has := map[string]bool{}
	for _, a := range avail {
		has[a.Container] = true
	}
	busy := s.busy()
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	out := make([]map[string]any, 0, len(list))
	for _, c := range list {
		st, err := s.d.Store.GetSettings(c.Name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Could not read the settings.")
			return
		}
		out = append(out, map[string]any{
			"name": c.Name, "image": c.Image, "source": string(c.Source), "policy": st.Policy, "httpUrl": st.HTTPURL,
			"repo": st.Repo, "verifyWindowSeconds": int(st.VerifyWindow / time.Second),
			"updateAvailable": has[c.Name], "busy": busy[c.Name],
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleChangelog(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	infos, err := s.d.Store.ListInfo()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read the updates.")
		return
	}
	var info *store.Info
	for i := range infos {
		if infos[i].Container == name {
			info = &infos[i]
		}
	}
	if info == nil {
		writeError(w, http.StatusNotFound, "There is no update available for that container.")
		return
	}
	releases := []map[string]any{}
	if info.Repo != "" && s.d.Changelog != nil {
		all, err := s.d.Changelog.Releases(r.Context(), info.Repo)
		if err != nil {
			s.logf("api: release notes for %s: %v", info.Repo, err)
		}
		for _, rel := range changelog.Between(all, info.OldVersion, info.NewVersion) {
			releases = append(releases, map[string]any{"tag": rel.Tag, "name": rel.Name, "body": rel.Body, "url": rel.URL, "publishedAt": rel.PublishedAt})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"repo": info.Repo, "oldVersion": info.OldVersion, "newVersion": info.NewVersion, "releases": releases})
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "The limit must be a positive number.")
			return
		}
		limit = min(n, 200)
	}
	hist, err := s.d.Store.ListHistory(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read the history.")
		return
	}
	out := make([]map[string]any, 0, len(hist))
	for _, h := range hist {
		out = append(out, map[string]any{
			"id": h.ID, "container": h.Container, "image": h.Image, "fromImage": h.FromImage, "toImage": h.ToImage,
			"startedAt": h.StartedAt, "finishedAt": h.FinishedAt, "outcome": h.Outcome, "reason": h.Reason, "log": nonNil(h.Log),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	failed := s.jobs.Recent()
	if failed == nil {
		failed = []Failure{}
	}
	last, _ := s.d.Store.GetSetting("last_check")
	writeJSON(w, http.StatusOK, map[string]any{"running": s.jobs.Running(), "failed": failed, "lastCheck": last})
}

func (s *Server) handleCheck(w http.ResponseWriter, r *http.Request) {
	ok := s.jobs.Start(s.d.BaseCtx, "check", "check", func(ctx context.Context) error {
		if _, err := s.d.Engine.Check(ctx); err != nil {
			return err
		}
		return s.d.Store.SetSetting("last_check", time.Now().UTC().Format(time.RFC3339))
	})
	if !ok {
		writeError(w, http.StatusConflict, "A check is already running.")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
}

func (s *Server) knownContainer(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := r.PathValue("name")
	list, err := s.d.Engine.Containers(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "Could not reach Docker.")
		return "", false
	}
	for _, c := range list {
		if c.Name == name {
			return name, true
		}
	}
	writeError(w, http.StatusNotFound, "There is no container with that name.")
	return "", false
}

func (s *Server) startContainerJob(w http.ResponseWriter, r *http.Request, kind string, fn func(context.Context, string) error) {
	name, ok := s.knownContainer(w, r)
	if !ok {
		return
	}
	if !s.jobs.Start(s.d.BaseCtx, name, kind, func(ctx context.Context) error { return fn(ctx, name) }) {
		writeError(w, http.StatusConflict, "That container is busy. Wait for the current action to finish.")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
}

func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	s.startContainerJob(w, r, "update", func(ctx context.Context, name string) error {
		_, err := s.d.Engine.Update(ctx, name)
		return err
	})
}

func (s *Server) handleRollback(w http.ResponseWriter, r *http.Request) {
	s.startContainerJob(w, r, "rollback", func(ctx context.Context, name string) error {
		_, err := s.d.Engine.Rollback(ctx, name)
		return err
	})
}

type settingsBody struct {
	Policy              string `json:"policy"`
	HTTPURL             string `json:"httpUrl"`
	Repo                string `json:"repo"`
	VerifyWindowSeconds int    `json:"verifyWindowSeconds"`
}

func (s *Server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	name, ok := s.knownContainer(w, r)
	if !ok {
		return
	}
	var b settingsBody
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	b.HTTPURL, b.Repo = strings.TrimSpace(b.HTTPURL), strings.TrimSpace(b.Repo)
	switch {
	case !store.ValidPolicy(b.Policy):
		writeError(w, http.StatusBadRequest, "The policy must be notify, auto or never.")
	case b.Repo != "" && !repoPattern.MatchString(b.Repo):
		writeError(w, http.StatusBadRequest, "The repository must look like owner/name.")
	case b.HTTPURL != "" && !strings.HasPrefix(b.HTTPURL, "http://") && !strings.HasPrefix(b.HTTPURL, "https://"):
		writeError(w, http.StatusBadRequest, "The health check URL must start with http:// or https://.")
	case b.VerifyWindowSeconds < 0 || b.VerifyWindowSeconds > 3600:
		writeError(w, http.StatusBadRequest, "The verify window must be between 0 and 3600 seconds.")
	default:
		st := store.Settings{Container: name, Policy: b.Policy, HTTPURL: b.HTTPURL, Repo: b.Repo, VerifyWindow: time.Duration(b.VerifyWindowSeconds) * time.Second}
		if err := s.d.Store.SetSettings(st); err != nil {
			writeError(w, http.StatusInternalServerError, "Could not save the settings.")
			return
		}
		writeJSON(w, http.StatusOK, b)
	}
}
