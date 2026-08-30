// Command check emits this application's offline go-tidb diagnostics as JSON.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	starterapp "github.com/mayahiro/go-tidb/examples/starter-app"
	physicalschema "github.com/mayahiro/go-tidb/schema"
)

const querySchemaSnapshot = `CREATE TABLE clip_genres (
  clip_id BIGINT NOT NULL,
  genre_id BIGINT NOT NULL,
  PRIMARY KEY (clip_id, genre_id),
  KEY clip_genres_genre_clip_key (genre_id, clip_id)
);`

func main() {
	catalog, err := physicalschema.Parse(querySchemaSnapshot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "starter check: parse schema snapshot: %v\n", err)
		os.Exit(1)
	}
	diagnostics := starterapp.CheckModels()
	diagnostics = append(diagnostics, starterapp.CheckRecentOrdersQuery(7, 1000)...)
	diagnostics = append(diagnostics, starterapp.CheckRecentClipsInGenreQueryWithSchema(catalog, 7)...)
	if err := json.NewEncoder(os.Stdout).Encode(diagnostics); err != nil {
		fmt.Fprintf(os.Stderr, "starter check: encode diagnostics: %v\n", err)
		os.Exit(1)
	}
}
