package loader

import (
	"context"
	"fmt"
	"strings"
)

type RegistryStore interface {
	CreateLoader(ctx context.Context, loader Definition) (Definition, error)
	UpdateLoader(ctx context.Context, loader Definition) (Definition, error)
	DeleteLoader(ctx context.Context, loaderID string) error
	SetLoaderEnabled(ctx context.Context, loaderID string, enabled bool) error
	SetLoaderTriggerEnabled(ctx context.Context, loaderID, triggerID string, enabled bool) error
	ReplaceLoaderTriggers(ctx context.Context, loaderID string, triggers []Trigger) ([]Trigger, error)
	GetLoader(ctx context.Context, loaderID string) (Definition, error)
}

type RegistryValidator interface {
	Validate(ctx context.Context, runtime, script string) (LoaderValidationResult, error)
}

type RegistryRefresher interface {
	Refresh(ctx context.Context) error
}

type RegistryNotifier func(reason string)

func CreateLoader(ctx context.Context, store RegistryStore, validator RegistryValidator, refresher RegistryRefresher, notify RegistryNotifier, loader Definition, defaultScript string) (Definition, error) {
	if strings.TrimSpace(loader.Summary.Runtime) == "" {
		loader.Summary.Runtime = RuntimeScheduler
	}
	if strings.TrimSpace(loader.Script) == "" {
		loader.Script = defaultScript
	}
	validation, err := validator.Validate(ctx, loader.Summary.Runtime, loader.Script)
	if err != nil {
		return Definition{}, err
	}
	created, err := store.CreateLoader(ctx, loader)
	if err != nil {
		return Definition{}, err
	}
	if _, err := store.ReplaceLoaderTriggers(ctx, created.Summary.ID, validation.Triggers); err != nil {
		_ = store.DeleteLoader(ctx, created.Summary.ID)
		return Definition{}, err
	}
	if err := refresher.Refresh(ctx); err != nil {
		return Definition{}, err
	}
	notifyRegistry(notify, "loader_updated")
	return store.GetLoader(ctx, created.Summary.ID)
}

func UpdateLoader(ctx context.Context, store RegistryStore, validator RegistryValidator, refresher RegistryRefresher, notify RegistryNotifier, loader Definition) (Definition, error) {
	validation, err := validator.Validate(ctx, loader.Summary.Runtime, loader.Script)
	if err != nil {
		return Definition{}, err
	}
	updated, err := store.UpdateLoader(ctx, loader)
	if err != nil {
		return Definition{}, err
	}
	if _, err := store.ReplaceLoaderTriggers(ctx, updated.Summary.ID, validation.Triggers); err != nil {
		return Definition{}, err
	}
	if err := refresher.Refresh(ctx); err != nil {
		return Definition{}, err
	}
	notifyRegistry(notify, "loader_updated")
	return store.GetLoader(ctx, updated.Summary.ID)
}

func DeleteLoader(ctx context.Context, store RegistryStore, refresher RegistryRefresher, notify RegistryNotifier, loaderID string) error {
	if err := store.DeleteLoader(ctx, loaderID); err != nil {
		return err
	}
	if err := refresher.Refresh(ctx); err != nil {
		return err
	}
	notifyRegistry(notify, "loader_updated")
	return nil
}

func SetLoaderEnabled(ctx context.Context, store RegistryStore, refresher RegistryRefresher, notify RegistryNotifier, loaderID string, enabled bool) (Definition, error) {
	if err := store.SetLoaderEnabled(ctx, loaderID, enabled); err != nil {
		return Definition{}, err
	}
	if err := refresher.Refresh(ctx); err != nil {
		return Definition{}, err
	}
	notifyRegistry(notify, "loader_updated")
	return store.GetLoader(ctx, loaderID)
}

func SetLoaderTriggerEnabled(ctx context.Context, store RegistryStore, refresher RegistryRefresher, notify RegistryNotifier, loaderID, triggerID string, enabled bool) (Definition, error) {
	if err := store.SetLoaderTriggerEnabled(ctx, loaderID, triggerID, enabled); err != nil {
		return Definition{}, err
	}
	if err := refresher.Refresh(ctx); err != nil {
		return Definition{}, err
	}
	notifyRegistry(notify, "loader_updated")
	return store.GetLoader(ctx, loaderID)
}

func LoadLoaderForRun(ctx context.Context, store RegistryStore, loaderID, triggerID string) (Definition, *Trigger, error) {
	loader, err := store.GetLoader(ctx, loaderID)
	if err != nil {
		return Definition{}, nil, err
	}
	if strings.TrimSpace(triggerID) == "" {
		return loader, nil, nil
	}
	triggerID = strings.TrimSpace(triggerID)
	for _, item := range loader.Triggers {
		if item.ID == triggerID {
			current := item
			return loader, &current, nil
		}
	}
	return Definition{}, nil, fmt.Errorf("loader trigger %s/%s not found", loaderID, triggerID)
}

func notifyRegistry(notify RegistryNotifier, reason string) {
	if notify != nil {
		notify(reason)
	}
}
