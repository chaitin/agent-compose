package api

import "github.com/chaitin/agent-compose/pkg/storage/configstore"

// The run handler finds these stores by type assertion, so a missing method
// would only fail at runtime with "store is required". Pin that the real
// store provides them; handler tests use fakes and cannot.
var (
	_ RunEventStore   = (*configstore.ConfigStore)(nil)
	_ RunMessageStore = (*configstore.ConfigStore)(nil)
)
