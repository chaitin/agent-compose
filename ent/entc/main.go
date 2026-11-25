// Command entc generates the ent DAO from ent/schema.
//
// Run it through `task generate:ent` (or `go generate ./ent/...`) after changing
// a schema. Never edit ent/dao by hand: the next generation overwrites it.
package main

import (
	"log"

	"entgo.io/ent/entc"
	"entgo.io/ent/entc/gen"
)

func main() {
	if err := entc.Generate(
		"./schema",
		&gen.Config{
			Target:  "./dao",
			Package: "github.com/chaitin/agent-compose/ent/dao",
			// The runtime opens the released SQL schema itself, so ent's
			// migration DDL is never applied. The features below only shape the
			// query API: raw SQL inside an ent transaction (ExecQuery), upserts
			// (Upsert), row locking (Lock), and query modification (Modifier).
			Features: []gen.Feature{
				gen.FeatureExecQuery,
				gen.FeatureUpsert,
				gen.FeatureLock,
				gen.FeatureModifier,
			},
		},
	); err != nil {
		log.Fatal("running ent codegen: ", err)
	}
}
