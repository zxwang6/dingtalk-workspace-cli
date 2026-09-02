package helpers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	outputpkg "github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/output"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/testseam"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/pkg/edition"
)

type whiteboardTestCall struct {
	server string
	tool   string
	args   map[string]any
}

type whiteboardTestCaller struct {
	dry      bool
	format   string
	err      func(whiteboardTestCall, int) error
	response func(whiteboardTestCall, int) string
	calls    []whiteboardTestCall
}

func (c *whiteboardTestCaller) CallTool(_ context.Context, server, tool string, args map[string]any) (*edition.ToolResult, error) {
	call := whiteboardTestCall{server: server, tool: tool, args: args}
	c.calls = append(c.calls, call)
	if c.err != nil {
		if err := c.err(call, len(c.calls)-1); err != nil {
			return nil, err
		}
	}
	text := `{}`
	if c.response != nil {
		text = c.response(call, len(c.calls)-1)
	}
	return &edition.ToolResult{Content: []edition.ContentBlock{{Type: "text", Text: text}}}, nil
}

func (c *whiteboardTestCaller) Format() string { return c.format }
func (c *whiteboardTestCaller) DryRun() bool   { return c.dry }
func (*whiteboardTestCaller) Fields() string   { return "" }
func (*whiteboardTestCaller) JQ() string       { return "" }

func installWhiteboardTestCaller(t *testing.T, caller *whiteboardTestCaller) *bytes.Buffer {
	t.Helper()
	testseam.Protect(t, &deps)
	InitDeps(caller)
	output := &bytes.Buffer{}
	deps.Out.w = output
	deps.Out.errW = &bytes.Buffer{}
	return output
}

func TestWhiteboardQueryRoutesAndDecodesResultJSON(t *testing.T) {
	caller := &whiteboardTestCaller{
		format: "json",
		response: func(whiteboardTestCall, int) string {
			return `{"success":true,"resultJson":"{\"nodes\":[{\"type\":\"text\"}]}"}`
		},
	}
	output := installWhiteboardTestCaller(t, caller)
	cmd := newWhiteboardCommand()
	cmd.SetArgs([]string{"query", "--node", "doc-1", "--part-id", "part-1"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(caller.calls) != 1 || caller.calls[0].server != "whiteboard" || caller.calls[0].tool != whiteboardQueryTool {
		t.Fatalf("calls = %#v", caller.calls)
	}
	if caller.calls[0].args["nodeId"] != "doc-1" || caller.calls[0].args["partId"] != "part-1" {
		t.Fatalf("args = %#v", caller.calls[0].args)
	}
	var payload map[string]any
	if err := json.Unmarshal(output.Bytes(), &payload); err != nil {
		t.Fatalf("output = %q: %v", output.String(), err)
	}
	if _, ok := payload["resultJson"].(map[string]any); !ok {
		t.Fatalf("resultJson was not decoded: %#v", payload)
	}
}

func TestCrossPlatformCoverageWhiteboardStandaloneQueryPromotesResultJSONToSource(t *testing.T) {
	caller := &whiteboardTestCaller{
		format: "json",
		response: func(whiteboardTestCall, int) string {
			return `{"success":true,"nodeId":"wb-1","revision":2,"view":"all","resultJson":"{\"schemaVersion\":\"1.0\",\"catalogVersion\":\"dml-v1\",\"pages\":[{\"id\":\"page\",\"nodes\":[]}]}","resultSummary":{"pageCount":1,"nodeCount":0}}`
		},
	}
	output := installWhiteboardTestCaller(t, caller)
	cmd := newWhiteboardCommand()
	cmd.SetArgs([]string{"query", "--node", "wb-1", "--view", "all"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(caller.calls) != 1 || caller.calls[0].tool != standaloneWhiteboardQueryTool {
		t.Fatalf("calls = %#v", caller.calls)
	}
	var payload map[string]any
	if err := json.Unmarshal(output.Bytes(), &payload); err != nil {
		t.Fatalf("output = %q: %v", output.String(), err)
	}
	source, ok := payload["source"].(map[string]any)
	if !ok {
		t.Fatalf("source was not projected: %#v", payload)
	}
	resultJSON, ok := payload["resultJson"].(map[string]any)
	if !ok || !reflect.DeepEqual(resultJSON, source) {
		t.Fatalf("decoded resultJson = %#v, source = %#v", resultJSON, source)
	}
	pages, ok := source["pages"].([]any)
	if !ok || len(pages) != 1 || pages[0].(map[string]any)["id"] != "page" {
		t.Fatalf("source.pages = %#v", source["pages"])
	}
}

func TestWhiteboardUpdateValidatesSourceAndRequiresConfirmation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "whiteboard.json")
	if err := os.WriteFile(path, []byte(`{"overwrite":false,"source":{"schemaVersion":"1.0","catalogVersion":"dml-v1","nodes":[{"id":"n1","type":"text"}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	caller := &whiteboardTestCaller{format: "json"}
	installWhiteboardTestCaller(t, caller)

	cmd := newWhiteboardCommand()
	cmd.SetIn(strings.NewReader("no\n"))
	cmd.SetArgs([]string{"update", "--node", "doc-1", "--part-id", "part-1", "--source", path})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "用户取消了操作") {
		t.Fatalf("err = %v, want cancellation", err)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("remote call happened before confirmation: %#v", caller.calls)
	}

	cmd = newWhiteboardCommand()
	cmd.SetArgs([]string{"update", "--node", "doc-1", "--part-id", "part-1", "--source", path, "--yes"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(caller.calls) != 1 || caller.calls[0].tool != whiteboardUpdateTool {
		t.Fatalf("calls = %#v", caller.calls)
	}
	if caller.calls[0].args["mode"] != "append" || caller.calls[0].args["nodes"] != `[{"id":"n1","type":"text"}]` {
		t.Fatalf("args = %#v", caller.calls[0].args)
	}
}

func TestCrossPlatformCoverageWhiteboardQueryDeterministicallyRoutesBothKinds(t *testing.T) {
	caller := &whiteboardTestCaller{format: "json"}
	installWhiteboardTestCaller(t, caller)

	cmd := newWhiteboardCommand()
	cmd.SetArgs([]string{"query", "--node", "wb-1"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(caller.calls) != 1 || caller.calls[0].tool != standaloneWhiteboardQueryTool {
		t.Fatalf("standalone calls = %#v", caller.calls)
	}
	if got := caller.calls[0].args; got["nodeId"] != "wb-1" || got["view"] != "summary" {
		t.Fatalf("standalone args = %#v", got)
	}

	caller.calls = nil
	cmd = newWhiteboardCommand()
	cmd.SetArgs([]string{"query", "--node", "doc-1", "--part-id", "part-1"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(caller.calls) != 1 || caller.calls[0].tool != whiteboardQueryTool {
		t.Fatalf("embedded calls = %#v", caller.calls)
	}

	for _, args := range [][]string{
		{"query", "--node", "doc-1", "--part-id", ""},
		{"query", "--node", "doc-1", "--part-id", "   "},
		{"query", "--node", "doc-1", "--part-id", "part-1", "--view", "all"},
	} {
		caller.calls = nil
		cmd = newWhiteboardCommand()
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Fatalf("args %v unexpectedly succeeded", args)
		}
		if len(caller.calls) != 0 {
			t.Fatalf("args %v reached remote calls %#v", args, caller.calls)
		}
	}
}

func TestCrossPlatformCoverageWhiteboardStandaloneUpdateRoutesExactCASArgs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "whiteboard.json")
	if err := os.WriteFile(path, []byte(`{"overwrite":true,"source":{"schemaVersion":"1.0","catalogVersion":"dml-v1","nodes":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	caller := &whiteboardTestCaller{format: "json"}
	installWhiteboardTestCaller(t, caller)
	cmd := newWhiteboardCommand()
	cmd.SetArgs([]string{
		"update", "--node", "wb-1", "--source", path, "--page-id", "page-1",
		"--expected-revision", "12", "--request-id", "req-1", "--yes",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(caller.calls) != 1 || caller.calls[0].tool != standaloneWhiteboardUpdateTool {
		t.Fatalf("calls = %#v", caller.calls)
	}
	want := map[string]any{
		"nodeId": "wb-1", "mode": "overwrite", "nodes": "[]", "pageId": "page-1",
		"expectedRevision": 12, "requestId": "req-1",
	}
	if !reflect.DeepEqual(caller.calls[0].args, want) {
		t.Fatalf("args = %#v, want %#v", caller.calls[0].args, want)
	}

	for _, args := range [][]string{
		{"update", "--node", "wb-1", "--source", path, "--page-id", "page-1", "--request-id", "req-1", "--yes"},
		{"update", "--node", "wb-1", "--source", path, "--expected-revision", "12", "--request-id", "req-1", "--yes"},
		{"update", "--node", "doc-1", "--part-id", "part-1", "--source", path, "--expected-revision", "12", "--yes"},
	} {
		caller.calls = nil
		cmd = newWhiteboardCommand()
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Fatalf("args %v unexpectedly succeeded", args)
		}
		if len(caller.calls) != 0 {
			t.Fatalf("args %v reached remote calls %#v", args, caller.calls)
		}
	}
}

func TestCrossPlatformCoverageWhiteboardCreateWithContentValidatesAndRedactsDryRun(t *testing.T) {
	contentPath := filepath.Join(t.TempDir(), "checkpoint.txt")
	if err := os.WriteFile(contentPath, []byte("secret-checkpoint"), 0o600); err != nil {
		t.Fatal(err)
	}
	caller := &whiteboardTestCaller{
		format: "json",
		response: func(whiteboardTestCall, int) string {
			// Captured from the pre-release whiteboard gateway. The HSF result
			// declares richer verification fields, but the currently published
			// projection omits them and serializes revision as a string.
			return `{"docUrl":"https://pre-alidocs.dingtalk.com/i/nodes/wb-new","folderId":"folder-1","logId":"trace-1","mobileUrl":"https://pre-alidocs.dingtalk.com/i/nodes/wb-new","name":"Board.adraw","nodeId":"wb-new","revision":"0","success":true}`
		},
	}
	output := installWhiteboardTestCaller(t, caller)
	cmd := newWhiteboardCommand()
	ctx, _ := outputpkg.WithResultStore(context.Background())
	cmd.SetContext(ctx)
	cmd.SetOut(output)
	cmd.SetArgs([]string{"create-with-content", "--name", "Board", "--content", contentPath, "--request-id", "create-1"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	leaf, _, err := cmd.Find([]string{"create-with-content"})
	if err != nil {
		t.Fatal(err)
	}
	if _, emitted, err := outputpkg.EmitStoredResult(leaf); err != nil || !emitted {
		t.Fatalf("emit real result: emitted=%v err=%v", emitted, err)
	}
	if len(caller.calls) != 1 || caller.calls[0].tool != standaloneWhiteboardCreateTool || caller.calls[0].args["content"] != "secret-checkpoint" {
		t.Fatalf("calls = %#v", caller.calls)
	}
	var created map[string]any
	if err := json.Unmarshal(output.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	createdData, _ := created["data"].(map[string]any)
	if createdData["nodeId"] != "wb-new" || createdData["revision"] != float64(0) {
		t.Fatalf("pre-release create result was not normalized: %s", output.String())
	}

	caller = &whiteboardTestCaller{format: "json", dry: true}
	output = installWhiteboardTestCaller(t, caller)
	cmd = newWhiteboardCommand()
	ctx, _ = outputpkg.WithResultStore(context.Background())
	cmd.SetContext(ctx)
	cmd.SetOut(output)
	cmd.SetArgs([]string{"create-with-content", "--name", "Board", "--content", contentPath, "--request-id", "create-1"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	leaf, _, err = cmd.Find([]string{"create-with-content"})
	if err != nil {
		t.Fatal(err)
	}
	if _, emitted, err := outputpkg.EmitStoredResult(leaf); err != nil || !emitted {
		t.Fatalf("emit dry-run result: emitted=%v err=%v", emitted, err)
	}
	var preview map[string]any
	if err := json.Unmarshal(output.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	data, _ := preview["data"].(map[string]any)
	if len(caller.calls) != 0 || strings.Contains(output.String(), "secret-checkpoint") || data["contentBytes"] != float64(17) {
		t.Fatalf("dry-run calls=%#v output=%s", caller.calls, output.String())
	}
}

func TestCrossPlatformCoverageWhiteboardCreateReceiptRejectsExplicitContradictions(t *testing.T) {
	tests := []struct {
		name     string
		response map[string]any
	}{
		{name: "missing node", response: map[string]any{"success": true, "revision": "0"}},
		{name: "missing revision", response: map[string]any{"success": true, "nodeId": "wb"}},
		{name: "negative revision", response: map[string]any{"success": true, "nodeId": "wb", "revision": "-1"}},
		{name: "wrong content type", response: map[string]any{"success": true, "nodeId": "wb", "revision": "0", "contentType": "DOC"}},
		{name: "content not applied", response: map[string]any{"success": true, "nodeId": "wb", "revision": "0", "requestedContentApplied": false}},
		{name: "request mismatch", response: map[string]any{"success": true, "nodeId": "wb", "revision": "0", "requestMatched": false}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateStandaloneWhiteboardCreateResponse(test.response); err == nil || !strings.Contains(err.Error(), "成功回执字段不符合约定") {
				t.Fatalf("error = %v", err)
			}
		})
	}

	response := map[string]any{
		"success": true,
		"result": map[string]any{
			"nodeId": "wb", "revision": json.Number("7"), "contentType": "wbd",
			"requestedContentApplied": true, "requestMatched": true,
		},
	}
	if err := validateStandaloneWhiteboardCreateResponse(response); err != nil {
		t.Fatal(err)
	}
}

func TestDocWhiteboardInsertBuildsCardAndReturnsPersistedPartID(t *testing.T) {
	var blockID string
	caller := &whiteboardTestCaller{
		format: "json",
		response: func(call whiteboardTestCall, index int) string {
			if index == 0 {
				var node []any
				if err := json.Unmarshal([]byte(call.args["jsonml"].(string)), &node); err != nil {
					t.Fatalf("jsonml: %v", err)
				}
				attrs := node[1].(map[string]any)
				blockID = attrs["uuid"].(string)
				return `{}`
			}
			jsonml := fmt.Sprintf(`["card",{"uuid":%q,"cardType":"hetu","metadata":{"id":"part-real"}}]`, blockID)
			encoded, _ := json.Marshal(jsonml)
			return fmt.Sprintf(`{"blocks":[{"blockId":%q,"jsonml":%s}]}`, blockID, encoded)
		},
	}
	output := installWhiteboardTestCaller(t, caller)
	previousDelays := whiteboardRetryDelays
	whiteboardRetryDelays = nil
	t.Cleanup(func() { whiteboardRetryDelays = previousDelays })

	cmd := newDocWhiteboardCommand()
	cmd.SetArgs([]string{"insert", "--node", "doc-1", "--yes"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(caller.calls) != 2 || caller.calls[0].tool != "insert_document_block" || caller.calls[1].tool != "list_document_blocks" {
		t.Fatalf("calls = %#v", caller.calls)
	}
	if caller.calls[0].server != "doc" || caller.calls[1].server != "doc" {
		t.Fatalf("unexpected servers: %#v", caller.calls)
	}
	var payload map[string]any
	if err := json.Unmarshal(output.Bytes(), &payload); err != nil {
		t.Fatalf("output = %q: %v", output.String(), err)
	}
	result, _ := payload["result"].(map[string]any)
	if result["whiteboardId"] != "part-real" {
		t.Fatalf("output = %#v", payload)
	}
}

// whiteboardCardBlockID 从 insert_document_block 的请求里取出 CLI 生成的卡片块 UUID，
// 让回查桩可以用真实块 ID 组装响应。
func whiteboardCardBlockID(t *testing.T, call whiteboardTestCall) string {
	t.Helper()
	raw, _ := call.args["jsonml"].(string)
	var node []any
	if err := json.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatalf("jsonml: %v", err)
	}
	if len(node) < 2 {
		t.Fatalf("jsonml node missing attrs: %q", raw)
	}
	attrs, _ := node[1].(map[string]any)
	id, _ := attrs["uuid"].(string)
	if id == "" {
		t.Fatalf("jsonml node missing uuid: %q", raw)
	}
	return id
}

// stubWhiteboardRetries 把重试节奏换成可观测的桩，返回已休眠次数的读取器。
func stubWhiteboardRetries(t *testing.T, delays int) func() int {
	t.Helper()
	previousDelays := whiteboardRetryDelays
	previousSleep := whiteboardSleep
	stub := make([]time.Duration, delays)
	for i := range stub {
		stub[i] = time.Millisecond
	}
	slept := 0
	whiteboardRetryDelays = stub
	whiteboardSleep = func(time.Duration) { slept++ }
	t.Cleanup(func() {
		whiteboardRetryDelays = previousDelays
		whiteboardSleep = previousSleep
	})
	return func() int { return slept }
}

// 插入成功后的回查如果自身失败（鉴权 / MCP 错误 / 响应解析失败），不能退化成
// “暂未落库” 的 soft success，否则 Agent 会把硬失败误判成最终一致性，
// 继续带着空 partId 调用 whiteboard query/update。
func TestDocWhiteboardInsertFailsClosedWhenVerificationQueryFails(t *testing.T) {
	tests := []struct {
		name      string
		queryErr  error
		queryBody func(blockID string) string
	}{
		{name: "mcp call failed", queryErr: errors.New("unauthorized")},
		{
			name:      "response missing blocks field",
			queryBody: func(string) string { return `{"success":true}` },
		},
		{
			name:      "blocks field is not an array",
			queryBody: func(string) string { return `{"blocks":{}}` },
		},
		{
			name: "block jsonml unparsable",
			queryBody: func(blockID string) string {
				return fmt.Sprintf(`{"blocks":[{"blockId":%q,"jsonml":"{"}]}`, blockID)
			},
		},
		{
			name: "card node without attrs",
			queryBody: func(blockID string) string {
				encoded, _ := json.Marshal(`[]`)
				return fmt.Sprintf(`{"blocks":[{"blockId":%q,"jsonml":%s}]}`, blockID, encoded)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			blockID := ""
			caller := &whiteboardTestCaller{format: "json"}
			caller.response = func(call whiteboardTestCall, index int) string {
				if index == 0 {
					blockID = whiteboardCardBlockID(t, call)
					return `{}`
				}
				if test.queryBody == nil {
					return `{}`
				}
				return test.queryBody(blockID)
			}
			if test.queryErr != nil {
				caller.err = func(_ whiteboardTestCall, index int) error {
					if index == 0 {
						return nil
					}
					return test.queryErr
				}
			}
			installWhiteboardTestCaller(t, caller)
			slept := stubWhiteboardRetries(t, 2)

			cmd := newDocWhiteboardCommand()
			cmd.SetArgs([]string{"insert", "--node", "doc-1", "--yes"})
			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), "回查验证失败") {
				t.Fatalf("err = %v, want fail-closed verification error", err)
			}
			if !strings.Contains(err.Error(), blockID) {
				t.Fatalf("err = %v, want inserted blockId %s carried in the message", err, blockID)
			}
			if len(caller.calls) != 2 || slept() != 0 {
				t.Fatalf("calls = %d, slept = %d, want a single query and no retry on hard failure",
					len(caller.calls), slept())
			}
		})
	}
}

// 块暂不可见是真正的最终一致性：重试耗尽后仍按 soft success 返回 blockId，
// whiteboardId 为 null。
func TestDocWhiteboardInsertSoftSucceedsWhenBlockNotYetVisible(t *testing.T) {
	caller := &whiteboardTestCaller{
		format: "json",
		response: func(_ whiteboardTestCall, index int) string {
			if index == 0 {
				return `{}`
			}
			return `{"blocks":[]}`
		},
	}
	output := installWhiteboardTestCaller(t, caller)
	slept := stubWhiteboardRetries(t, 2)

	cmd := newDocWhiteboardCommand()
	cmd.SetArgs([]string{"insert", "--node", "doc-1", "--yes"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("block-not-visible must stay a soft success: %v", err)
	}
	// 1 次插入 + 3 次回查（attempt 0..2），其间休眠 2 次。
	if len(caller.calls) != 4 || slept() != 2 {
		t.Fatalf("calls = %d, slept = %d, want retries to be exhausted", len(caller.calls), slept())
	}
	var payload map[string]any
	if err := json.Unmarshal(output.Bytes(), &payload); err != nil {
		t.Fatalf("output = %q: %v", output.String(), err)
	}
	result, _ := payload["result"].(map[string]any)
	whiteboardID, present := result["whiteboardId"]
	if payload["success"] != true || !present || whiteboardID != nil {
		t.Fatalf("output = %#v, want soft success with an explicit null whiteboardId", payload)
	}
	if result["blockId"] == "" || result["blockId"] == nil {
		t.Fatalf("output = %#v, want blockId preserved on soft success", payload)
	}
}

// 同级插入与容器内插入共用 MCP 的 referenceBlockId：同时传两者过去会让 parent
// 静默覆盖 ref-block、而 --where 仍留在请求里污染容器插入语义。现在必须显式报错。
func TestDocWhiteboardInsertRejectsConflictingBlockAnchors(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{
			name: "ref-block with parent-block",
			args: []string{"insert", "--node", "doc-1", "--ref-block", "b1", "--parent-block", "p1", "--yes"},
		},
		{
			name: "where with parent-block",
			args: []string{"insert", "--node", "doc-1", "--parent-block", "p1", "--where", "before", "--yes"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			caller := &whiteboardTestCaller{format: "json"}
			installWhiteboardTestCaller(t, caller)

			cmd := newDocWhiteboardCommand()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs(test.args)
			err := cmd.Execute()
			if err == nil {
				t.Fatalf("args %v must be rejected as mutually exclusive", test.args)
			}
			if len(caller.calls) != 0 {
				t.Fatalf("args %v reached a remote call: %#v", test.args, caller.calls)
			}
		})
	}
}

func TestDocMediaUploadReturnsStableResourceContract(t *testing.T) {
	file := filepath.Join(t.TempDir(), "icon.svg")
	if err := os.WriteFile(file, []byte("<svg/>"), 0o600); err != nil {
		t.Fatal(err)
	}
	caller := &whiteboardTestCaller{
		format: "json",
		response: func(whiteboardTestCall, int) string {
			return `{"uploadUrl":"https://upload.example.test/token","resourceId":"res-1","resourceUrl":"https://resource.example.test/icon.svg"}`
		},
	}
	output := installWhiteboardTestCaller(t, caller)
	previousPut := httpPutFile
	httpPutFile = func(context.Context, string, map[string]string, string, int64) error { return nil }
	t.Cleanup(func() { httpPutFile = previousPut })

	cmd := newDocCommand()
	cmd.SetArgs([]string{"media", "upload", "--node", "doc-1", "--file", file, "--yes"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(caller.calls) != 1 || caller.calls[0].server != "doc" || caller.calls[0].tool != "get_doc_attachment_upload_info" {
		t.Fatalf("calls = %#v", caller.calls)
	}
	var payload map[string]any
	if err := json.Unmarshal(output.Bytes(), &payload); err != nil {
		t.Fatalf("output = %q: %v", output.String(), err)
	}
	if strings.Contains(output.String(), "upload.example.test") || payload["resourceId"] != "res-1" {
		t.Fatalf("output = %#v", payload)
	}
}

func TestDocMediaUploadRedactsTemporaryURLFromUploadError(t *testing.T) {
	file := filepath.Join(t.TempDir(), "icon.svg")
	if err := os.WriteFile(file, []byte("<svg/>"), 0o600); err != nil {
		t.Fatal(err)
	}
	uploadURL := "https://upload.example.test/secret-token"
	caller := &whiteboardTestCaller{
		format: "json",
		response: func(whiteboardTestCall, int) string {
			return fmt.Sprintf(`{"uploadUrl":%q,"resourceId":"res-1","resourceUrl":"https://resource.example.test/icon.svg"}`, uploadURL)
		},
	}
	installWhiteboardTestCaller(t, caller)
	previousPut := httpPutFile
	httpPutFile = func(context.Context, string, map[string]string, string, int64) error {
		return fmt.Errorf("PUT %s: connection reset", uploadURL)
	}
	t.Cleanup(func() { httpPutFile = previousPut })

	cmd := newDocCommand()
	cmd.SetArgs([]string{"media", "upload", "--node", "doc-1", "--file", file, "--yes"})
	err := cmd.Execute()
	if err == nil || strings.Contains(err.Error(), uploadURL) || !strings.Contains(err.Error(), "<redacted upload URL>") {
		t.Fatalf("err = %v, want redacted temporary upload URL", err)
	}
}
