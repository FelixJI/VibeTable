package pluginstore

import (
	"context"
	"encoding/json"

	"github.com/pocketbase/pocketbase/core"
)

// CommitInstall is the fixed install command used after Host admission and
// local package retention. No HTTP compensation can delete a concurrent install.
func (service *Service) CommitInstall(ctx context.Context, planRaw, revisionRaw json.RawMessage) (json.RawMessage, error) {
	plan, err := decodePayload(planRaw)
	if err != nil {
		return nil, err
	}
	if err := validateObject(plan, "planId projectKey projectRevision sourceType sourceLocation packageHash manifest schemas", "planId projectKey projectRevision sourceType sourceLocation packageHash"); err != nil {
		return nil, err
	}
	manifest, ok := plan["manifest"].(map[string]any)
	if !ok {
		return nil, invalidPayload()
	}
	pluginID, version := textValue(manifest["pluginId"]), textValue(manifest["version"])
	developmentSource := any(nil)
	if plan["sourceType"] == "local-folder" {
		developmentSource = plan["sourceLocation"]
	}
	snapshot, err := json.Marshal(map[string]any{
		"projectKey": plan["projectKey"], "pluginId": pluginID, "version": version,
		"packageHash": plan["packageHash"], "sourceType": plan["sourceType"],
		"sourceLocation": plan["sourceLocation"], "developmentSourceLocation": developmentSource,
		"sourceChanged": false, "manifest": manifest, "schemas": plan["schemas"],
		"status": "disabled", "disabledReason": "disabled_by_user", "blockingReasons": []string{}, "revision": 1,
	})
	if err != nil {
		return nil, invalidPayload()
	}
	identity, err := validateSnapshot(snapshot)
	if err != nil {
		return nil, err
	}
	if err := service.ValidateProject(identity.projectKey); err != nil {
		return nil, err
	}
	revisionIdentity, hash, err := validateRevisionPayload(revisionRaw)
	if err != nil {
		return nil, err
	}
	if revisionIdentity != identity || hash != plan["packageHash"] || jsonText(revisionRaw, "version") != version || jsonText(revisionRaw, "state") != "current" {
		return nil, invalidPayload()
	}
	var revision map[string]any
	if json.Unmarshal(revisionRaw, &revision) != nil {
		return nil, invalidPayload()
	}
	planManifest, _ := json.Marshal(manifest)
	revisionManifest, _ := json.Marshal(revision["manifest"])
	if string(planManifest) != string(revisionManifest) {
		return nil, invalidPayload()
	}
	err = service.transact(ctx, func(tx core.App) error {
		current, err := findInstallation(tx, identity.projectKey, identity.pluginID)
		if err != nil {
			return err
		}
		if current != nil {
			return requestError("plugin.already_installed", "plugin is already installed", nil)
		}
		if err := saveRecord(tx, record{Kind: KindInstallation, ProjectKey: identity.projectKey, PluginID: identity.pluginID, ItemKey: "current", Payload: snapshot}, false); err != nil {
			return err
		}
		stored, err := findRecord(tx, KindRevision, identity.projectKey, identity.pluginID, hash)
		if err != nil {
			return err
		}
		seq := int64(0)
		if stored != nil {
			seq = int64(stored.GetInt("seq"))
		} else {
			seq, err = nextProjectSeq(tx, KindRevision, identity.projectKey)
			if err != nil {
				return err
			}
		}
		if err := saveRecord(tx, record{Kind: KindRevision, ProjectKey: identity.projectKey, PluginID: identity.pluginID, ItemKey: hash, Payload: revisionRaw, Seq: seq}, stored != nil); err != nil {
			return err
		}
		audit, err := lifecycleAudit(service.now(), snapshot, "install")
		if err != nil {
			return err
		}
		if _, err = service.recordAuditTx(tx, audit); err != nil {
			return err
		}
		event, err := catalogChangedEvent(service.newID(), service.now(), snapshot)
		if err != nil {
			return err
		}
		return saveOutboxEvent(tx, event)
	})
	return snapshot, err
}
