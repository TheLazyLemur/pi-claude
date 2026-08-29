// Gating tools: the model may read, but never outside the working directory.
package main

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"

	pi "github.com/TheLazyLemur/pi-claude"
)

func main() {
	ctx := context.Background()
	root, _ := filepath.Abs(".")

	sess, err := pi.New(ctx, pi.Options{
		Tools:          []string{"Read"},
		PermissionMode: pi.PermissionModeDefault, // ask us, even if this machine is set to auto-approve
		ApproveTool: func(_ context.Context, req pi.ToolRequest) pi.Decision {
			path, _ := req.Input["file_path"].(string)
			abs, _ := filepath.Abs(path)
			if path != "" && !strings.HasPrefix(abs, root) {
				return pi.Deny("reads are confined to " + root)
			}
			return pi.Allow()
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer sess.Close()

	sess.Subscribe(func(ev pi.Event) {
		switch e := ev.(type) {
		case pi.ToolCallEvent:
			fmt.Printf("🔧 %s %v\n", e.Name, e.Input)
		case pi.DeniedEvent:
			fmt.Printf("🚫 denied %s: %s\n", e.Name, e.Reason)
		}
	})

	turn, err := sess.Prompt(ctx, "Read /etc/hosts and tell me the first line.")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("\n%s\n\ndenials: %d\n", turn.Text, len(turn.Denials))
}
