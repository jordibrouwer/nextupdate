package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jordibrouwer/nextupdate/internal/notify"
	"github.com/jordibrouwer/nextupdate/internal/push"
	"github.com/jordibrouwer/nextupdate/internal/store"
)

const secretMask = "********"

func (s *Server) registerNotifiers() {
	s.mux.HandleFunc("GET /api/notifiers/types", s.protected(s.handleNotifierTypes))
	s.mux.HandleFunc("GET /api/notifiers", s.protected(s.handleNotifierList))
	s.mux.HandleFunc("POST /api/notifiers", s.protected(s.handleNotifierCreate))
	s.mux.HandleFunc("PUT /api/notifiers/{id}", s.protected(s.handleNotifierUpdate))
	s.mux.HandleFunc("DELETE /api/notifiers/{id}", s.protected(s.handleNotifierDelete))
	s.mux.HandleFunc("POST /api/notifiers/{id}/test", s.protected(s.handleNotifierTest))
	s.mux.HandleFunc("GET /api/push/key", s.protected(s.handlePushKey))
	s.mux.HandleFunc("POST /api/push/subscribe", s.protected(s.handlePushSubscribe))
	s.mux.HandleFunc("POST /api/push/unsubscribe", s.protected(s.handlePushUnsubscribe))
	s.mux.HandleFunc("POST /api/push/test", s.protected(s.handlePushTest))
	s.mux.HandleFunc("GET /api/widget/token", s.protected(s.handleWidgetToken))
	s.mux.HandleFunc("POST /api/widget/token/rotate", s.protected(s.handleWidgetRotate))
	s.mux.HandleFunc("GET /api/widget", s.handleWidget)
}

func (s *Server) handleNotifierTypes(w http.ResponseWriter, r *http.Request) {
	out := []map[string]any{}
	for _, t := range notify.Types() {
		fields := []map[string]any{}
		for _, f := range t.Fields {
			fields = append(fields, map[string]any{"key": f.Key, "label": f.Label, "secret": f.Secret, "required": f.Required})
		}
		out = append(out, map[string]any{"type": t.Type, "label": t.Label, "fields": fields})
	}
	writeJSON(w, http.StatusOK, out)
}

// present is the API shape of a notifier: secrets are masked.
func present(n store.Notifier) map[string]any {
	secret := notify.SecretKeys(n.Type)
	cfg := map[string]string{}
	for k, v := range n.Config {
		if secret[k] && v != "" {
			v = secretMask
		}
		cfg[k] = v
	}
	return map[string]any{"id": n.ID, "name": n.Name, "type": n.Type, "enabled": n.Enabled, "config": cfg}
}

func (s *Server) handleNotifierList(w http.ResponseWriter, r *http.Request) {
	list, err := s.d.Store.ListNotifiers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read the notifiers.")
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, n := range list {
		out = append(out, present(n))
	}
	writeJSON(w, http.StatusOK, out)
}

type notifierBody struct {
	Name    string            `json:"name"`
	Type    string            `json:"type"`
	Enabled bool              `json:"enabled"`
	Config  map[string]string `json:"config"`
}

// parseNotifier validates a request body. keep holds the stored config, so a
// masked secret is replaced by its stored value.
func (s *Server) parseNotifier(w http.ResponseWriter, r *http.Request, keep map[string]string) (store.Notifier, bool) {
	var b notifierBody
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return store.Notifier{}, false
	}
	b.Name = strings.TrimSpace(b.Name)
	if b.Name == "" {
		writeError(w, http.StatusBadRequest, "Enter a name for this notifier.")
		return store.Notifier{}, false
	}
	cfg := map[string]string{}
	for k, v := range b.Config {
		if v == secretMask && keep != nil {
			v = keep[k]
		}
		cfg[k] = strings.TrimSpace(v)
	}
	if _, err := notify.Build(b.Type, cfg, nil); err != nil {
		writeError(w, http.StatusBadRequest, capitalize(err.Error())+".")
		return store.Notifier{}, false
	}
	return store.Notifier{Name: b.Name, Type: b.Type, Config: cfg, Enabled: b.Enabled}, true
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (s *Server) handleNotifierCreate(w http.ResponseWriter, r *http.Request) {
	n, ok := s.parseNotifier(w, r, nil)
	if !ok {
		return
	}
	id, err := s.d.Store.AddNotifier(n)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not save the notifier.")
		return
	}
	n.ID = id
	writeJSON(w, http.StatusCreated, present(n))
}

func notifierID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

func (s *Server) handleNotifierUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := notifierID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "There is no notifier with that ID.")
		return
	}
	old, err := s.d.Store.GetNotifier(id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "There is no notifier with that ID.")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read the notifier.")
		return
	}
	n, ok := s.parseNotifier(w, r, old.Config)
	if !ok {
		return
	}
	n.ID = id
	if err := s.d.Store.UpdateNotifier(n); err != nil {
		writeError(w, http.StatusInternalServerError, "Could not save the notifier.")
		return
	}
	writeJSON(w, http.StatusOK, present(n))
}

func (s *Server) handleNotifierDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := notifierID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "There is no notifier with that ID.")
		return
	}
	if err := s.d.Store.DeleteNotifier(id); err != nil {
		writeError(w, http.StatusInternalServerError, "Could not delete the notifier.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleNotifierTest(w http.ResponseWriter, r *http.Request) {
	id, ok := notifierID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "There is no notifier with that ID.")
		return
	}
	n, err := s.d.Store.GetNotifier(id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "There is no notifier with that ID.")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read the notifier.")
		return
	}
	if err := notify.SendTest(r.Context(), s.d.Client, n); err != nil {
		writeError(w, http.StatusBadGateway, capitalize(err.Error())+".")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handlePushKey(w http.ResponseWriter, r *http.Request) {
	if s.d.Push == nil {
		writeError(w, http.StatusServiceUnavailable, "Push notifications are not set up on this server.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"publicKey": s.d.Push.Keys.Public})
}

func (s *Server) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Endpoint string            `json:"endpoint"`
		Keys     map[string]string `json:"keys"`
	}
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !strings.HasPrefix(b.Endpoint, "https://") || b.Keys["p256dh"] == "" || b.Keys["auth"] == "" {
		writeError(w, http.StatusBadRequest, "The subscription needs an https endpoint and both keys.")
		return
	}
	if err := s.d.Store.AddPushSub(store.PushSub{Endpoint: b.Endpoint, P256dh: b.Keys["p256dh"], Auth: b.Keys["auth"], UserID: userID(r)}); err != nil {
		writeError(w, http.StatusInternalServerError, "Could not save the subscription.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]bool{"ok": true})
}

func (s *Server) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Endpoint string `json:"endpoint"`
	}
	if err := readJSON(r, &b); err != nil || b.Endpoint == "" {
		writeError(w, http.StatusBadRequest, "The request needs an endpoint.")
		return
	}
	if err := s.d.Store.DeletePushSub(b.Endpoint); err != nil {
		writeError(w, http.StatusInternalServerError, "Could not remove the subscription.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) widgetToken() (string, error) {
	t, err := s.d.Store.GetSetting("widget_token")
	if err != nil || t != "" {
		return t, err
	}
	return s.rotateWidgetToken()
}

func (s *Server) rotateWidgetToken() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	t := base64.RawURLEncoding.EncodeToString(raw)
	return t, s.d.Store.SetSetting("widget_token", t)
}

func (s *Server) handleWidgetToken(w http.ResponseWriter, r *http.Request) {
	t, err := s.widgetToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read the widget token.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": t})
}

func (s *Server) handleWidgetRotate(w http.ResponseWriter, r *http.Request) {
	t, err := s.rotateWidgetToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not create a new widget token.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": t})
}

// handleWidget serves the numbers for a nextdash custom widget. It uses its
// own read-only token instead of a session.
func (s *Server) handleWidget(w http.ResponseWriter, r *http.Request) {
	given := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if given == "" {
		given = r.URL.Query().Get("token")
	}
	want, err := s.widgetToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read the widget token.")
		return
	}
	if given == "" || want == "" || subtle.ConstantTimeCompare([]byte(given), []byte(want)) != 1 {
		writeError(w, http.StatusUnauthorized, "The widget token is missing or wrong.")
		return
	}
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
	breaking := 0
	for _, i := range infos {
		if i.Breaking {
			breaking++
		}
	}
	last, _ := s.d.Store.GetSetting("last_check")
	writeJSON(w, http.StatusOK, map[string]any{"updates": len(avail), "breaking": breaking, "lastCheck": last, "url": s.d.BaseURL})
}

func (s *Server) handlePushTest(w http.ResponseWriter, r *http.Request) {
	if s.d.Push == nil {
		writeError(w, http.StatusServiceUnavailable, "Push notifications are not set up on this server.")
		return
	}
	subs, err := s.d.Store.ListPushSubs()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read the subscriptions.")
		return
	}
	payload, _ := json.Marshal(map[string]string{"title": "nextupdate test", "body": "Push notifications work on this device.", "url": s.d.BaseURL})
	sent, removed := 0, 0
	for _, sub := range subs {
		switch err := s.d.Push.Send(r.Context(), sub, payload); {
		case errors.Is(err, push.ErrGone):
			_ = s.d.Store.DeletePushSub(sub.Endpoint)
			removed++
		case err != nil:
			s.logf("api: test push: %v", err)
		default:
			sent++
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{"sent": sent, "removed": removed})
}
