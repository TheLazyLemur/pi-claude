// The same agent loop, off Claude Code and onto a local model.
//
// Nothing here knows it is talking to Ollama: only the backend passed to
// pi.Open changed. Needs `ollama serve` on this machine and the model in
// anthropic.Defaults() pulled.
package main

import (
	"context"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"strings"
	"time"

	pi "github.com/TheLazyLemur/pi-claude"
	"github.com/TheLazyLemur/pi-claude/external/anthropic"
)

func main() {
	token := fmt.Sprintf("%04d", rand.IntN(10000))

	getTime := pi.DefineTool("get_time", "Get the current time and the session's verification token.",
		func(_ context.Context, _ pi.NoParams) (pi.ToolResult, error) {
			return pi.Text("%s (verification token %s)", time.Now().Format(time.RFC1123), token), nil
		})

	sess, err := pi.Open(context.Background(), anthropic.New(), pi.Options{
		CustomTools: []pi.Tool{getTime},
		MaxTurns:    5,
		ApproveTool: func(_ context.Context, req pi.ToolRequest) pi.Decision {
			if !req.Mine {
				return pi.Deny("only the tools this program registered may run")
			}
			fmt.Printf("🔐 allow %s\n", req.Name)
			return pi.Allow()
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer sess.Close()

	sess.Subscribe(func(ev pi.Event) {
		switch e := ev.(type) {
		case pi.ReadyEvent:
			fmt.Printf("📋 %s, tools: %v\n", e.Model, e.Tools)
		case pi.ToolCallEvent:
			fmt.Printf("🔧 %s %v\n", e.Name, e.Input)
		case pi.ToolResultEvent:
			fmt.Printf("↩️  %s\n", e.Text)
		case pi.DeniedEvent:
			fmt.Printf("🚫 denied %s: %s\n", e.Name, e.Reason)
		case pi.TextEvent:
			fmt.Printf("🤖 %s\n", e.Text)
		}
	})

	turn, err := sess.Prompt(context.Background(),
		"Call get_time. Reply with the time and the verification token, exactly as the tool gave them.")
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("\nresult: %s (%d turns, %d tokens in, %d out)\n",
		turn.Subtype, turn.Turns, turn.Usage.InputTokens, turn.Usage.OutputTokens)

	if !strings.Contains(turn.Text, token) {
		fmt.Printf("FAIL  token %s did not come back in the answer\n", token)
		os.Exit(1)
	}
	fmt.Printf("ok    token %s made the round trip through the model\n", token)
}
