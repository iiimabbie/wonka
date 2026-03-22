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
			&core.AutodateField{Name: "created_at", OnCreate: true},
		)
		collection.AddIndex("idx_ledger_agent", false, "agent_id", "")
		collection.AddIndex("idx_ledger_idempotency", true, "agent_id, idempotency_key", "")

		if err := app.Save(collection); err != nil {
			log.Printf("Warning: failed to create candy_ledger collection: %v", err)
		} else {
			log.Println("✅ Created 'candy_ledger' collection")
		}
	} else {
		// Migrations
		migrateAddCreatedAt(app)
		migrateAddAgentRelation(app)
	}

	// Migrations for v2
	migrateAddAgentType(app)
	migrateAddTransferId(app)

	// --- transfers collection ---
	ensureTransfersCollection(app)

	// --- agent_balances view ---
	ensureAgentBalancesView(app)
}

func migrateAddCreatedAt(app *pocketbase.PocketBase) {
	collection, err := app.FindCollectionByNameOrId("candy_ledger")
	if err != nil {
		return
	}

	for _, f := range collection.Fields {
		if f.GetName() == "created_at" {
			return
		}
	}

	collection.Fields.Add(
		&core.AutodateField{Name: "created_at", OnCreate: true},
	)

	if err := app.Save(collection); err != nil {
		log.Printf("Warning: failed to add created_at to candy_ledger: %v", err)
	} else {
		log.Println("✅ Migrated candy_ledger: added created_at field")
	}
}

func migrateAddAgentRelation(app *pocketbase.PocketBase) {
	collection, err := app.FindCollectionByNameOrId("candy_ledger")
	if err != nil {
		return
	}

	// Check if agent relation field already exists
	for _, f := range collection.Fields {
		if f.GetName() == "agent" {
			return
		}
	}

	agentsCollection, err := app.FindCollectionByNameOrId("agents")
	if err != nil {
		log.Printf("Warning: agents collection not found, skipping agent relation migration: %v", err)
		return
	}

	collection.Fields.Add(
		&core.RelationField{
			Name:         "agent",
			CollectionId: agentsCollection.Id,
			MaxSelect:    1,
		},
	)

	if err := app.Save(collection); err != nil {
		log.Printf("Warning: failed to add agent relation to candy_ledger: %v", err)
		return
	}
	log.Println("✅ Migrated candy_ledger: added agent relation field")

	// Backfill existing records
	records, err := app.FindAllRecords("candy_ledger")
	if err != nil {
		log.Printf("Warning: failed to fetch candy_ledger records for backfill: %v", err)
		return
	}

	for _, r := range records {
		if r.GetString("agent") == "" {
			r.Set("agent", r.GetString("agent_id"))
			if err := app.Save(r); err != nil {
				log.Printf("Warning: failed to backfill agent for record %s: %v", r.Id, err)
			}
		}
	}
	log.Printf("✅ Backfilled agent relation for %d records", len(records))
}

func migrateAddAgentType(app *pocketbase.PocketBase) {
	collection, err := app.FindCollectionByNameOrId("agents")
	if err != nil {
		return
	}

	for _, f := range collection.Fields {
		if f.GetName() == "type" {
			return
		}
	}

	collection.Fields.Add(
		&core.TextField{Name: "type"},
	)

	if err := app.Save(collection); err != nil {
		log.Printf("Warning: failed to add type to agents: %v", err)
	} else {
		log.Println("✅ Migrated agents: added type field")
	}
}

func migrateAddTransferId(app *pocketbase.PocketBase) {
	collection, err := app.FindCollectionByNameOrId("candy_ledger")
	if err != nil {
		return
	}

	for _, f := range collection.Fields {
		if f.GetName() == "transfer_id" {
			return
		}
	}

	transfersCol, err := app.FindCollectionByNameOrId("transfers")
	if err != nil {
		// transfers collection doesn't exist yet, skip
		return
	}

	collection.Fields.Add(
		&core.RelationField{
			Name:         "transfer_id",
			CollectionId: transfersCol.Id,
			MaxSelect:    1,
		},
	)

	if err := app.Save(collection); err != nil {
		log.Printf("Warning: failed to add transfer_id to candy_ledger: %v", err)
	} else {
		log.Println("✅ Migrated candy_ledger: added transfer_id field")
	}
}

func ensureTransfersCollection(app *pocketbase.PocketBase) {
	if _, err := app.FindCollectionByNameOrId("transfers"); err == nil {
		return // already exists
	}

	agentsCol, err := app.FindCollectionByNameOrId("agents")
	if err != nil {
		log.Printf("Warning: agents collection not found, skipping transfers creation")
		return
	}

	collection := core.NewBaseCollection("transfers")
	collection.Fields.Add(
		&core.RelationField{
			Name:         "from_agent",
			CollectionId: agentsCol.Id,
			MaxSelect:    1,
			Required:     true,
		},
		&core.RelationField{
			Name:         "to_agent",
			CollectionId: agentsCol.Id,
			MaxSelect:    1,
			Required:     true,
		},
		&core.NumberField{Name: "amount", Required: true},
		&core.TextField{Name: "reason", Required: true},
		&core.TextField{Name: "idempotency_key", Required: true},
		&core.AutodateField{Name: "created_at", OnCreate: true},
	)
	collection.AddIndex("idx_transfers_from", false, "from_agent", "")
	collection.AddIndex("idx_transfers_to", false, "to_agent", "")
	collection.AddIndex("idx_transfers_idempotency", true, "idempotency_key", "")

	if err := app.Save(collection); err != nil {
		log.Printf("Warning: failed to create transfers collection: %v", err)
	} else {
		log.Println("✅ Created 'transfers' collection")
	}
}

func ensureAgentBalancesView(app *pocketbase.PocketBase) {
	if _, err := app.FindCollectionByNameOrId("agent_balances"); err == nil {
		return // already exists
	}

	view := core.NewViewCollection("agent_balances")
	view.ViewQuery = `SELECT a.id, a.name, a.enabled, COALESCE(SUM(cl.delta), 0) as balance FROM agents a LEFT JOIN candy_ledger cl ON cl.agent_id = a.id GROUP BY a.id`

	if err := app.Save(view); err != nil {
		log.Printf("Warning: failed to create agent_balances view: %v", err)
	} else {
		log.Println("✅ Created 'agent_balances' view")
	}
}
