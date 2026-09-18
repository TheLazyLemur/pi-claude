package scripted_test

import (
	"context"
	"strings"
	"testing"

	pi "github.com/TheLazyLemur/pi-claude"
	"github.com/TheLazyLemur/pi-claude/external/scripted"
)

type greetParams struct {
	Name string `json:"name" desc:"Who to greet"`
}

func TestPortAcceptsABackendThatIsNotACLI(t *testing.T) {
	// given
	// ... a host with one tool, opened on a backend that spawns nothing
	greet := pi.DefineTool("greet", "Greet someone",
		func(_ context.Context, p greetParams) (pi.ToolResult, error) {
			return pi.Text("hello %s", p.Name), nil
		})

	sess, err := pi.Open(context.Background(),
		scripted.New(scripted.Call("greet", greetParams{Name: "dan"})),
		pi.Options{CustomTools: []pi.Tool{greet}})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sess.Close()

	var saw []string
	sess.Subscribe(func(ev pi.Event) {
		switch e := ev.(type) {
		case pi.ReadyEvent:
			saw = append(saw, "ready:"+e.Model)
		case pi.ToolCallEvent:
			saw = append(saw, "tool:"+e.Name)
		}
	})

	// when
	// ... a prompt runs
	turn, err := sess.Prompt(context.Background(), "greet dan")

	// then
	// ... the host's tool ran and the turn came back through the same shapes
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if turn.Text != "hello dan" {
		t.Fatalf("text = %q", turn.Text)
	}
	if len(saw) != 2 || saw[0] != "ready:scripted" || saw[1] != "tool:greet" {
		t.Fatalf("events = %v", saw)
	}
}

func TestHostPolicyAppliesOnAnyBackend(t *testing.T) {
	// given
	// ... a host that refuses everything
	greet := pi.DefineTool("greet", "Greet someone",
		func(_ context.Context, p greetParams) (pi.ToolResult, error) {
			return pi.Text("hello %s", p.Name), nil
		})

	sess, err := pi.Open(context.Background(),
		scripted.New(scripted.Call("greet", greetParams{Name: "dan"})),
		pi.Options{
			CustomTools: []pi.Tool{greet},
			ApproveTool: func(context.Context, pi.ToolRequest) pi.Decision {
				return pi.Deny("not today")
			},
		})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sess.Close()

	// when
	// ... a prompt runs
	turn, err := sess.Prompt(context.Background(), "greet dan")

	// then
	// ... the refusal happened in the host, not in the backend
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if len(turn.Denials) != 1 || turn.Denials[0].ToolName != "greet" {
		t.Fatalf("denials = %+v", turn.Denials)
	}
}

func TestPromptRefusesImages(t *testing.T) {
	// given
	// ... a scripted session, whose Reply only ever sees the prompt text
	sess, err := pi.Open(context.Background(), scripted.New(scripted.Say("hi")), pi.Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sess.Close()

	// when
	// ... a prompt carries an image
	_, err = sess.Prompt(context.Background(), "what is this?", pi.Image{MediaType: "image/png", Data: []byte{1}})

	// then
	// ... it is refused rather than dropped on the way to the Reply
	if err == nil || !strings.Contains(err.Error(), "image") {
		t.Fatalf("err = %v, want an image refusal", err)
	}
}
