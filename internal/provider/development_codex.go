package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type codex struct{ executable, home string }

// NewCodex requires a dedicated home with operator-provisioned authentication.
// It never copies credentials or inherits the server's credential environment.
func NewCodex(executable, home string) (Client, error) {
	if !filepath.IsAbs(executable) {
		return nil, errors.New("codex executable must be absolute")
	}
	if s, err := os.Stat(executable); err != nil || s.IsDir() {
		return nil, errors.New("codex executable must exist")
	}
	if err := developmentDirectory(home); err != nil {
		return nil, err
	}
	home = filepath.Clean(home)
	homeInfo, _ := os.Stat(home)
	for _, personal := range []string{os.Getenv("CODEX_HOME"), os.Getenv("HOME"), os.Getenv("USERPROFILE")} {
		if personal == "" {
			continue
		}
		for _, candidate := range []string{personal, filepath.Join(personal, ".codex")} {
			info, err := os.Stat(
				candidate,
			) // #nosec G703 -- Read-only comparison rejects ambient personal home aliases.
			if strings.EqualFold(home, filepath.Clean(candidate)) || (err == nil && os.SameFile(homeInfo, info)) {
				return nil, errors.New("codex home must be dedicated to this provider")
			}
		}
	}
	return &codex{executable, home}, nil
}

const codexOutputSchema = `{"type":"object","properties":{"text":{"type":"string"},"calls":{"type":"array","items":{"type":"object","properties":{"id":{"type":"string"},"name":{"type":"string"},"arguments":{"type":"string"}},"required":["id","name","arguments"],"additionalProperties":false}}},"required":["text","calls"],"additionalProperties":false}`

const codexHarnessInstructions = "Supply one model turn for Loom's development/test agent loop. The JSON request below contains Loom's Instructions, conversation Input and available harness Tools. Follow those instructions and continue that conversation. Do not use Codex's native tools. Instead, request actual Loom execution through the output calls array: each entry needs a unique nonempty id, a name from Tools and arguments encoded as a JSON object string. Loom executes these calls and supplies their real results in Input on the next turn. This is executable tool access, not a hypothetical plan; disabled Codex tools do not disable Loom tools. Read API help when needed, then request the service call to obtain current data. Help text is not a service result. Return final text with empty calls only when the task is complete or an evidenced blocker prevents progress. Never invent results or access errors. Keep output within MaxOutputTokens. Never claim token usage.\n"

func codexTextOnly(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		if typ, ok := x[fieldType].(string); ok &&
			(strings.Contains(typ, "image") || strings.Contains(typ, "audio") || strings.Contains(typ, "file")) {
			return false
		}
		for _, child := range x {
			if !codexTextOnly(child) {
				return false
			}
		}
	case []any:
		for _, child := range x {
			if !codexTextOnly(child) {
				return false
			}
		}
	}
	return true
}

type codexBound struct {
	n      atomic.Int64
	cancel context.CancelFunc
}

func (b *codexBound) add(n int) error {
	if b.n.Add(int64(n)) > developmentLimit {
		b.cancel()
		return errors.New("codex output too large")
	}
	return nil
}
func (b *codexBound) Write(p []byte) (int, error) {
	if err := b.add(len(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}

type codexReader struct {
	reader interface{ Read([]byte) (int, error) }
	bound  *codexBound
}

func (r codexReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if boundErr := r.bound.add(n); boundErr != nil {
		return n, boundErr
	}
	return n, err
}

func (p *codex) Respond(ctx context.Context, in Request) (Response, error) {
	b, err := developmentRequest(in)
	if err != nil {
		return Response{}, err
	}
	for _, item := range in.Input {
		var v any
		if json.Unmarshal(item, &v) != nil || !codexTextOnly(v) {
			return Response{}, &Failure{Code: "unsupported_input"}
		}
	}
	if ctx.Err() != nil {
		return Response{}, ctx.Err()
	}
	dir, err := os.MkdirTemp("", "clx-codex-*")
	if err != nil {
		return Response{}, &Failure{Code: codeCodexSetup}
	}
	defer func() { _ = os.RemoveAll(dir) }()
	schema := filepath.Join(dir, "output.schema.json")
	if os.WriteFile(schema, []byte(codexOutputSchema), 0600) != nil {
		return Response{}, &Failure{Code: codeCodexSetup}
	}
	work := filepath.Join(dir, "work")
	if os.Mkdir(work, 0700) != nil {
		return Response{}, &Failure{Code: codeCodexSetup}
	}
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := p.command(child, in, work, schema, b)

	bound := &codexBound{cancel: cancel}
	cmd.Stderr = bound
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Response{}, &Failure{Code: codeCodexSetup}
	}
	if cmd.Start() != nil {
		return Response{}, &Failure{Code: "codex_start"}
	}

	out, text, streamErr := readCodexStream(cmd, stdout, bound, cancel, in.ClientRequestID)
	if streamErr != nil {
		return out, streamErr
	}

	var envelope struct {
		Text  *string `json:"text"`
		Calls *[]struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"calls"`
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&envelope) != nil || !json.Valid([]byte(text)) || envelope.Text == nil || envelope.Calls == nil {
		out.ErrorCode = "codex_invalid_response"
		return developmentResponse(in, out)
	}
	out.Text = *envelope.Text
	for _, c := range *envelope.Calls {
		out.Calls = append(out.Calls, Call{ID: c.ID, Name: c.Name, Arguments: json.RawMessage(c.Arguments)})
	}
	return developmentResponse(in, out)
}

const codexScanBuffer = 4096

const codexItemCompleted = "item.completed"

// Completed error items are CLI notices, not tool execution or model output.
// Terminal success and actual usage remain mandatory in readCodexStream.
func allowedCodexItem(eventType, itemType string) bool {
	return itemType == "agent_message" || itemType == "reasoning" ||
		(eventType == codexItemCompleted && itemType == "error")
}

func (p *codex) command(child context.Context, in Request, work, schema string, b []byte) *exec.Cmd {
	args := []string{
		"exec",
		"--ephemeral",
		"--ignore-user-config",
		"--ignore-rules",
		"--skip-git-repo-check",
		"--sandbox",
		"read-only",
		"--json",
		"--output-schema",
		schema,
		"-m",
		in.Model,
		"-c",
		"features.shell_tool=false",
		"-c",
		"features.apply_patch_freeform=false",
		"-c",
		"web_search=\"disabled\"",
		"-c",
		"mcp_servers={}",
	}
	for _, feature := range []string{"unified_exec", "apps", "plugins", "hooks", "memories", "multi_agent", "multi_agent_v2", "browser_use", "browser_use_external", "computer_use", "in_app_browser", "image_generation", "view_image", "workspace_dependencies", "skill_search", "skill_mcp_dependency_install", "sleep_tool", "goals", "code_mode_host", "fast_mode"} {
		args = append(args, "-c", "features."+feature+"=false")
	}
	args = append(
		args,
		"-c",
		"project_doc_max_bytes=0",
		"-c",
		"memories.use_memories=false",
		"-c",
		"service_tier=\"default\"",
	)
	if in.Effort != "" {
		args = append(args, "-c", "model_reasoning_effort="+strconv.Quote(in.Effort))
	}
	args = append(args, "-")
	cmd := exec.CommandContext(
		child,
		p.executable,
		args...) // #nosec G204 -- Operator executable and separate arguments; no shell interpolation.
	cmd.WaitDelay = time.Second
	cmd.Dir = work
	for _, name := range []string{"PATH", "SystemRoot", "WINDIR", "TEMP", "TMP", "TMPDIR"} {
		if v := os.Getenv(name); v != "" {
			cmd.Env = append(cmd.Env, name+"="+v)
		}
	}
	cmd.Env = append(cmd.Env, "CODEX_HOME="+p.home, "HOME="+p.home, "USERPROFILE="+p.home)
	cmd.Stdin = bytes.NewReader(
		append(
			[]byte(codexHarnessInstructions),
			b...),
	)
	return cmd
}

func readCodexStream(
	cmd *exec.Cmd,
	stdout io.ReadCloser,
	bound *codexBound,
	cancel context.CancelFunc,
	requestID string,
) (Response, string, error) {
	fail := func(code string) (Response, string, error) {
		cancel()
		_ = stdout.Close()
		_ = cmd.Wait()
		return Response{}, "", &Failure{Code: code, UnknownUsage: true}
	}
	scanner := bufio.NewScanner(codexReader{stdout, bound})
	scanner.Buffer(make([]byte, codexScanBuffer), developmentLimit)
	var text, threadID string
	var usage *Usage
	completed := false
	for scanner.Scan() {
		var event struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
			Item     struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil || completed {
			return fail("codex_invalid_event")
		}
		switch event.Type {
		case "thread.started":
			threadID = event.ThreadID
		case "turn.started":
		case "item.started", "item.updated", codexItemCompleted:
			if !allowedCodexItem(event.Type, event.Item.Type) {
				return fail("codex_unexpected_tool")
			}
			if event.Type == codexItemCompleted && event.Item.Type == "agent_message" {
				text = event.Item.Text
			}
		case "turn.completed":
			var usageErr error
			usage, usageErr = codexUsage(scanner.Bytes())
			if usageErr != nil {
				return fail("codex_invalid_usage")
			}

			completed = true
		default:
			return fail("codex_invalid_event")
		}
	}
	scanErr := scanner.Err()
	if scanErr != nil {
		return fail("codex_output_unreadable")
	}
	if !validCodexEnd(cmd.Wait(), completed, usage) {
		return Response{}, "", &Failure{Code: "codex_incomplete", UnknownUsage: true}
	}
	out := Response{ID: threadID, Usage: *usage}
	if out.ID == "" {
		out.ID = requestID
	}
	return out, text, nil
}

func codexUsage(raw []byte) (*Usage, error) {
	var u struct {
		Usage *struct {
			Input   *int64 `json:"input_tokens"`
			Cached  *int64 `json:"cached_input_tokens"`
			Output  *int64 `json:"output_tokens"`
			Written int64  `json:"cache_write_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &u) != nil || u.Usage == nil || u.Usage.Input == nil ||
		u.Usage.Cached == nil ||
		u.Usage.Output == nil {
		return nil, errors.New("invalid codex usage")
	}
	usage := &Usage{
		InputTokens:       *u.Usage.Input,
		CachedInputTokens: *u.Usage.Cached,
		OutputTokens:      *u.Usage.Output,
		CacheWriteTokens:  u.Usage.Written,
	}
	return usage, nil
}

func validCodexEnd(err error, completed bool, usage *Usage) bool {
	return err == nil && completed && usage != nil
}
