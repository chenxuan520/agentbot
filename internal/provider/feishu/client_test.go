package feishu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func TestBuildPostContentKeepsBlankTitleWhenNotProvided(t *testing.T) {
	t.Parallel()

	raw := buildPostContent("第一行\n第二行", " ", "")

	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("unmarshal content: %v", err)
	}
	zhCN, ok := payload["zh_cn"].(map[string]any)
	if !ok {
		t.Fatalf("missing zh_cn payload: %+v", payload)
	}
	title, _ := zhCN["title"].(string)
	if title != " " {
		t.Fatalf("title = %q, want single space", title)
	}
	content, ok := zhCN["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("content = %+v, want single markdown row", zhCN["content"])
	}
	row, ok := content[0].([]any)
	if !ok || len(row) != 1 {
		t.Fatalf("row = %+v", content[0])
	}
	block, ok := row[0].(map[string]any)
	if !ok {
		t.Fatalf("block = %+v", row[0])
	}
	text, _ := block["text"].(string)
	if text != "第一行\n第二行" {
		t.Fatalf("body text = %q, want %q", text, "第一行\n第二行")
	}
}

func TestRecallMessageCallsDeleteAPI(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"token-test"}`))
		case "/open-apis/im/v1/messages/om_test":
			if r.Method != http.MethodDelete {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			if got := r.Header.Get("Authorization"); got != "Bearer token-test" {
				t.Fatalf("authorization header = %q, want Bearer token-test", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := New("cli_test", "secret_test", "OnIt", "")
	client.apiClient = lark.NewClient("cli_test", "secret_test", lark.WithOpenBaseUrl(server.URL))
	client.httpClient = server.Client()

	if err := client.RecallMessage(context.Background(), "om_test"); err != nil {
		t.Fatalf("recall message: %v", err)
	}
}

func TestNewRemoteHandlingEmojiFallsBackToHandling(t *testing.T) {
	cases := []struct {
		name       string
		ack        string
		remote     string
		wantAck    string
		wantRemote string
	}{
		{name: "remote unset falls back to ack", ack: "OnIt", remote: "", wantAck: "OnIt", wantRemote: "OnIt"},
		{name: "remote set is independent", ack: "OnIt", remote: "Typing", wantAck: "OnIt", wantRemote: "Typing"},
		{name: "both unset default to OnIt", ack: "", remote: "", wantAck: "OnIt", wantRemote: "OnIt"},
		{name: "custom ack is reused for unset remote", ack: "SMILE", remote: "", wantAck: "SMILE", wantRemote: "SMILE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := New("cli", "secret", tc.ack, tc.remote)
			if c.handlingReactionEmoji != tc.wantAck {
				t.Fatalf("handlingReactionEmoji = %q, want %q", c.handlingReactionEmoji, tc.wantAck)
			}
			if c.remoteHandlingReactionEmoji != tc.wantRemote {
				t.Fatalf("remoteHandlingReactionEmoji = %q, want %q", c.remoteHandlingReactionEmoji, tc.wantRemote)
			}
		})
	}
}

func TestBuildPostContentMentionKeepsFirstLineInBody(t *testing.T) {
	t.Parallel()

	raw := buildPostContent("第一行\n```text\nprovider: feishu\n```", " ", "ou_xxx")

	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("unmarshal content: %v", err)
	}
	zhCN, ok := payload["zh_cn"].(map[string]any)
	if !ok {
		t.Fatalf("missing zh_cn payload: %+v", payload)
	}
	title, _ := zhCN["title"].(string)
	if title != " " {
		t.Fatalf("title = %q, want single space", title)
	}
	content, ok := zhCN["content"].([]any)
	if !ok || len(content) != 2 {
		t.Fatalf("content = %+v, want mention row + markdown row", zhCN["content"])
	}
	mentionRow, ok := content[0].([]any)
	if !ok || len(mentionRow) != 2 {
		t.Fatalf("mention row = %+v", content[0])
	}
	mentionText, ok := mentionRow[1].(map[string]any)
	if !ok {
		t.Fatalf("mention text block = %+v", mentionRow[1])
	}
	if text, _ := mentionText["text"].(string); text != " 第一行" {
		t.Fatalf("mention inline text = %q, want %q", text, " 第一行")
	}
	bodyRow, ok := content[1].([]any)
	if !ok || len(bodyRow) != 1 {
		t.Fatalf("body row = %+v", content[1])
	}
	bodyBlock, ok := bodyRow[0].(map[string]any)
	if !ok {
		t.Fatalf("body block = %+v", bodyRow[0])
	}
	if text, _ := bodyBlock["text"].(string); text != "```text\nprovider: feishu\n```" {
		t.Fatalf("body markdown = %q", text)
	}
}

func TestBuildPostContentSupportsMultipleMentions(t *testing.T) {
	t.Parallel()

	raw := buildPostContent("第一行\n第二行", " ", "ou_a", "ou_b")

	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("unmarshal content: %v", err)
	}
	zhCN, ok := payload["zh_cn"].(map[string]any)
	if !ok {
		t.Fatalf("missing zh_cn payload: %+v", payload)
	}
	content, ok := zhCN["content"].([]any)
	if !ok || len(content) != 2 {
		t.Fatalf("content = %+v, want mention row + markdown row", zhCN["content"])
	}
	mentionRow, ok := content[0].([]any)
	if !ok || len(mentionRow) != 4 {
		t.Fatalf("mention row = %+v", content[0])
	}
	mentionA, _ := mentionRow[0].(map[string]any)
	space, _ := mentionRow[1].(map[string]any)
	mentionB, _ := mentionRow[2].(map[string]any)
	inlineText, _ := mentionRow[3].(map[string]any)
	if mentionA["tag"] != "at" || mentionA["user_id"] != "ou_a" {
		t.Fatalf("first mention block = %+v", mentionA)
	}
	if space["tag"] != "text" || space["text"] != " " {
		t.Fatalf("space block = %+v", space)
	}
	if mentionB["tag"] != "at" || mentionB["user_id"] != "ou_b" {
		t.Fatalf("second mention block = %+v", mentionB)
	}
	if inlineText["tag"] != "text" || inlineText["text"] != " 第一行" {
		t.Fatalf("inline text block = %+v", inlineText)
	}
	bodyRow, ok := content[1].([]any)
	if !ok || len(bodyRow) != 1 {
		t.Fatalf("body row = %+v", content[1])
	}
	bodyBlock, _ := bodyRow[0].(map[string]any)
	if bodyBlock["text"] != "第二行" {
		t.Fatalf("body block = %+v", bodyBlock)
	}
}

func TestBuildPostContentReplacesMentionPlaceholdersInline(t *testing.T) {
	t.Parallel()

	raw := buildPostContent("__agent_bot_mention_0__ 当前指标已恢复，先关闭观察。", " ", "ou_a")

	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("unmarshal content: %v", err)
	}
	zhCN, ok := payload["zh_cn"].(map[string]any)
	if !ok {
		t.Fatalf("missing zh_cn payload: %+v", payload)
	}
	content, ok := zhCN["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("content = %+v, want single inline row", zhCN["content"])
	}
	row, ok := content[0].([]any)
	if !ok || len(row) != 2 {
		t.Fatalf("row = %+v", content[0])
	}
	mentionBlock, _ := row[0].(map[string]any)
	textBlock, _ := row[1].(map[string]any)
	if mentionBlock["tag"] != "at" || mentionBlock["user_id"] != "ou_a" {
		t.Fatalf("mention block = %+v", mentionBlock)
	}
	if textBlock["tag"] != "text" || textBlock["text"] != " 当前指标已恢复，先关闭观察。" {
		t.Fatalf("text block = %+v", textBlock)
	}
}

func TestBuildPostContentKeepsMarkdownListWhenInlineMentionExists(t *testing.T) {
	t.Parallel()

	raw := buildPostContent("- <at user_id=\"ou_a\">凌晨</at> 这一行应该同时是列表和真@", " ", "ou_sender")

	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("unmarshal content: %v", err)
	}
	zhCN, ok := payload["zh_cn"].(map[string]any)
	if !ok {
		t.Fatalf("missing zh_cn payload: %+v", payload)
	}
	content, ok := zhCN["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("content = %+v, want single markdown row", zhCN["content"])
	}
	row, ok := content[0].([]any)
	if !ok || len(row) != 1 {
		t.Fatalf("row = %+v", content[0])
	}
	block, ok := row[0].(map[string]any)
	if !ok {
		t.Fatalf("block = %+v", row[0])
	}
	if block["tag"] != "md" {
		t.Fatalf("tag = %+v, want md", block["tag"])
	}
	if block["text"] != "- <at user_id=\"ou_a\">凌晨</at> 这一行应该同时是列表和真@" {
		t.Fatalf("body text = %+v", block["text"])
	}
}

func TestBuildPostContentConvertsStandaloneMarkdownLinkToHyperlinkRow(t *testing.T) {
	t.Parallel()

	raw := buildPostContent("[**当前输出快照**](https://agent-bot.example.com/?token=abt_sess_demo&tab=transcript)\n```text\nsession_id: ses_demo\n```", " ", "ou_sender")

	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("unmarshal content: %v", err)
	}
	zhCN, ok := payload["zh_cn"].(map[string]any)
	if !ok {
		t.Fatalf("missing zh_cn payload: %+v", payload)
	}
	content, ok := zhCN["content"].([]any)
	if !ok || len(content) != 3 {
		t.Fatalf("content = %+v, want mention row + link row + markdown row", zhCN["content"])
	}
	linkRow, ok := content[1].([]any)
	if !ok || len(linkRow) != 1 {
		t.Fatalf("link row = %+v", content[1])
	}
	linkBlock, ok := linkRow[0].(map[string]any)
	if !ok {
		t.Fatalf("link block = %+v", linkRow[0])
	}
	if linkBlock["tag"] != "a" {
		t.Fatalf("tag = %+v, want a", linkBlock["tag"])
	}
	if linkBlock["text"] != "当前输出快照" {
		t.Fatalf("link text = %+v", linkBlock["text"])
	}
	if linkBlock["href"] != "https://agent-bot.example.com/?token=abt_sess_demo&tab=transcript" {
		t.Fatalf("link href = %+v", linkBlock["href"])
	}
	style, ok := linkBlock["style"].([]any)
	if !ok || len(style) != 1 || style[0] != "bold" {
		t.Fatalf("link style = %+v, want [bold]", linkBlock["style"])
	}
}
