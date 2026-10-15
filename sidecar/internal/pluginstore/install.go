package pluginstore

import (
	"context"
	"encoding/json"
	"github.com/pocketbase/dbx"

	"github.com/pocketbase/pocketbase/core"
)

// CommitInstall is the fixed install command used after Host admission and
// local package retention. No HTTP compensation can delete a concurrent install.
func (service *Service) CommitInstall(ctx context.Context, planRaw, revisionRaw json.RawMessage) (json.RawMessage, error) {
	return service.commitPackage(ctx, planRaw, revisionRaw, "install")
}

func (service *Service) CommitUpgrade(ctx context.Context, planRaw, revisionRaw json.RawMessage) (json.RawMessage, error) {
	return service.commitPackage(ctx, planRaw, revisionRaw, "upgrade")
}

func (service *Service) CommitRollback(ctx context.Context, planRaw, revisionRaw json.RawMessage) (json.RawMessage, error) {
	return service.commitPackage(ctx, planRaw, revisionRaw, "rollback")
}

func (service *Service) commitPackage(ctx context.Context, planRaw, revisionRaw json.RawMessage, eventType string) (json.RawMessage, error) {
	upgrade := eventType != "install"
	plan, err := decodePayload(planRaw)
	if err != nil {
		return nil, err
	}
	if err := validateObject(plan, "planId projectKey projectRevision sourceType sourceLocation packageHash manifest schemas targetInstallationId targetRevision", "planId projectKey projectRevision sourceType sourceLocation packageHash"); err != nil {
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
		if !upgrade && (plan["targetInstallationId"] != nil || plan["targetRevision"] != nil) {
			return invalidPayload()
		}
		if !upgrade && current != nil {
			return requestError("plugin.already_installed", "plugin is already installed", nil)
		}
		if upgrade {
			expectedID := textValue(plan["targetInstallationId"])
			expectedRevision := jsonInt(planRaw, "targetRevision")
			if expectedID == "" || expectedRevision < 1 || current == nil || jsonText(current.Payload, "installationId") != expectedID || jsonInt(current.Payload, "revision") != expectedRevision {
				return revisionConflict(KindInstallation, expectedRevision, "installation changed")
			}
			snapshot, err = updateSnapshot(snapshot, map[string]any{
				"installationId": expectedID, "revision": expectedRevision + 1,
				"status":         jsonText(current.Payload, "status"),
				"disabledReason": nil, "blockingReasons": []string{},
			})
			if err != nil {
				return err
			}
			// Preserve enablement and blockers while switching the complete package.
			old, err := decodePayload(current.Payload)
			if err != nil {
				return err
			}
			snapshot, err = updateSnapshot(snapshot, map[string]any{"disabledReason": old["disabledReason"], "blockingReasons": old["blockingReasons"]})
			if err != nil {
				return err
			}
			revisions, err := tx.FindRecordsByFilter(recordsCollection,
				"kind={:kind} && project_key={:project} && plugin_id={:plugin}", "", 0, 0,
				dbx.Params{"kind": KindRevision, "project": identity.projectKey, "plugin": identity.pluginID})
			if err != nil {
				return storageError()
			}
			for _, stored := range revisions {
				prior, err := recordFromStored(stored)
				if err != nil {
					return err
				}
				if jsonText(prior.Payload, "state") == "current" {
					prior.Payload, err = updateSnapshot(prior.Payload, map[string]any{"state": "rollback"})
					if err != nil {
						return err
					}
					if err := saveRecord(tx, prior, true); err != nil {
						return err
					}
				} else if err := tx.Delete(stored); err != nil {
					return storageError()
				}
			}
		}
		if err := saveRecord(tx, record{Kind: KindInstallation, ProjectKey: identity.projectKey, PluginID: identity.pluginID, ItemKey: "current", Payload: snapshot}, upgrade); err != nil {
			return err
		}
		installed, err := findInstallation(tx, identity.projectKey, identity.pluginID)
		if err != nil {
			return err
		}
		if installed == nil {
			return storageError()
		}
		snapshot = installed.Payload
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
		audit, err := lifecycleAudit(service.now(), snapshot, eventType)
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
