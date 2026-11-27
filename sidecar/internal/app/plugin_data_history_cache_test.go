package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/pluginmodel"
	"github.com/vibetable/vibetable/sidecar/internal/pluginstore"
	"github.com/vibetable/vibetable/sidecar/internal/queryschema"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

// dataReadFixture 在既有映射 fixture（existingModelMapping）之上准备两个文本字段与一行数据，
// 只授予显式 id 读权限并提交一次映射：mapped.FieldID 与 replacement.FieldID 只能经 completed
// 模型授权投影。#588 的缓存只服务 data.query / dataset.open，因此连续读取走真实的
// executePluginData query（投影 id + 具体字段）与 dataset.open，以“能否投影出该列的值”观察授权。
type dataReadFixture struct {
	pb          *pocketbase.PocketBase
	workspace   string
	input       pluginSurfaceAPIRequest
	domain      *schemaFieldChangeDomain
	mapped      v2.ApplyReceipt
	replacement v2.ApplyReceipt
	source      *queryschema.Source
}

const (
	dataReadSeedValue      = "甲"
	dataReadOtherSeedValue = "乙"
)

func newDataReadFixture(t *testing.T, seed bool) *dataReadFixture {
	t.Helper()
	pb, workspace, input, domain, _, mapped := existingModelMapping(t)
	replacement := createSchemaProductField(t, pb, mapped.TableID, v2.LogicalText, "替换字段", "history-cache-replace")
	input = changeDataGrants(t, pluginstore.New(pb, workspace), input, []pluginReadGrant{
		{Collection: "$active", Operations: []string{"read", "query"}, Fields: []string{"id"}},
		{Collection: mapped.TableID, Operations: []string{"read", "query"}, Fields: []string{"id"}},
	})
	input.ActiveCollection = mapped.TableID
	source, err := queryschema.New(pb.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &dataReadFixture{pb: pb, workspace: workspace, input: input, domain: domain, mapped: mapped, replacement: replacement, source: source}
	f.remap(t, mapped.FieldID)
	if seed {
		f.seed(t)
	}
	return f
}

func (f *dataReadFixture) seed(t *testing.T) {
	t.Helper()
	schema, err := schemaexecution.Describe(context.Background(), f.pb, f.mapped.TableID)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := f.pb.FindCollectionByNameOrId(schema.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	row := core.NewRecord(collection)
	row.Set(f.mapped.Definition.Identity.PhysicalName, dataReadSeedValue)
	row.Set(f.replacement.Definition.Identity.PhysicalName, dataReadOtherSeedValue)
	row.Set(f.mapped.Definition.Value.Presence.PhysicalName, true)
	row.Set(f.replacement.Definition.Value.Presence.PhysicalName, true)
	if err = f.pb.Save(row); err != nil {
		t.Fatal(err)
	}
}

// remap 走产品自己的 model.preview(map)+model.commit，追加 completed 进度记录。
func (f *dataReadFixture) remap(t *testing.T, fieldID string) {
	t.Helper()
	result, err := modelCall(t, f.pb, f.workspace, f.input, f.domain, "model.preview", modelPreviewRequest{Mode: "map", Mappings: map[string]pluginmodel.Binding{"items": {TableID: f.mapped.TableID, Fields: map[string]string{"name": fieldID}}}})
	if err != nil || len(result.(modelPreview).Diagnostics) != 0 {
		t.Fatalf("mapping preview: %+v %v", result, err)
	}
	if _, err = modelCall(t, f.pb, f.workspace, f.input, f.domain, "model.commit", map[string]any{"requestId": result.(modelPreview).RequestID}); err != nil {
		t.Fatal(err)
	}
}

func (f *dataReadFixture) call(t *testing.T, operation string, request any) (map[string]any, error) {
	t.Helper()
	raw, _ := json.Marshal(request)
	return executePluginData(context.Background(), f.pb, f.source, f.workspace, pluginDataRequest{InstallationID: f.input.InstallationID, PackageHash: f.input.PackageHash, InstallationRevision: f.input.InstallationRevision, ProjectKey: f.input.ProjectKey, PluginID: f.input.PluginID, ActiveCollection: f.input.ActiveCollection, Operation: operation, Request: raw})
}

// query 执行一次真实的 data.query：投影 id 与 fieldID，返回整页结果。
func (f *dataReadFixture) query(t *testing.T, fieldID string) (map[string]any, error) {
	t.Helper()
	return f.call(t, "query", pluginQueryRequest{Contract: "vibetable.plugin-query.v2", Collection: f.mapped.TableID, Fields: []string{"id", fieldID}})
}

// open 执行一次真实的 dataset.open：来源投影 id 与 fieldID 并按该字段分组。
func (f *dataReadFixture) open(t *testing.T, fieldID string) error {
	t.Helper()
	result, err := f.call(t, "dataset.open", pluginDatasetRequest{Contract: pluginDatasetContract, Sources: []pluginDatasetSource{{Query: pluginQueryRequest{Contract: "vibetable.plugin-query.v2", Collection: f.mapped.TableID, Fields: []string{"id", fieldID}}, GroupBy: []string{fieldID}}}})
	if err != nil {
		return err
	}
	if sources, _ := result["sources"].([]any); len(sources) != 1 {
		return fmt.Errorf("dataset.open returned %d sources, want 1", len(sources))
	}
	return nil
}

// warm 连续读取两次并确认第二次命中；返回此时的缓存统计。
func (f *dataReadFixture) warm(t *testing.T) pluginmodel.ProgressCacheStats {
	t.Helper()
	f.readField(t, f.mapped.FieldID)
	f.readField(t, f.mapped.FieldID)
	warm := pluginmodel.ProgressCacheStatsFor(f.pb)
	if warm.Hits == 0 {
		t.Fatalf("second read did not hit the cached history: %+v", warm)
	}
	return warm
}

// readField 经 query 投影 fieldID 并要求得到它的种子值。
func (f *dataReadFixture) readField(t *testing.T, fieldID string) {
	t.Helper()
	result, err := f.query(t, fieldID)
	requireProjected(t, result, err, fieldID, dataReadSeedValue)
}

func (f *dataReadFixture) historyCount(t *testing.T) int {
	t.Helper()
	history, err := pluginmodel.History(context.Background(), f.pb, f.input.ProjectKey, f.input.PluginID)
	if err != nil {
		t.Fatal(err)
	}
	return len(history)
}

func (f *dataReadFixture) latestCompleted(t *testing.T) pluginmodel.Progress {
	t.Helper()
	history, err := pluginmodel.History(context.Background(), f.pb, f.input.ProjectKey, f.input.PluginID)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].State.Status == "completed" {
			return history[i]
		}
	}
	t.Fatal("history has no completed progress")
	return pluginmodel.Progress{}
}

// appendProgress 在调用方事务内追加一条平台审计进度（与事务授权测试相同的写法）。
func (f *dataReadFixture) appendProgress(t *testing.T, tx core.App, installed pluginSurfaceInstallation, id string, detail any) {
	t.Helper()
	if _, err := pluginstore.New(tx, f.workspace).RecordAuditInTransaction(context.Background(), modelAudit(f.input, installed, id, "model.progress", detail)); err != nil {
		t.Fatal(err)
	}
}

func requireProjected(t *testing.T, result map[string]any, err error, fieldID, want string) {
	t.Helper()
	if err != nil {
		t.Fatalf("projecting %s was denied: %v", fieldID, err)
	}
	items, _ := result["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("query projected %d rows, want the single seeded row", len(items))
	}
	if value := items[0].(map[string]any)[fieldID]; fmt.Sprint(value) != want {
		t.Fatalf("projected %s = %v (%T), want %q", fieldID, value, value, want)
	}
}

// 旧历史首次读取逐条解码；连续的 query 与 dataset.open 命中同一份已验证历史且不再解码。
func TestPluginDataQueryHistoryCacheFirstReadThenHit(t *testing.T) {
	for _, method := range []string{"query", "dataset.open"} {
		t.Run(method, func(t *testing.T) {
			f := newDataReadFixture(t, true)
			total := f.historyCount(t)
			read := func() {
				t.Helper()
				if method == "query" {
					f.readField(t, f.mapped.FieldID)
					return
				}
				if err := f.open(t, f.mapped.FieldID); err != nil {
					t.Fatal(err)
				}
			}
			before := pluginmodel.ProgressCacheStatsFor(f.pb)
			read()
			cold := pluginmodel.ProgressCacheStatsFor(f.pb)
			if cold.Reads != before.Reads+1 || cold.Hits != before.Hits || cold.Decoded != before.Decoded+uint64(total) {
				t.Fatalf("first read must decode every record once: before=%+v cold=%+v history=%d", before, cold, total)
			}
			read()
			warm := pluginmodel.ProgressCacheStatsFor(f.pb)
			if warm.Reads != cold.Reads+1 || warm.Hits != cold.Hits+1 || warm.Decoded != cold.Decoded {
				t.Fatalf("second read must hit without decoding: cold=%+v warm=%+v", cold, warm)
			}
		})
	}
}

// 命中读取的 schema 修订来自当前表而不是缓存时的表。
func TestPluginDataQueryCacheHitReportsCurrentSchemaRevision(t *testing.T) {
	f := newDataReadFixture(t, true)
	f.warm(t)
	result, err := f.query(t, f.mapped.FieldID)
	requireProjected(t, result, err, f.mapped.FieldID, dataReadSeedValue)
	table, err := schemaexecution.Describe(context.Background(), f.pb, f.mapped.TableID)
	if err != nil {
		t.Fatal(err)
	}
	if result["schemaRevision"] != table.Snapshot.SchemaRevision {
		t.Fatalf("cached read reported schema revision %v, current table has %v", result["schemaRevision"], table.Snapshot.SchemaRevision)
	}
}

// 撤权后的首次请求：安装停用优先于任何缓存授权；重新启用（安装修订已变）后旧修订的
// completed 状态不再授予字段，缓存不能沿用停用前的授权。
func TestPluginDataQueryAfterRevocationChecksCurrentInstallation(t *testing.T) {
	f := newDataReadFixture(t, true)
	ctx := context.Background()
	warm := f.warm(t)
	store := pluginstore.New(f.pb, f.workspace)
	if _, err := store.SetEnabled(ctx, f.input.ProjectKey, f.input.PluginID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.query(t, f.mapped.FieldID); err == nil || !strings.Contains(err.Error(), "plugin_read_denied") {
		t.Fatalf("first read after revocation was authorized: %v", err)
	}
	if current := pluginmodel.ProgressCacheStatsFor(f.pb); current.Hits != warm.Hits || current.Decoded != warm.Decoded {
		t.Fatalf("revoked read consulted the cached history: warm=%+v current=%+v", warm, current)
	}
	if _, err := store.SetEnabled(ctx, f.input.ProjectKey, f.input.PluginID, true); err != nil {
		t.Fatal(err)
	}
	raw, err := store.GetInstallation(ctx, f.input.ProjectKey, f.input.PluginID)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Revision int64 `json:"revision"`
	}
	if err = json.Unmarshal(raw, &snapshot); err != nil || snapshot.Revision <= f.input.InstallationRevision {
		t.Fatalf("enable did not bump the installation revision: %+v %v", snapshot, err)
	}
	f.input.InstallationRevision = snapshot.Revision
	if result, err := f.query(t, "id"); err != nil || len(result["items"].([]any)) != 1 {
		t.Fatalf("explicit id grant must survive re-enabling: %v", err)
	}
	if _, err = f.query(t, f.mapped.FieldID); err == nil || !strings.Contains(err.Error(), "plugin_read_denied") {
		t.Fatalf("first read after re-enabling reused the completed state of an older installation revision: %v", err)
	}
}

// 重映射后的首次读取只解码新增记录并反映新绑定；物理字段漂移后即使命中历史也必须给出绑定诊断。
func TestPluginDataQueryAfterRemapAndSchemaDriftUsesCurrentAuthority(t *testing.T) {
	f := newDataReadFixture(t, false)
	ctx := context.Background()
	if _, err := f.query(t, f.mapped.FieldID); err != nil {
		t.Fatal(err)
	}
	warm := pluginmodel.ProgressCacheStatsFor(f.pb)
	count := f.historyCount(t)
	f.remap(t, f.replacement.FieldID)
	f.seed(t)
	result, err := f.query(t, f.replacement.FieldID)
	requireProjected(t, result, err, f.replacement.FieldID, dataReadOtherSeedValue)
	current := pluginmodel.ProgressCacheStatsFor(f.pb)
	appended := uint64(f.historyCount(t) - count)
	if appended == 0 {
		t.Fatal("remapping appended no progress")
	}
	if current.Hits != warm.Hits+1 || current.Decoded != warm.Decoded+appended {
		t.Fatalf("remapped read must decode only appended records: warm=%+v current=%+v appended=%d", warm, current, appended)
	}
	if _, err = f.query(t, f.mapped.FieldID); err == nil || !strings.Contains(err.Error(), "plugin_read_denied") {
		t.Fatalf("first read after remapping kept the old binding: %v", err)
	}

	// 物理字段漂移（与修复测试相同的触发方式）：缓存命中也不能跳过绑定诊断。
	table, err := schemaexecution.Describe(ctx, f.pb, f.mapped.TableID)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := f.pb.FindCollectionByNameOrId(table.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	collection.Fields.GetByName(f.replacement.Definition.Identity.PhysicalName).SetHidden(true)
	if err = f.pb.Save(collection); err != nil {
		t.Fatal(err)
	}
	beforeDrift := pluginmodel.ProgressCacheStatsFor(f.pb)
	if _, err = f.query(t, f.replacement.FieldID); err == nil || !strings.Contains(err.Error(), "plugin_model_binding_stale") {
		t.Fatalf("cached read skipped binding diagnostics after schema drift: %v", err)
	}
	if current = pluginmodel.ProgressCacheStatsFor(f.pb); current.Hits != beforeDrift.Hits+1 || current.Decoded != beforeDrift.Decoded {
		t.Fatalf("drift read should hit the unchanged history: before=%+v current=%+v", beforeDrift, current)
	}
}

// 回滚的事务不得把未提交进度留给下一次读取；同 id、同 seq、等长的内容替换不得被当作命中。
func TestPluginDataQueryIgnoresRolledBackAndEqualLengthReplacedHistory(t *testing.T) {
	f := newDataReadFixture(t, true)
	ctx := context.Background()
	warm := f.warm(t)
	total := uint64(f.historyCount(t))

	// 事务内追加一条会改变绑定的合法进度，并在事务内经缓存路径读取后回滚。
	uncommitted := f.latestCompleted(t).State
	uncommitted.RequestID = "rolled-back-synthetic-model"
	uncommitted.Bindings = map[string]pluginmodel.Binding{"items": {TableID: f.mapped.TableID, Fields: map[string]string{"name": f.replacement.FieldID}}}
	rollback := errors.New("rollback synthetic progress")
	err := f.pb.RunInTransaction(func(tx core.App) error {
		installed, err := surfaceInstallation(ctx, tx, f.workspace, f.input)
		if err != nil {
			return err
		}
		f.appendProgress(t, tx, installed, "rolled-back-synthetic-progress", uncommitted)
		inside, err := completedModelHistory(pluginmodel.WithProgressCache(ctx, tx), tx, f.input.ProjectKey, f.input.PluginID)
		if err != nil {
			return err
		}
		for _, state := range inside {
			if state.RequestID == uncommitted.RequestID {
				return rollback
			}
		}
		return errors.New("transaction read did not observe its own uncommitted progress")
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	// 事务内的缓存读取确实看到了未提交记录：前缀命中且只解码了它。
	inTransaction := pluginmodel.ProgressCacheStatsFor(f.pb)
	if inTransaction.Hits != warm.Hits+1 || inTransaction.Decoded != warm.Decoded+1 {
		t.Fatalf("in-transaction cached read must decode only its own record: warm=%+v in-transaction=%+v", warm, inTransaction)
	}
	f.readField(t, f.mapped.FieldID)
	afterRollback := pluginmodel.ProgressCacheStatsFor(f.pb)
	if afterRollback.Hits != inTransaction.Hits || afterRollback.Decoded != inTransaction.Decoded+total {
		t.Fatalf("history shorter than the cached one must be decoded again: in-transaction=%+v after=%+v total=%d", inTransaction, afterRollback, total)
	}
	if _, err = f.query(t, f.replacement.FieldID); err == nil || !strings.Contains(err.Error(), "plugin_read_denied") {
		t.Fatalf("rolled-back progress leaked into the next read: %v", err)
	}
	afterDenied := pluginmodel.ProgressCacheStatsFor(f.pb)

	// 等长替换：权威记录 id、seq 与字节长度都不变，只有绑定字段不同。
	latest := f.latestCompleted(t)
	record, err := f.pb.FindFirstRecordByFilter("vibetable_plugin_records", "kind='audit' && project_key={:project} && plugin_id={:plugin} && seq={:seq}", dbx.Params{"project": f.input.ProjectKey, "plugin": f.input.PluginID, "seq": latest.Seq})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(record.GetRaw("payload_json"))
	if err != nil {
		t.Fatal(err)
	}
	occurrences := bytes.Count(raw, []byte(f.mapped.FieldID))
	if len(f.mapped.FieldID) != len(f.replacement.FieldID) || occurrences != 1 {
		t.Fatalf("equal-length replacement precondition: %d vs %d bytes, occurrences=%d", len(f.mapped.FieldID), len(f.replacement.FieldID), occurrences)
	}
	replaced := bytes.Replace(raw, []byte(f.mapped.FieldID), []byte(f.replacement.FieldID), 1)
	if len(replaced) != len(raw) {
		t.Fatalf("replacement changed the record length: %d vs %d", len(replaced), len(raw))
	}
	if _, err = f.pb.DB().NewQuery("UPDATE vibetable_plugin_records SET payload_json={:payload} WHERE id={:id}").Bind(dbx.Params{"payload": string(replaced), "id": record.Id}).Execute(); err != nil {
		t.Fatal(err)
	}
	result, err := f.query(t, f.replacement.FieldID)
	requireProjected(t, result, err, f.replacement.FieldID, dataReadOtherSeedValue)
	final := pluginmodel.ProgressCacheStatsFor(f.pb)
	if final.Hits != afterDenied.Hits || final.Decoded != afterDenied.Decoded+total {
		t.Fatalf("replaced history must be decoded again: before=%+v final=%+v total=%d", afterDenied, final, total)
	}
	if _, err = f.query(t, f.mapped.FieldID); err == nil || !strings.Contains(err.Error(), "plugin_read_denied") {
		t.Fatalf("equal-length replacement was served as a cache hit: %v", err)
	}
}

// 追加损坏进度时 fail-closed：已缓存的历史不能掩盖新追加的损坏记录，且失败结果从不入缓存。
func TestPluginDataQueryFailsClosedOnAppendedCorruptProgress(t *testing.T) {
	f := newDataReadFixture(t, true)
	ctx := context.Background()
	warm := f.warm(t)
	err := f.pb.RunInTransaction(func(tx core.App) error {
		installed, err := surfaceInstallation(ctx, tx, f.workspace, f.input)
		if err != nil {
			return err
		}
		f.appendProgress(t, tx, installed, "corrupt-synthetic-progress", map[string]any{"contract": "invalid"})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err = f.query(t, f.mapped.FieldID); err == nil {
			t.Fatalf("query %d served authority from history containing corrupt progress", i)
		}
		if err = f.open(t, f.mapped.FieldID); err == nil {
			t.Fatalf("dataset.open %d served authority from history containing corrupt progress", i)
		}
		if current := pluginmodel.ProgressCacheStatsFor(f.pb); current.Hits != warm.Hits {
			t.Fatalf("read %d over a corrupt append was counted as a hit: warm=%+v current=%+v", i, warm, current)
		}
	}
	if states, err := pluginmodel.States(ctx, f.pb, f.input.ProjectKey, f.input.PluginID); err == nil || states != nil {
		t.Fatalf("uncached history disagrees with the cached reads: %d %v", len(states), err)
	}
}

// 安装升级（安装修订变化）后的首次读取不得沿用旧修订的 completed 授权；新修订下重新提交映射后
// 首次读取只解码新增记录。
func TestPluginDataQueryAfterInstallationUpgradeRebindsFromNewRevision(t *testing.T) {
	f := newDataReadFixture(t, true)
	warm := f.warm(t)
	count := f.historyCount(t)
	previous := f.input.InstallationRevision
	f.input = changeInstalledModel(t, f.pb, f.workspace, f.input, func(*pluginmodel.Declaration) {})
	if f.input.InstallationRevision <= previous {
		t.Fatal("upgrade did not bump the installation revision")
	}
	if result, err := f.query(t, "id"); err != nil || len(result["items"].([]any)) != 1 {
		t.Fatalf("explicit id grant must survive the upgrade: %v", err)
	}
	if current := pluginmodel.ProgressCacheStatsFor(f.pb); current.Hits != warm.Hits+1 || current.Decoded != warm.Decoded {
		t.Fatalf("unchanged history after upgrade must hit: warm=%+v current=%+v", warm, current)
	}
	if _, err := f.query(t, f.mapped.FieldID); err == nil || !strings.Contains(err.Error(), "plugin_read_denied") {
		t.Fatalf("first read after upgrade reused authority of installation revision %d: %v", previous, err)
	}
	f.remap(t, f.mapped.FieldID)
	appended := uint64(f.historyCount(t) - count)
	if appended == 0 {
		t.Fatal("remapping appended no progress")
	}
	f.readField(t, f.mapped.FieldID)
	// 被拒绝的那次读取同样命中未变化的历史；重新映射后的读取只解码追加记录。
	if final := pluginmodel.ProgressCacheStatsFor(f.pb); final.Hits != warm.Hits+3 || final.Decoded != warm.Decoded+appended {
		t.Fatalf("read after upgrade remapping must decode only appended records: warm=%+v final=%+v appended=%d", warm, final, appended)
	}
}
