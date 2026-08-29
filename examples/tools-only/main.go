// Every built-in tool off, one custom tool on, handled end to end.
//
// The same demo hand-written against the raw protocol is 332 lines.
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
)

func main() {
	token := fmt.Sprintf("%04d", rand.IntN(10000))

	getTime := pi.DefineTool("get_time", "Get the current time and the session's verification token.",
		func(_ context.Context, _ pi.NoParams) (pi.ToolResult, error) {
			return pi.Text("%s (verification token %s)", time.Now().Format(time.RFC1123), token), nil
		})

	sess, err := pi.New(context.Background(), pi.Options{
		NoTools:     pi.NoToolsAll, // no built-ins, no machine-configured MCP servers
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

	var offered []string
	sess.Subscribe(func(ev pi.Event) {
		switch e := ev.(type) {
		case pi.ReadyEvent:
			offered = e.Tools
			fmt.Printf("📋 tools the model can see (%d): %v\n", len(e.Tools), e.Tools)
		case pi.ToolCallEvent:
			fmt.Printf("🔧 %s\n", e.Name)
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

	fmt.Printf("\nresult: %s (cost $%.4f, %d turns)\n", turn.Subtype, turn.CostUSD, turn.Turns)
	fmt.Println("\n--- verdict ---")

	checks := []struct {
		ok   bool
		text string
	}{
		{len(offered) == 1, fmt.Sprintf("tools offered: %d (want 1, ours)", len(offered))},
		{len(turn.Denials) == 0, fmt.Sprintf("tool calls denied: %d", len(turn.Denials))},
		{strings.Contains(turn.Text, token), fmt.Sprintf("token %s came back in the answer", token)},
	}

	code := 0
	for _, c := range checks {
		mark := "ok  "
		if !c.ok {
			mark, code = "FAIL", 1
		}
		fmt.Printf("  %s  %s\n", mark, c.text)
	}
	os.Exit(code)
}
