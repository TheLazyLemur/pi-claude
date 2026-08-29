// Exercises the features that cannot be proven with a fake transport:
// hooks, structured output, streaming deltas and subagents.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	pi "github.com/TheLazyLemur/pi-claude"
)

type verdict struct {
	Answer     string `json:"answer" desc:"The capital city"`
	Confidence int    `json:"confidence" desc:"0 to 100"`
}

func main() {
	ctx := context.Background()
	results := map[string]bool{}

	// --- hooks + streaming -------------------------------------------------
	var mu sync.Mutex
	hookFired := false
	var hookSaw string

	sess, err := pi.New(ctx, pi.Options{
		Tools:                  []string{"Read"},
		PermissionMode:         pi.PermissionModeDefault,
		IncludePartialMessages: true,
		Hooks: map[pi.HookEvent][]pi.HookMatcher{
			pi.HookPreToolUse: {{
				Hooks: []pi.HookFunc{func(_ context.Context, in pi.HookInput) (pi.HookOutput, error) {
					mu.Lock()
					hookFired, hookSaw = true, in.ToolName
					mu.Unlock()
					return pi.HookDeny("go.mod is off limits; the module is github.com/TheLazyLemur/pi-claude"), nil
				}},
			}},
		},
	})
	if err != nil {
		fmt.Println("spawn:", err)
		os.Exit(1)
	}

	deltas := 0
	sess.Subscribe(func(ev pi.Event) {
		switch e := ev.(type) {
		case pi.DeltaEvent:
			mu.Lock()
			deltas++
			mu.Unlock()
		case pi.ToolCallEvent:
			fmt.Printf("  tool: %s\n", e.Name)
		}
	})

	turn, err := sess.Prompt(ctx, "Read the file ./go.mod and tell me the module name.")
	if err != nil {
		fmt.Println("prompt:", err)
	}
	sess.Close()

	mu.Lock()
	results["hook fired"] = hookFired
	results["hook saw the tool name"] = hookSaw != ""
	results["deny reason reached the model"] = strings.Contains(turn.Text, "off limits") ||
		strings.Contains(turn.Text, "pi-claude")
	results["streaming deltas arrived"] = deltas > 0
	mu.Unlock()
	fmt.Printf("  hook fired=%v saw=%q deltas=%d\n", hookFired, hookSaw, deltas)
	fmt.Printf("  answer: %s\n", strings.TrimSpace(firstLine(turn.Text)))

	// --- structured output -------------------------------------------------
	structured, err := pi.Run(ctx, "What is the capital of France?", pi.Options{
		NoTools:      pi.NoToolsAll,
		MaxTurns:     1,
		OutputSchema: pi.SchemaFor[verdict](),
	})
	if err != nil {
		fmt.Println("structured:", err)
	}
	var got verdict
	decoded := len(structured.StructuredOutput) > 0 &&
		json.Unmarshal(structured.StructuredOutput, &got) == nil
	results["structured output returned"] = decoded && got.Answer != ""
	fmt.Printf("  structured: %s (confidence %d)\n", got.Answer, got.Confidence)

	// --- subagents ---------------------------------------------------------
	agentTurn, err := pi.Run(ctx, "Say the word 'ready' and nothing else.", pi.Options{
		NoTools:  pi.NoToolsAll,
		MaxTurns: 1,
		Agents: map[string]pi.Agent{
			"reviewer": {Description: "Reviews code for bugs", Prompt: "You are a code reviewer."},
		},
	})
	results["session with custom agents starts"] = err == nil && !agentTurn.IsError
	if err != nil {
		fmt.Println("  agents:", err)
	}

	fmt.Println("\n--- verdict ---")
	code := 0
	for _, name := range []string{
		"hook fired", "hook saw the tool name", "deny reason reached the model",
		"streaming deltas arrived",
		"structured output returned", "session with custom agents starts",
	} {
		mark := "ok  "
		if !results[name] {
			mark, code = "FAIL", 1
		}
		fmt.Printf("  %s  %s\n", mark, name)
	}
	os.Exit(code)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
