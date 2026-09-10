package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// These are consumed by agent preparation, not general runtime execution.
// The guest already needs Node for agent-compose-runtime. The probe uses only
// Node's built-in modules and transfers metadata/hashes rather than skill bytes.
type guestDirectoryPublisher interface {
	PublishGuestDirectory(context.Context, *domain.Sandbox, domain.VMState, driverpkg.GuestDirectoryPublication) error
}

type guestSymlinkWriter interface {
	EnsureGuestSymlink(context.Context, *domain.Sandbox, domain.VMState, driverpkg.GuestSymlinkProjection) error
}

type guestAgentSkillsProjection struct {
	Status  string                          `json:"status"`
	Entries []execution.AgentSkillFileEntry `json:"entries"`
}

func (r *AgentRunner) guestSkillsWriterFor(session *domain.Sandbox) execution.GuestSkillsWriterFunc {
	if r == nil || r.runtimes == nil || r.store == nil {
		if session != nil && session.Summary.Driver == "k8s" {
			return guestSkillsWriteError(fmt.Errorf("guest skills runtime dependencies are unavailable"))
		}
		return nil
	}
	runtime, err := r.runtimes.ForSession(session)
	if err != nil {
		return guestSkillsWriteError(fmt.Errorf("resolve guest skills runtime: %w", err))
	}
	if _, writesDirectories := runtime.(GuestDirWriter); !writesDirectories {
		if session != nil && session.Summary.Driver == "k8s" {
			return guestSkillsWriteError(fmt.Errorf("runtime does not support guest directory transfer"))
		}
		return nil
	}
	return func(ctx context.Context, hostSkillsDir string) error {
		publisher, canPublish := runtime.(guestDirectoryPublisher)
		linker, canLink := runtime.(guestSymlinkWriter)
		if !canPublish || !canLink {
			return fmt.Errorf("runtime does not support managed guest skill projection")
		}
		vmState, err := r.store.GetVMState(session.Summary.ID)
		if err != nil {
			return err
		}
		if _, err := runtime.EnsureSandbox(ctx, session, vmState, domain.ProxyState{}); err != nil {
			return fmt.Errorf("prepare guest skills runtime: %w", err)
		}
		expected, err := execution.AgentSkillsProjectionEntries(ctx, hostSkillsDir)
		if err != nil {
			return err
		}
		guestSkills := filepath.Join(r.config.GuestHomePath, ".agents", "skills")
		projection, err := readGuestAgentSkillsProjection(ctx, runtime, session, vmState, guestSkills)
		if err != nil {
			return err
		}
		if err := linker.EnsureGuestSymlink(ctx, session, vmState,
			driverpkg.GuestSymlinkProjection{GuestPath: filepath.Join(r.config.GuestHomePath, ".claude", "skills"), RelativeTarget: "../.agents/skills", ManagedMarkers: []string{".agent-compose-skills.json", ".agent-compose-managed"}}); err != nil {
			return err
		}
		if projection.Status != "present" || !slices.Equal(expected, projection.Entries) {
			if err := publisher.PublishGuestDirectory(ctx, session, vmState, driverpkg.GuestDirectoryPublication{HostSource: hostSkillsDir, GuestDestination: guestSkills, ManagedMarker: ".agent-compose-skills.json"}); err != nil {
				return fmt.Errorf("publish guest skills: %w", err)
			}
		}
		return nil
	}
}

func guestSkillsWriteError(err error) execution.GuestSkillsWriterFunc {
	return func(context.Context, string) error { return err }
}

func readGuestAgentSkillsProjection(ctx context.Context, runtime SandboxRuntime, session *domain.Sandbox, vmState domain.VMState, guestDir string) (guestAgentSkillsProjection, error) {
	result, err := runtime.Exec(ctx, session, vmState, domain.ExecSpec{
		Command: "node", Args: []string{"-e", guestAgentSkillsProbe, guestDir, filepath.Dir(filepath.Dir(guestDir))}, Cwd: "/",
	})
	if err != nil {
		return guestAgentSkillsProjection{}, fmt.Errorf("inspect guest skills: %w", err)
	}
	if !result.Success {
		return guestAgentSkillsProjection{}, fmt.Errorf("inspect guest skills: exit code %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	var projection guestAgentSkillsProjection
	decoder := json.NewDecoder(strings.NewReader(result.Stdout))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&projection); err != nil {
		return guestAgentSkillsProjection{}, fmt.Errorf("decode guest skills: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return guestAgentSkillsProjection{}, fmt.Errorf("unexpected trailing guest skills response")
	}
	switch projection.Status {
	case "present", "missing", "different":
		return projection, nil
	default:
		return guestAgentSkillsProjection{}, fmt.Errorf("unexpected guest skills status %q", projection.Status)
	}
}

const guestAgentSkillsProbe = `
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const root = process.argv[1];
const entries = [];
const home = process.argv[2];
async function walk(directory, prefix) {
  for (const name of await fs.promises.readdir(directory)) {
    if (!prefix && name === '.agent-compose-skills.json') continue;
    const relative = prefix ? prefix + '/' + name : name;
    const full = path.join(directory, name);
    const stat = await fs.promises.lstat(full);
    if (!stat.isDirectory() && !stat.isFile()) return false;
    const entry = {path: relative, kind: stat.isDirectory() ? 'directory' : 'file',
      executable: stat.mode & 0o111, size: 0, sha256: ''};
    if (stat.isFile()) {
      entry.size = stat.size;
      const hash = crypto.createHash('sha256');
      for await (const chunk of fs.createReadStream(full)) hash.update(chunk);
      entry.sha256 = hash.digest('hex');
    }
    entries.push(entry);
    if (stat.isDirectory() && !await walk(full, relative)) return false;
  }
  return true;
}
(async () => {
  // Only the canonical root may be an owned publication symlink. Parent
  // redirection must fail before reading content outside this private home.
  let parent = home;
  for (const component of ['', ...path.relative(home, path.dirname(root)).split('/').filter(Boolean)]) {
    if (component) parent = path.join(parent, component);
    let parentStat;
    try { parentStat = await fs.promises.lstat(parent); }
    catch (error) { if (error.code === 'ENOENT') break; throw error; }
    if (!parentStat.isDirectory() || parentStat.isSymbolicLink()) throw new Error('invalid guest skill parent: ' + parent);
  }
  let stat;
  try {
    stat = await fs.promises.lstat(root);
    if (stat.isSymbolicLink()) {
      const controlName = '.agent-compose-' + path.basename(root);
      const target = await fs.promises.readlink(root);
      const generation = target.slice(controlName.length + 1);
      if (!target.startsWith(controlName + '/generation-') ||
          generation.includes('/') || generation.includes(String.fromCharCode(92))) {
        process.stdout.write(JSON.stringify({status: 'different', entries: []}));
        return;
      }
      const control = path.join(path.dirname(root), controlName);
      for (const [location, directory] of [[control, true], [path.join(control, '.owner'), false], [path.join(control, generation), true]]) {
        const metadata = await fs.promises.lstat(location);
        if (metadata.isSymbolicLink() || (directory ? !metadata.isDirectory() : !metadata.isFile())) throw new Error('invalid guest skill publication path: ' + location);
      }
      const owner = await fs.promises.readFile(path.join(control, '.owner'), 'utf8');
      if (owner !== 'agent-compose-directory-v1:' + root) throw new Error('invalid guest skill publication owner');
      stat = await fs.promises.stat(root);
    }
  }
  catch (error) {
    if (error.code === 'ENOENT') {
      process.stdout.write(JSON.stringify({status: 'missing', entries: []}));
      return;
    }
    throw error;
  }
  let status = 'different';
  if (stat.isDirectory() && await walk(root, '')) status = 'present';
  entries.sort((a, b) => Buffer.compare(Buffer.from(a.path), Buffer.from(b.path)));
  process.stdout.write(JSON.stringify({status, entries: status === 'present' ? entries : []}));
})().catch(error => { process.stderr.write(String(error)); process.exitCode = 1; });
`
