// Watching a session work: events as they happen, across several turns.
package main

import (
	"context"
	"fmt"
	"log"

	pi "github.com/TheLazyLemur/pi-claude"
)

func main() {
	ctx := context.Background()

	sess, err := pi.New(ctx, pi.Options{
		Tools:              []string{"Read", "Grep", "Glob"}, // read-only built-ins
		AppendSystemPrompt: "Be terse. Never guess at file contents; read them.",
	})
	if err != nil {
		log.Fatal(err)
	}
	defer sess.Close()

	stop := sess.Subscribe(func(ev pi.Event) {
		switch e := ev.(type) {
		case pi.ReadyEvent:
			fmt.Printf("[ready] %s, %d tools\n", e.Model, len(e.Tools))
		case pi.ToolCallEvent:
			fmt.Printf("[tool]  %s %v\n", e.Name, e.Input)
		case pi.TextEvent:
			fmt.Printf("[text]  %s\n", e.Text)
		case pi.TurnEvent:
			fmt.Printf("[turn]  $%.4f, %d turns\n", e.Turn.CostUSD, e.Turn.Turns)
		case pi.ErrorEvent:
			fmt.Printf("[error] %v\n", e.Err)
		}
	})
	defer stop()

	// The subprocess stays alive, so the second turn still has the first in context.
	if _, err := sess.Prompt(ctx, "What Go files are in this directory?"); err != nil {
		log.Fatal(err)
	}
	if _, err := sess.Prompt(ctx, "Which of those is the largest?"); err != nil {
		log.Fatal(err)
	}
}
