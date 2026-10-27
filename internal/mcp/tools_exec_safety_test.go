package mcp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
)

func TestExecPodLogsMetadataWithoutCommandOrOutput(t *testing.T) {
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })
	input := execPodInput{Namespace: "shop", Name: "web-1", Command: []string{"psql", "postgres://user:argument-secret@db/app"}}
	run := func(context.Context, string, string, string, []string, io.Writer, io.Writer) error {
		return errors.New("transport-output-secret")
	}
	wrapped := logToolCall("exec_pod", func(ctx context.Context, req *mcp.CallToolRequest, input execPodInput) (*mcp.CallToolResult, any, error) {
		return execPod(ctx, fake.NewClientset(runningPod("web-1", true, "app")), nil, run, input)
	})
	res, _, err := wrapped(t.Context(), nil, input)
	if err != nil || res == nil || !res.IsError || !strings.Contains(decodeToolResult(t, res)["message"].(string), "transport-output-secret") {
		t.Fatalf("client lost its error: %v", err)
	}
	for _, secret := range []string{"argument-secret", "transport-output-secret", "postgres://", "argv="} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("logs contain %q: %s", secret, logs.String())
		}
	}
	for _, field := range []string{`executable="psql"`, `argumentCount=1`, `commandHash=`, `outcome="stream_error"`, `component=mcp`, `tool=exec_pod`} {
		if !strings.Contains(logs.String(), field) {
			t.Errorf("missing audit field %s: %s", field, logs.String())
		}
	}
}

func TestExecPodOmitsMistakenShellLinesFromAudit(t *testing.T) {
	for _, command := range []string{"curl -H 'Authorization: Bearer command-secret' http://service", "PGPASSWORD=command-secret psql", "sh\u00a0-c\u00a0command-secret", "https://user:command-secret@host"} {
		t.Run(command, func(t *testing.T) {
			var logs bytes.Buffer
			previous := log.Writer()
			log.SetOutput(&logs)
			t.Cleanup(func() { log.SetOutput(previous) })
			wrapped := logToolCall("exec_pod", func(ctx context.Context, _ *mcp.CallToolRequest, input execPodInput) (*mcp.CallToolResult, any, error) {
				return execPod(ctx, fake.NewClientset(runningPod("web-1", true, "app")), nil, func(context.Context, string, string, string, []string, io.Writer, io.Writer) error {
					return &execSetupError{errors.New("not started")}
				}, input)
			})
			wrapped(t.Context(), nil, execPodInput{Namespace: "shop", Name: "web-1", Command: []string{command}})
			if strings.Contains(logs.String(), "command-secret") || !strings.Contains(logs.String(), `executable="" argumentCount=0 commandHash=`) {
				t.Fatalf("unsafe audit: %s", logs.String())
			}
		})
	}
}

func TestExecPodInterruptedExecutionReportsUnknownOutcome(t *testing.T) {
	for _, mode := range []string{"canceled", "parent deadline", "stream error", "forbidden", "setup error"} {
		t.Run(mode, func(t *testing.T) {
			var logs bytes.Buffer
			previous := log.Writer()
			log.SetOutput(&logs)
			t.Cleanup(func() { log.SetOutput(previous) })
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "parent deadline" {
				var deadlineCancel context.CancelFunc
				ctx, deadlineCancel = context.WithTimeout(t.Context(), 50*time.Millisecond)
				defer deadlineCancel()
			}
			run := func(ctx context.Context, _, _, _ string, _ []string, stdout, _ io.Writer) error {
				io.WriteString(stdout, "partial output")
				switch mode {
				case "canceled":
					cancel()
					return ctx.Err()
				case "parent deadline":
					<-ctx.Done()
					return ctx.Err()
				case "forbidden":
					return apierrors.NewForbidden(schema.GroupResource{Resource: "pods/exec"}, "web-1", errors.New("denied"))
				case "setup error":
					return &execSetupError{errors.New("executor setup failed")}
				default:
					return io.ErrUnexpectedEOF
				}
			}
			res, _, err := execPod(ctx, fake.NewClientset(runningPod("web-1", true, "app")), nil, run, execPodInput{Namespace: "shop", Name: "web-1", Command: []string{"check"}})
			unknown := mode != "forbidden" && mode != "setup error"
			if unknown {
				if err != nil || res == nil || !res.IsError {
					t.Fatalf("lost error result: %v %v", res, err)
				}
				got := decodeToolResult(t, res)
				if got["stdout"] != "partial output" || got["pod"] != "web-1" || got["exitCode"] != nil || !strings.Contains(got["message"].(string), "may still be running") || !strings.Contains(got["message"].(string), "inspect its effects before retrying") {
					t.Fatalf("lost evidence: %+v", got)
				}
			} else if err == nil {
				t.Fatal("rejection must return an error")
			}
			outcome := "stream_error"
			switch mode {
			case "canceled", "parent deadline":
				outcome = "canceled"
			case "forbidden", "setup error":
				outcome = "rejected"
			}
			if !strings.Contains(logs.String(), `outcome="`+outcome+`"`) {
				t.Fatalf("outcome: %s", logs.String())
			}
		})
	}
}

func TestExecPodPermissionHintsDescribeTheFailedStage(t *testing.T) {
	denied := apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "replicasets"}, "", errors.New("denied"))
	resolution := execPodError(denied, false).Error()
	if strings.Contains(resolution, "exec requires create") || !strings.Contains(resolution, "revision evidence") || !strings.Contains(resolution, "replicasets") {
		t.Fatal(resolution)
	}
	stream := execPodError(denied, true).Error()
	if !strings.Contains(stream, "exec needs pods/exec permission") {
		t.Fatal(stream)
	}
}

func TestExecAuditAttemptPrecedesRunAndCorrelatesCompletion(t *testing.T) {
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })
	var attempt string
	run := func(context.Context, string, string, string, []string, io.Writer, io.Writer) error {
		attempt = logs.String()
		if !strings.Contains(attempt, `outcome="attempt"`) || strings.Contains(attempt, `outcome="exit_0"`) {
			t.Errorf("missing pre-execution record: %s", attempt)
		}
		return nil
	}
	_, _, err := execPod(t.Context(), fake.NewClientset(runningPod("web-1", true, "app")), nil, run, execPodInput{Namespace: "shop", Name: "web-1", Command: []string{"true"}})
	if err != nil {
		t.Fatal(err)
	}
	index := strings.Index(attempt, ` invocationID="`)
	if index < 0 {
		t.Fatalf("missing invocation identity: %s", attempt)
	}
	id := strings.Fields(attempt[index:])[0]
	if strings.Count(logs.String(), id) != 2 || !strings.Contains(logs.String(), `outcome="exit_0"`) {
		t.Fatalf("uncorrelated completion: %s", logs.String())
	}
}
