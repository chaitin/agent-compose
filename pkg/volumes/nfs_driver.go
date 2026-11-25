package volumes

import (
	"context"
	"fmt"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/nfsvolume"
)

// NFSDriver records an existing NFS export as an agent-compose volume. The
// export is owned by the NFS server: creating the volume does not contact the
// server and removing it never deletes data there. Each runtime mounts the
// export itself when a sandbox starts, so the daemon needs neither mount
// privileges nor network access to the server.
type NFSDriver struct{}

func NewNFSDriver() NFSDriver { return NFSDriver{} }

func (NFSDriver) Name() string { return domain.VolumeDriverNFS }

func (d NFSDriver) Create(_ context.Context, record domain.VolumeRecord) (domain.VolumeRecord, error) {
	source, options, err := nfsvolume.FromOptions(record.Options)
	if err != nil {
		return domain.VolumeRecord{}, err
	}
	record.Driver = d.Name()
	record.Options = options
	record.Path = source.Ref()
	return record, nil
}

func (d NFSDriver) Inspect(_ context.Context, record domain.VolumeRecord) (domain.VolumeRecord, error) {
	if _, err := nfsvolume.ParseRef(record.Path); err != nil {
		return domain.VolumeRecord{}, fmt.Errorf("inspect nfs volume %s: %w", record.Name, err)
	}
	record.Driver = d.Name()
	return record, nil
}

// Remove forgets the volume. The export and its files stay on the server.
func (NFSDriver) Remove(context.Context, domain.VolumeRecord) error { return nil }

func (NFSDriver) ResolveMountSource(_ context.Context, record domain.VolumeRecord) (string, error) {
	source, err := nfsvolume.ParseRef(record.Path)
	if err != nil {
		return "", fmt.Errorf("resolve nfs volume %s: %w", record.Name, err)
	}
	return source.Ref(), nil
}
