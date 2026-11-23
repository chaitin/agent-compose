package pgcopy

// copyTables lists the application tables in foreign-key dependency order.
// schema_migrations is deliberately excluded: the target records only the
// migrations it applied itself. Keep this list in sync with the PostgreSQL
// baseline and the SQLite final schema; Copy rejects a mismatch.
var copyTables = []string{
	"capability_gateway",
	"credential_handle",
	"event",
	"event_delivery",
	"event_sandbox_link",
	"global_env",
	"llm_facade_token",
	"llm_model",
	"llm_provider",
	"llm_provider_model",
	"project",
	"sandbox_projection_meta",
	"sandboxes",
	"volumes",
	"webhook_source",
	"workspace_config",
	"llm_catalog_default",
	"project_agent",
	"project_revision",
	"project_volumes",
	"project_run",
	"project_scheduler",
	"project_run_completion",
	"project_run_event",
	"project_run_label",
	"scheduler_event",
	"scheduler_run",
	"scheduler_sandbox_binding",
	"scheduler_state",
	"scheduler_trigger",
}
