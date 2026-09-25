package engine

import (
	"context"

	"github.com/jordibrouwer/nextupdate/internal/docker"
	"github.com/jordibrouwer/nextupdate/internal/store"
	"github.com/jordibrouwer/nextupdate/internal/updater"
	"github.com/jordibrouwer/nextupdate/internal/verify"
)

// NewVerifier builds a verifier that reads the HTTP check and the verify
// window of each container from its settings, on top of the base check.
func NewVerifier(api docker.API, st *store.Store, base verify.Check) updater.Verifier {
	return func(ctx context.Context, name, id string) verify.Result {
		chk := base
		s, err := st.GetSettings(name)
		if err != nil {
			return verify.Result{Reason: "read settings: " + err.Error()}
		}
		if s.VerifyWindow > 0 {
			chk.Window = s.VerifyWindow
		}
		chk.HTTPURL = s.HTTPURL
		return verify.Verify(ctx, api, id, chk)
	}
}
