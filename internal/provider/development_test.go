package provider //nolint:testpackage // Exercises private mailbox response validation and the subprocess test fixture.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func developmentTestRequest() Request {
	return Request{
		Model:           "test-model",
		Effort:          "low",
		ClientRequestID: "../../unsafe/id",
		Input:           []Item{Message("user", "hello")},
		MaxOutputTokens: 100,
		Tools:           []Tool{{Type: "function", Name: "lookup", Parameters: json.RawMessage(`{"type":"object"}`)}},
	}
}

func TestDevelopmentMockManualQA(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	client, err := NewMock(dir)
	if err != nil {
		t.Fatal(err)
	}
	in := developmentTestRequest()
	h := sha256.Sum256([]byte(in.ClientRequestID))
	base := filepath.Join(dir, hex.EncodeToString(h[:]))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		out, responseErr := client.Respond(ctx, in)
		if responseErr == nil && (len(out.Calls) != 1 || len(out.Output) != 1 || out.Usage.CachedInputTokens != 2) {
			responseErr = errors.New("QA response lost calls or usage")
		}
		result <- responseErr
	}()
	for {
		if _, checkErr := os.Stat(base + ".request.json"); checkErr == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		time.Sleep(time.Millisecond)
	}
	data, err := os.ReadFile(
		base + ".request.json",
	) // #nosec G304 -- Test-owned temporary directory plus SHA256 filename.
	if err != nil {
		t.Fatal(err)
	}
	var got Request
	if json.Unmarshal(data, &got) != nil || got.ClientRequestID != in.ClientRequestID {
		t.Fatal("invalid mailbox request")
	}
	out := Response{
		ID:    "qa-id",
		Calls: []Call{{ID: "call1", Name: "lookup", Arguments: json.RawMessage(`{"x":1}`)}},
		Usage: Usage{InputTokens: 10, CachedInputTokens: 2, OutputTokens: 3},
	}
	b, _ := json.Marshal(out)
	if fileErr := os.WriteFile(base+".response.tmp", b, 0600); fileErr != nil {
		t.Fatal(fileErr)
	}
	if fileErr := os.Rename(base+".response.tmp", base+".response.json"); fileErr != nil {
		t.Fatal(fileErr)
	}
	if responseErr := <-result; responseErr != nil {
		t.Fatal(responseErr)
	}
	if _, checkErr := client.Respond(ctx, in); checkErr != nil {
		t.Fatal("identical replay:", checkErr)
	}
	in.Input = []Item{Message("user", "different")}
	if _, checkErr := client.Respond(ctx, in); checkErr == nil {
		t.Fatal("changed request replay accepted")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("unexpected files: %v", entries)
	}
}

func TestDevelopmentMockCancellationAndValidation(t *testing.T) {
	t.Parallel()
	client, _ := NewMock(t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := client.Respond(ctx, developmentTestRequest())
	var failure *Failure
	if !errors.As(err, &failure) || !failure.UnknownUsage || failure.Code != "mock_cancelled" {
		t.Fatalf("cancellation: %v", err)
	}
	in := developmentTestRequest()
	for _, out := range []Response{
		{ID: "x", Usage: Usage{InputTokens: 2, CachedInputTokens: 2, CacheWriteTokens: 1}},
		{ID: "x", Text: "valid", Output: []Item{Message("assistant", "inconsistent")}},
		{ID: "x", Calls: []Call{{ID: "x", Name: "missing", Arguments: json.RawMessage(`{}`)}}},
	} {
		if _, checkErr := developmentResponse(in, out); checkErr == nil {
			t.Fatal("accepted invalid response")
		}
	}
	if _, checkErr := developmentResponse(
		in,
		Response{ID: "x", Usage: Usage{OutputTokens: 101}},
	); !errors.As(checkErr, &failure) || failure.Code != "output_limit" ||
		failure.UnknownUsage {
		t.Fatalf("output bound: %v", checkErr)
	}
}

// The test binary serves as a fake Codex executable. It never calls a model.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "exec" {
		runCodexFixture()
		return
	}

	os.Exit(m.Run())
}

func TestDevelopmentCodexFixture(t *testing.T) {
	executable, _ := os.Executable()
	home := t.TempDir()
	client, err := NewCodex(executable, home)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLX_PROVIDER_TEST_SECRET", "must-not-inherit")
	envelope := `{"text":"hello","calls":[{"id":"c1","name":"lookup","arguments":"{\"x\":1}"}]}`
	message, _ := json.Marshal(
		map[string]any{"type": "item.completed", "item": map[string]string{"type": "agent_message", "text": envelope}},
	)
	stream := "{\"type\":\"thread.started\",\"thread_id\":\"thread1\"}\n" + string(
		message,
	) + "\n{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":12,\"cached_input_tokens\":3,\"output_tokens\":7}}\n"
	if fileErr := os.WriteFile(filepath.Join(home, "fixture"), []byte(stream), 0600); fileErr != nil {
		t.Fatal(fileErr)
	}
	out, err := client.Respond(context.Background(), developmentTestRequest())
	if err != nil {
		t.Fatal(err)
	}
	if out.Text != "hello" || len(out.Calls) != 1 ||
		out.Usage != (Usage{InputTokens: 12, CachedInputTokens: 3, OutputTokens: 7}) {
		t.Fatalf("bad output: %#v", out)
	}
	for _, bad := range []string{"not JSON\n", "{\"type\":\"item.started\",\"item\":{\"type\":\"command_execution\"}}\n", string(message) + "\n{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":12}}\n", "sleep"} {
		if fileErr := os.WriteFile(filepath.Join(home, "fixture"), []byte(bad), 0600); fileErr != nil {
			t.Fatal(fileErr)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		_, responseErr := client.Respond(ctx, developmentTestRequest())
		cancel()
		var failure *Failure
		if !errors.As(responseErr, &failure) || !failure.UnknownUsage {
			t.Fatalf("failure should preserve reservation: %v", responseErr)
		}
	}
	brokenMessage, _ := json.Marshal(
		map[string]any{"type": "item.completed", "item": map[string]string{"type": "agent_message", "text": "{}"}},
	)
	brokenStream := string(
		brokenMessage,
	) + "\n{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":12,\"cached_input_tokens\":3,\"output_tokens\":7}}\n"
	if fileErr := os.WriteFile(filepath.Join(home, "fixture"), []byte(brokenStream), 0600); fileErr != nil {
		t.Fatal(fileErr)
	}
	rejected, err := client.Respond(context.Background(), developmentTestRequest())
	var known *Failure
	if !errors.As(err, &known) || known.UnknownUsage || rejected.ID == "" || rejected.Usage.InputTokens != 12 {
		t.Fatalf("known billed usage lost: %#v %v", rejected, err)
	}

	in := developmentTestRequest()
	in.Input = []Item{json.RawMessage(`{"type":"input_image","image_url":"https://example.com/image"}`)}
	if _, checkErr := client.Respond(context.Background(), in); checkErr == nil {
		t.Fatal("image input accepted")
	}
}

func runCodexFixture() {
	if !validCodexFixtureSchema(os.Args[1:]) {
		os.Exit(8)
	}
	args := strings.Join(os.Args[1:], "\n")
	for _, required := range []string{"--ephemeral", "--ignore-user-config", "--ignore-rules", "--skip-git-repo-check", "read-only", "--json", "--output-schema", "features.shell_tool=false", "features.apply_patch_freeform=false", `web_search="disabled"`, "mcp_servers={}", `model_reasoning_effort="low"`} {
		if !strings.Contains(args, required) {
			os.Exit(3)
		}
	}
	if os.Getenv("CLX_PROVIDER_TEST_SECRET") != "" || os.Getenv("HOME") != os.Getenv("CODEX_HOME") ||
		os.Getenv("USERPROFILE") != os.Getenv("CODEX_HOME") {
		os.Exit(4)
	}
	entries, _ := os.ReadDir(".")
	if len(entries) != 0 {
		os.Exit(5)
	}
	stdin, _ := io.ReadAll(os.Stdin)
	if !strings.Contains(string(stdin), "lookup") || !strings.Contains(string(stdin), "MaxOutputTokens") {
		os.Exit(6)
	}
	fixture, err := os.ReadFile(
		filepath.Join(os.Getenv("CODEX_HOME"), "fixture"),
	) // #nosec G703 -- Fake executable reads a fixture in the test-owned home.
	if err != nil {
		os.Exit(7)
	}
	if string(fixture) == "sleep" {
		time.Sleep(time.Hour)
		os.Exit(0)
	}
	_, _ = os.Stdout.Write(fixture)
	os.Exit(0)
}

// validCodexFixtureSchema checks the actual file supplied to the subprocess.
func validCodexFixtureSchema(args []string) bool {
	for index, arg := range args {
		if arg == "--output-schema" && index+1 < len(args) {
			raw, err := os.ReadFile(args[index+1])
			return err == nil && json.Valid(raw)
		}
	}
	return false
}
