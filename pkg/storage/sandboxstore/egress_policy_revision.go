package sandboxstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chaitin/agent-compose/pkg/egress"
)

// egressPolicyRevisionPath is the sandbox-owned record of the egress policy
// revision the daemon has observed for this sandbox. It lives beside the other
// runtime facts so removing a sandbox removes its revision with it.
func (s *Store) egressPolicyRevisionPath(id string) string {
	return filepath.Join(s.sandboxDir(id), "egress", "policy-revision.json")
}

// GetEgressPolicyRevision returns the persisted revision for one sandbox. A
// sandbox that has never had a policy observed returns the zero value, which
// Recorded reports as absent rather than as revision zero.
func (s *Store) GetEgressPolicyRevision(ctx context.Context, id string) (egress.PolicyRevision, error) {
	id = strings.TrimSpace(id)
	if err := validateSandboxIDForRemove(id); err != nil {
		return egress.PolicyRevision{}, err
	}
	if err := ctx.Err(); err != nil {
		return egress.PolicyRevision{}, err
	}
	revision, err := s.readEgressPolicyRevision(id)
	if errors.Is(err, os.ErrNotExist) {
		return egress.PolicyRevision{}, nil
	}
	return revision, err
}

// ObserveEgressPolicyRevision records the effective policy currently observed
// for one sandbox. The revision advances only when hash differs from the stored
// one, so replaying the same policy is idempotent; AppliedAt is the first time
// that revision was persisted and AckAt is the first time an enforcement point
// acknowledged it. acknowledge is false for a reader such as inspect, which must
// not turn its own report into proof that the running sandbox has the policy.
//
// The read-modify-write holds the sandbox lock and replaces the file atomically,
// so concurrent proxy requests and a start cannot produce a torn record or skip
// a revision.
func (s *Store) ObserveEgressPolicyRevision(ctx context.Context, id, hash string, acknowledge bool) (egress.PolicyRevision, error) {
	id = strings.TrimSpace(id)
	if err := validateSandboxIDForRemove(id); err != nil {
		return egress.PolicyRevision{}, err
	}
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return egress.PolicyRevision{}, fmt.Errorf("egress policy revision for sandbox %s requires a hash", id)
	}
	unlock := s.lockSandbox(id)
	defer unlock()
	if err := ctx.Err(); err != nil {
		return egress.PolicyRevision{}, err
	}
	current, err := s.readEgressPolicyRevision(id)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return egress.PolicyRevision{}, err
	}
	now := s.currentTime().UTC()
	if current.Hash == hash {
		if !acknowledge || current.Acknowledged() {
			return current, nil
		}
		current.AckAt = now
		if err := s.writeEgressPolicyRevision(id, current); err != nil {
			return egress.PolicyRevision{}, err
		}
		return current, nil
	}
	next := egress.PolicyRevision{Revision: current.Revision + 1, Hash: hash, AppliedAt: now}
	if acknowledge {
		next.AckAt = now
	}
	if err := s.writeEgressPolicyRevision(id, next); err != nil {
		return egress.PolicyRevision{}, err
	}
	return next, nil
}

func (s *Store) readEgressPolicyRevision(id string) (egress.PolicyRevision, error) {
	data, err := os.ReadFile(s.egressPolicyRevisionPath(id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return egress.PolicyRevision{}, os.ErrNotExist
		}
		return egress.PolicyRevision{}, fmt.Errorf("read egress policy revision for sandbox %s: %w", id, err)
	}
	var revision egress.PolicyRevision
	if err := json.Unmarshal(data, &revision); err != nil {
		return egress.PolicyRevision{}, fmt.Errorf("decode egress policy revision for sandbox %s: %w", id, err)
	}
	return revision, nil
}

func (s *Store) writeEgressPolicyRevision(id string, revision egress.PolicyRevision) error {
	path := s.egressPolicyRevisionPath(id)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create egress policy revision directory for sandbox %s: %w", id, err)
	}
	data, err := json.MarshalIndent(revision, "", "  ")
	if err != nil {
		return fmt.Errorf("encode egress policy revision for sandbox %s: %w", id, err)
	}
	if err := writeFileAtomically(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("persist egress policy revision for sandbox %s: %w", id, err)
	}
	return nil
}
