// Command check emits this application's offline go-tidb diagnostics as JSON.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	starterapp "github.com/mayahiro/go-tidb/examples/starter-app"
)

func main() {
	diagnostics := starterapp.CheckModels()
	diagnostics = append(diagnostics, starterapp.CheckRecentOrdersQuery(7, 1000)...)
	if err := json.NewEncoder(os.Stdout).Encode(diagnostics); err != nil {
		fmt.Fprintf(os.Stderr, "starter check: encode diagnostics: %v\n", err)
		os.Exit(1)
	}
}
