package app

import (
	"context"
	"reflect"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/sourceimport"
)

// The whole disclosure chain must survive together: the reviewed preview, the
// durable admission receipt and the history readback carry the acknowledged
// source notices verbatim, so a later history view shows exactly what the
// import window disclosed (including unselected tables).
func TestSourceImportNoticesSurvivePreviewAdmissionAndHistory(t *testing.T) {
	ctx := context.Background()
	fixture := newSourceImportAuthorityFixture(t)
	notices := []string{
		"文件包含 1 个仪表盘：仪表盘不会随导入迁移",
		"表「同名来源表」的字段「总额」是公式：计算值未随文件导出，迁移后该列将为空",
		"附件字段仅保留引用快照：附件字节不会下载或迁入",
	}
	source := sourceExecutionOrdinarySnapshot(2)
	source.Notices = notices
	plan := sourceExecutionPreview(t, source, nil)
	if !reflect.DeepEqual(plan.Notices, notices) {
		t.Fatalf("preview plan lost source notices: %#v", plan.Notices)
	}

	owner := newImportPlanOwner("workspace-one")
	preview, err := owner.mintSource(plan, 7, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(preview.Plan.Notices, notices) {
		t.Fatalf("preview reply lost source notices: %#v", preview.Plan.Notices)
	}

	journal := sourceimport.NewJournal(fixture.app)
	started, err := owner.startSource(ctx, sourceLifecycleRequest{
		Token: preview.Token, JobID: "notices-job", SessionEpoch: 7,
	}, 7, journal)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(started.Notices, notices) {
		t.Fatalf("durable admission receipt lost source notices: %#v", started.Notices)
	}

	listed, err := journal.List(ctx)
	if err != nil || len(listed) != 1 {
		t.Fatalf("history readback failed: %v, %d entries", err, len(listed))
	}
	if !reflect.DeepEqual(listed[0].Notices, notices) {
		t.Fatalf("history receipt lost source notices: %#v", listed[0].Notices)
	}
}
