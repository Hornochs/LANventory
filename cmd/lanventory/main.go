// Command lanventory serves an inventory system for LAN parties with DGUV V3
// inspection tracking.
package main

import (
	"os"

	"github.com/lan-dot-party/lanventory/cmd/lanventory/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
