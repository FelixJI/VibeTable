package app

import (
	"encoding/json"
	"html/template"
	"net/http"
	"strings"

	"github.com/pocketbase/pocketbase/core"
)

const desktopSuperuserEmail = "desktop-admin@vibetable.local"

var adminBootstrapTemplate = template.Must(template.New("admin-bootstrap").Parse(
	`<!doctype html><meta charset="utf-8"><title>VibeTable Data Management</title>` +
		`<meta id="vibetable-admin-bootstrap" data-auth="{{.}}">` +
		`<script>const bootstrap=document.getElementById("vibetable-admin-bootstrap");` +
		`localStorage.setItem("__pb_superusers__/_",bootstrap.dataset.auth);` +
		`location.replace("/_/");</script>`,
))

func renderAdminBootstrap(authValue []byte) (string, error) {
	var page strings.Builder
	if err := adminBootstrapTemplate.Execute(&page, string(authValue)); err != nil {
		return "", err
	}
	return page.String(), nil
}

func registerAdminRoutes(event *core.ServeEvent) {
	event.Router.GET(
		"/api/vibetable/v1/admin/bootstrap",
		func(request *core.RequestEvent) error {
			superuser, err := request.App.FindAuthRecordByEmail(
				core.CollectionNameSuperusers,
				desktopSuperuserEmail,
			)
			if err != nil {
				collection, findErr := request.App.FindCollectionByNameOrId(
					core.CollectionNameSuperusers,
				)
				if findErr != nil {
					return request.InternalServerError(
						"Failed to load the local administrator collection.",
						findErr,
					)
				}

				superuser = core.NewRecord(collection)
				superuser.SetEmail(desktopSuperuserEmail)
				superuser.SetRandomPassword()
				superuser.Set("verified", true)
				if saveErr := request.App.Save(superuser); saveErr != nil {
					return request.InternalServerError(
						"Failed to create the local administrator.",
						saveErr,
					)
				}
			}

			token, err := superuser.NewAuthToken()
			if err != nil {
				return request.InternalServerError(
					"Failed to create the local administrator session.",
					err,
				)
			}

			authValue, err := json.Marshal(map[string]any{
				"token": token,
				"record": map[string]any{
					"id":             superuser.Id,
					"collectionId":   superuser.Collection().Id,
					"collectionName": superuser.Collection().Name,
					"email":          superuser.Email(),
					"verified":       superuser.Verified(),
					"created":        superuser.GetString("created"),
					"updated":        superuser.GetString("updated"),
				},
			})
			if err != nil {
				return request.InternalServerError(
					"Failed to serialize the local administrator session.",
					err,
				)
			}
			page, err := renderAdminBootstrap(authValue)
			if err != nil {
				return request.InternalServerError(
					"Failed to render the local administrator session.",
					err,
				)
			}

			request.Response.Header().Set("Cache-Control", "no-store")
			request.Response.Header().Set(
				"Content-Security-Policy",
				"default-src 'none'; script-src 'unsafe-inline'",
			)
			return request.HTML(http.StatusOK, page)
		},
	)
}
