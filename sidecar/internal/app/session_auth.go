package app

import (
	"net/http"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/hook"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/auth"
)

// bindVibetableSessionAuth installs the process-wide session admission hook.
// Every route on the sidecar router — including the internal import plan
// lifecycle ports — is closed unless the request carries the exact sidecar
// session secret. Extracted from app.go so tests exercise the production
// hook, not a copy.
func bindVibetableSessionAuth(
	router *router.Router[*core.RequestEvent],
	session auth.Secret,
) {
	router.Bind(&hook.Handler[*core.RequestEvent]{
		Id:       "vibetableSessionAuth",
		Priority: -10_000,
		Func: func(request *core.RequestEvent) error {
			if !session.Matches(request.Request.Header.Get(auth.HeaderName)) {
				return request.JSON(http.StatusUnauthorized, map[string]any{
					"code":    "session.unauthorized",
					"message": "valid sidecar session secret required",
				})
			}
			return request.Next()
		},
	})
}
