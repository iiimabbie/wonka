package main

import (
	"log"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
)

func ensureCollections(app *pocketbase.PocketBase) {
	// --- agents collection ---
	if _, err := app.FindCollectionByNameOrId("agents"); err != nil {
		collection := core.NewBaseCollection("agents")
		collection.Fields.Add(
			&core.TextField{Name: "name", Required: true},
			&core.TextField{Name: "key_hash", Required: true},
			&core.BoolField{Name: "enabled"},
		)
		collection.AddIndex("idx_agents_key_hash", true, "key_hash", "")

		if err := app.Save(collection); err != nil {
			log.Printf("Warning: failed to create agents collection: %v", err)
		} else {
			log.Println("✅ Created 'agents' collection")
		}
	}

	// --- candy_ledger collection ---
	if _, err := app.FindCollectionByNameOrId("candy_ledger"); err != nil {
		collection := core.NewBaseCollection("candy_ledger")
		collection.Fields.Add(
			&core.TextField{Name: "agent_id", Required: true},
			&core.NumberField{Name: "delta", Required: true},
			&core.TextField{Name: "reason", Required: true},
			&core.TextField{Name: "idempotency_key", Required: true},
		)
		collection.AddIndex("idx_ledger_agent", false, "agent_id", "")
		collection.AddIndex("idx_ledger_idempotency", true, "agent_id, idempotency_key", "")

		if err := app.Save(collection); err != nil {
			log.Printf("Warning: failed to create candy_ledger collection: %v", err)
		} else {
			log.Println("✅ Created 'candy_ledger' collection")
		}
	}
}
