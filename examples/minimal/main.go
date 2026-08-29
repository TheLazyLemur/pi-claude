// The smallest useful program: one prompt, one answer.
package main

import (
	"context"
	"fmt"
	"log"

	pi "github.com/TheLazyLemur/pi-claude"
)

func main() {
	turn, err := pi.Run(context.Background(), "In one sentence, what is a goroutine?", pi.Options{
		NoTools:  pi.NoToolsAll,
		MaxTurns: 1,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(turn.Text)
	fmt.Printf("\n(%d tokens in, %d out, $%.4f)\n",
		turn.Usage.InputTokens, turn.Usage.OutputTokens, turn.CostUSD)
}
