package claude

import (
	"testing"

	"github.com/extractumio/todobem/internal/classify"
	"github.com/extractumio/todobem/internal/model"
)

// TestArtifactAndReportFindings: the Artifact tool (publish, read, list) is a code/artifact op —
// never a change of the sources — and ReportFindings is a review-stage op; an unmapped tool is
// still an honest unknown named by the tool.
func TestArtifactAndReportFindings(t *testing.T) {
	content := metaLine("mode", map[string]any{"mode": "normal"}) +
		promptLine(1_000, "u1", "Review the change and publish the report.", map[string]any{"promptSource": "typed", "origin": map[string]any{"kind": "human"}}) +
		assistantLine(2_000, "a1", "m1", "tool_use", toolUse("t1", "Edit", map[string]any{"file_path": fxCWD + "/pkg/x.go", "old_string": "a", "new_string": "b"}), usage{in: 10, cacheRead: 100, out: 10}) +
		resultLine(2_100, "r1", "t1", "The file has been updated.", false, nil) +
		assistantLine(3_000, "a2", "m2", "tool_use", toolUse("t2", "ReportFindings", map[string]any{"level": "medium", "findings": []any{map[string]any{"file": "pkg/x.go", "line": 3, "summary": "off by one"}}}), usage{in: 10, cacheRead: 100, out: 10}) +
		resultLine(3_100, "r2", "t2", "ok", false, nil) +
		assistantLine(4_000, "a3", "m3", "tool_use", toolUse("t3", "Artifact", map[string]any{"file_path": "/scratch/report.html", "icon": "chart"}), usage{in: 10, cacheRead: 100, out: 10}) +
		resultLine(9_000, "r3", "t3", "Published: https://claude.ai/artifact/x", false, nil) +
		assistantLine(9_500, "a4", "m4", "tool_use", toolUse("t4", "Artifact", map[string]any{"action": "read", "url": "https://claude.ai/artifact/x"}), usage{in: 10, cacheRead: 100, out: 10}) +
		resultLine(9_800, "r4", "t4", "<html>", false, nil) +
		assistantLine(10_000, "a5", "m5", "tool_use", toolUse("t5", "SomeNewTool", map[string]any{"x": 1}), usage{in: 10, cacheRead: 100, out: 10}) +
		resultLine(10_100, "r5", "t5", "ok", false, nil) +
		assistantLine(11_000, "a6", "m6", "end_turn", text("Done."), usage{in: 10, cacheRead: 100, out: 10})
	h, _ := home(t, content)
	_, s := open(t, h)
	m := s.Model
	checkPartition(t, m)
	ops := map[string]*model.Operation{}
	for _, o := range m.Lanes[0].Ops {
		ops[o.ID] = o
	}
	if o := ops["t2"]; o == nil || o.Phase != classify.Code || o.Kind != "review findings" || o.Lifecycle != classify.LcReview || o.Title != "report review findings" {
		t.Errorf("ReportFindings: %+v", o)
	}
	if o := ops["t3"]; o == nil || o.Phase != classify.Code || o.Kind != "artifact" || o.Title != "artifact publish report.html" || o.End != 9_000 || o.LifecycleRule != "phase code" {
		t.Errorf("Artifact publish: %+v", o)
	}
	if o := ops["t4"]; o == nil || o.Kind != "artifact" || o.Title != "artifact read x" {
		t.Errorf("Artifact read: %+v", o)
	}
	if classify.IsChangeOp(classify.Code, "artifact") {
		t.Error("an artifact publish is not a change of the sources")
	}
	if o := ops["t5"]; o == nil || o.Phase != classify.Unknown || o.Kind != "tool:SomeNewTool" {
		t.Errorf("an unmapped tool stays an honest unknown: %+v", o)
	}
}
