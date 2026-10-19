package app

import (
	"net/http"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
)

// pluginWorkerMutationPath is the retired Host-executor seam of the legacy
// arbitrary plugin Worker mutation chain (#486). The route stays registered
// so a direct legacy caller receives an explicit, stable retirement
// rejection instead of a generic method-not-found: it never reads or decodes
// the body, never coordinates a business write and never executes plugin
// code. The generic plugin surface (/api/vibetable/v3/plugins/surface) and
// the plugin data seams are unaffected.
const pluginWorkerMutationPath = "/api/vibetable/v2/plugins/worker/mutation"

func registerPluginWorkerRetiredRoutes(r *router.Router[*core.RequestEvent]) {
	r.POST(pluginWorkerMutationPath, func(event *core.RequestEvent) error {
		if err := event.Request.Context().Err(); err != nil {
			return err
		}
		return event.JSON(http.StatusGone, map[string]any{
			"code":    pluginstore.WorkerRetiredReason,
			"message": "the arbitrary plugin Worker mutation chain retired with the Worker retirement; use the plugin surface for execution or pluginPreservation.snapshot for read-only preservation",
			"details": map[string]any{
				"preservationRpc": "pluginPreservation.snapshot",
			},
		})
	})
}
