package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

const dockerSkillsE2ERoot = "/root/.agents/skills"

// The real Codex SDK still launches a subprocess and consumes its NDJSON. Only
// the external provider CLI is replaced; every prompt traverses ExecuteAgentRun
// and prepares agent files, unlike a reused sandbox's Command run.
const dockerSkillsE2ECodexFixture = `#!/usr/bin/env node
require("fs").appendFileSync("/data/state/codex-fixture.calls", "called\n");
process.stdin.resume();
for (const event of [
  {type:"thread.started",thread_id:"skills-e2e-thread"},
  {type:"item.completed",item:{id:"answer",type:"agent_message",text:"skills-e2e-done"}},
  {type:"turn.completed",usage:{input_tokens:0,cached_input_tokens:0,output_tokens:0}}
]) process.stdout.write(JSON.stringify(event)+"\n");
`

type dockerSkillsE2EInventory struct {
	Entries        int    `json:"entries"`
	Files          int    `json:"files"`
	Bytes          int64  `json:"bytes"`
	TreeHash       string `json:"tree_hash"`
	InodeHash      string `json:"inode_hash"`
	CanonicalPath  string `json:"canonical_path"`
	ClaudePath     string `json:"claude_path"`
	ClaudeLink     string `json:"claude_link"`
	ClaudeIdentity string `json:"claude_identity"`
}

func TestE2EDockerSkillsReuse(t *testing.T) {
	image := strings.TrimSpace(os.Getenv(dockerWorkspaceE2EImageEnv))
	if image == "" {
		t.Skipf("set %s to the local workspace-test guest image", dockerWorkspaceE2EImageEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	repoRoot := e2eRepoRoot(t)
	root, err := os.MkdirTemp(repoRoot, ".skills-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	dataRoot := filepath.Join(root, "data")
	if err := os.MkdirAll(dataRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "skills", "pdf")
	files := writeDockerSkillsE2EFixture(t, source, dockerSkillsE2EFileCount(t))
	binary := e2eDaemonBinary(t, ctx, repoRoot, root)
	dockerClient := newE2EDockerClient(t, ctx, image)
	address := unusedLoopbackAddress(t)
	baseURL := "http://" + address
	daemon := startE2EDaemon(t, binary, repoRoot, dataRoot, address, image)
	waitForE2EDaemon(t, ctx, daemon, baseURL)
	httpClient := newE2EHTTPClient()
	httpClient.Timeout = 3 * time.Minute
	defer httpClient.CloseIdleConnections()
	projects := agentcomposev2connect.NewProjectServiceClient(httpClient, baseURL)
	runs := agentcomposev2connect.NewRunServiceClient(httpClient, baseURL)
	sandboxes := agentcomposev2connect.NewSandboxServiceClient(httpClient, baseURL)
	execs := newE2EExecClient(httpClient, baseURL)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("skills E2E daemon log:\n%s", daemon.logs.String())
		}
	})
	projectID := applyDockerSkillsE2EYAML(t, ctx, binary, baseURL, root, image)
	first := runE2EWorkspaceSandbox(t, ctx, runs, sandboxes, projectID, "skills-first-bootstrap")
	firstID := first.GetSandboxId()
	removedFirst, removedSecond := false, false
	t.Cleanup(func() { cleanupE2EWorkspaceSandbox(t, dockerClient, sandboxes, firstID, removedFirst) })
	second := runE2EWorkspaceSandbox(t, ctx, runs, sandboxes, projectID, "skills-second-bootstrap")
	secondID := second.GetSandboxId()
	t.Cleanup(func() { cleanupE2EWorkspaceSandbox(t, dockerClient, sandboxes, secondID, removedSecond) })
	if firstID == secondID {
		t.Fatal("independent bootstrap runs reused the same sandbox")
	}
	for _, id := range []string{firstID, secondID} {
		installDockerSkillsE2ECodexFixture(t, ctx, execs, id)
		assertDockerSkillsE2ECodexCalls(t, ctx, execs, id, 0)
		assertWorkspaceMountE2EManifest(t, ctx, execs, id, dockerSkillsE2ERoot+"/pdf", files)
	}
	baseline := inventoryDockerSkillsE2E(t, ctx, execs, firstID)
	secondBefore := inventoryDockerSkillsE2E(t, ctx, execs, secondID)
	if baseline.TreeHash != secondBefore.TreeHash || baseline.Files != len(files)+1 {
		t.Fatalf("two initial private trees differ or contain unexpected files: first=%+v second=%+v", baseline, secondBefore)
	}
	assertDockerSkillsE2ECopies(t, dataRoot, len(files), 1, 2)
	for attempt := range 3 {
		started := time.Now()
		runDockerSkillsE2EPrompt(t, ctx, runs, projectID, firstID, fmt.Sprintf("skills-unchanged-%d", attempt))
		current := inventoryDockerSkillsE2E(t, ctx, execs, firstID)
		if current != baseline {
			t.Fatalf("unchanged prompt %d replaced an inode, content or alias: before=%+v after=%+v", attempt, baseline, current)
		}
		t.Logf("skills_measurement phase=unchanged run=%d source_files=%d canonical_entries=%d canonical_files=%d canonical_bytes=%d same_tree=true same_inodes=true same_alias=true run_ms=%d", attempt+1, len(files), current.Entries, current.Files, current.Bytes, time.Since(started).Milliseconds())
	}
	assertDockerSkillsE2ECopies(t, dataRoot, len(files), 1, 2)
	mutate := runE2EWorkspaceCommand(t, ctx, execs, firstID, "node", "-e", `const fs=require("fs"),r=process.argv[1];fs.writeFileSync(r+"/SKILL.md","guest mutation");fs.unlinkSync(r+"/files/file-000000.txt");fs.writeFileSync(r+"/extra.txt","guest extra");fs.mkdirSync(r+"/extra-dir");fs.chmodSync(r+"/files/file-000001.txt",0o755);fs.chmodSync(r,0o700);`, dockerSkillsE2ERoot+"/pdf")
	requireE2EExecSuccess(t, "mutate private guest skills", mutate)
	assertWorkspaceMountE2EHostManifest(t, source, files)
	if got := inventoryDockerSkillsE2E(t, ctx, execs, secondID); got != secondBefore {
		t.Fatal("first sandbox mutations changed the second sandbox")
	}
	runDockerSkillsE2EPrompt(t, ctx, runs, projectID, firstID, "skills-repair-guest")
	repaired := inventoryDockerSkillsE2E(t, ctx, execs, firstID)
	if repaired.TreeHash != baseline.TreeHash || repaired.Files != baseline.Files {
		t.Fatalf("prompt failed to restore modified/deleted/extra files, directories or executable permissions: before=%+v repaired=%+v", baseline, repaired)
	}
	assertWorkspaceMountE2EManifest(t, ctx, execs, firstID, dockerSkillsE2ERoot+"/pdf", files)
	assertWorkspaceMountE2EHostManifest(t, source, files)

	updated := maps.Clone(files)
	updated["SKILL.md"] += "\nUpdated source version.\n"
	updated["files/file-000001.txt"] = "skills-e2e:changed source file"
	for _, name := range []string{"SKILL.md", "files/file-000001.txt"} {
		writeE2EHostFile(t, filepath.Join(source, name), updated[name])
	}
	if got := inventoryDockerSkillsE2E(t, ctx, execs, firstID); got != repaired {
		t.Fatal("source change mutated a previously published private copy")
	}
	runDockerSkillsE2EPrompt(t, ctx, runs, projectID, firstID, "skills-update-source")
	updatedFirst := inventoryDockerSkillsE2E(t, ctx, execs, firstID)
	if updatedFirst.TreeHash == repaired.TreeHash {
		t.Fatal("source update did not change the published skills tree")
	}
	assertWorkspaceMountE2EManifest(t, ctx, execs, firstID, dockerSkillsE2ERoot+"/pdf", updated)
	if got := inventoryDockerSkillsE2E(t, ctx, execs, secondID); got != secondBefore {
		t.Fatal("updating the first sandbox changed the second sandbox")
	}
	secondMutation := runE2EWorkspaceCommand(t, ctx, execs, secondID, "node", "-e", `require("fs").writeFileSync(process.argv[1]+"/SKILL.md","second sandbox mutation")`, dockerSkillsE2ERoot+"/pdf")
	requireE2EExecSuccess(t, "mutate second sandbox private skills", secondMutation)
	if got := inventoryDockerSkillsE2E(t, ctx, execs, firstID); got != updatedFirst {
		t.Fatal("second sandbox mutation changed the first sandbox")
	}
	assertWorkspaceMountE2EHostManifest(t, source, updated)
	runDockerSkillsE2EPrompt(t, ctx, runs, projectID, secondID, "skills-update-second")
	updatedSecond := inventoryDockerSkillsE2E(t, ctx, execs, secondID)
	if updatedSecond.TreeHash != updatedFirst.TreeHash {
		t.Fatal("second sandbox did not receive the complete updated source tree")
	}
	assertWorkspaceMountE2EManifest(t, ctx, execs, secondID, dockerSkillsE2ERoot+"/pdf", updated)
	assertDockerSkillsE2ECodexCalls(t, ctx, execs, firstID, 5)
	assertDockerSkillsE2ECodexCalls(t, ctx, execs, secondID, 1)
	t.Log("skills_measurement phase=fixture-calls bootstrap_calls=0 first_prompt_calls=5 second_prompt_calls=1 total_prompt_calls=6")
	assertDockerSkillsE2ECopies(t, dataRoot, len(files), 2, 2)
	for _, sandbox := range []*agentcomposev2.Sandbox{first, second} {
		assertDockerSkillsE2ENoStages(t, filepath.Dir(sandbox.GetWorkspacePath()))
	}
	removeE2ESandboxPublic(t, ctx, sandboxes, firstID)
	removedFirst = true
	assertWorkspaceMountE2EHostManifest(t, source, updated)
	removeE2ESandboxPublic(t, ctx, sandboxes, secondID)
	removedSecond = true
	assertWorkspaceMountE2EHostManifest(t, source, updated)
	assertE2EDockerSandboxContainerCount(t, ctx, dockerClient, firstID, 0)
	assertE2EDockerSandboxContainerCount(t, ctx, dockerClient, secondID, 0)
	if _, err := projects.RemoveProject(ctx, connect.NewRequest(&agentcomposev2.RemoveProjectRequest{Project: &agentcomposev2.ProjectRef{Selector: &agentcomposev2.ProjectRef_ProjectId{ProjectId: projectID}}})); err != nil {
		t.Fatal(err)
	}
	t.Logf("skills_measurement phase=complete source_files=%d sandbox_count=2 unchanged_prompt_runs=3 guest_mutation_repaired=true source_update_delivered=true bidirectional_isolation=true source_preserved_after_removal=true", len(files))
}

func applyDockerSkillsE2EYAML(t *testing.T, ctx context.Context, binary, baseURL, root, image string) string {
	t.Helper()
	yaml := fmt.Sprintf("name: skills-reuse-e2e\nagents:\n  worker:\n    provider: codex\n    image: %s\n    driver:\n      docker: {}\n    env:\n      CODEX_BIN: /data/state/codex-fixture\n    skills:\n      - name: pdf\n        provider: file\n        path: ./skills/pdf\n    sandbox:\n      stopped_runtime_policy: retain\n", image)
	compose := filepath.Join(root, "agent-compose.yml")
	writeE2EHostFile(t, compose, yaml)
	command := exec.CommandContext(ctx, binary, "up", "--host", baseURL, "--file", compose)
	command.Env = overrideE2EEnv(os.Environ(), map[string]string{"AGENT_COMPOSE_AUTH_TOKEN": "", "AUTH_PASSWORD": "", "AUTH_USERNAME": ""})
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("apply skills YAML through CLI: %v\n%s", err, output)
	}
	httpClient := newE2EHTTPClient()
	defer httpClient.CloseIdleConnections()
	client := agentcomposev2connect.NewProjectServiceClient(httpClient, baseURL)
	response, err := client.GetProject(ctx, connect.NewRequest(&agentcomposev2.GetProjectRequest{Project: &agentcomposev2.ProjectRef{Selector: &agentcomposev2.ProjectRef_Name{Name: "skills-reuse-e2e"}}}))
	if err != nil {
		t.Fatal(err)
	}
	projectID := response.Msg.GetProject().GetSummary().GetProjectId()
	if projectID == "" {
		t.Fatal("GetProject omitted skills project identity")
	}
	return projectID
}

func dockerSkillsE2EFileCount(t *testing.T) int {
	t.Helper()
	value := strings.TrimSpace(os.Getenv("AGENT_COMPOSE_E2E_SKILL_FILES"))
	if value == "" {
		return 257
	}
	count, err := strconv.Atoi(value)
	if err != nil || count < 3 || count > 100000 {
		t.Fatal("AGENT_COMPOSE_E2E_SKILL_FILES must be between 3 and 100000")
	}
	return count
}

func writeDockerSkillsE2EFixture(t *testing.T, source string, count int) map[string]string {
	t.Helper()
	files := map[string]string{"SKILL.md": "---\nname: pdf\ndescription: Isolated Docker skills reuse fixture\n---\nSkills E2E original version.\n"}
	for index := 0; index < count-1; index++ {
		files[fmt.Sprintf("files/file-%06d.txt", index)] = fmt.Sprintf("skills-e2e:%06d:%s", index, strings.Repeat("x", 1024))
	}
	if err := os.MkdirAll(filepath.Join(source, "empty-directory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source, "files"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, contents := range files {
		writeE2EHostFile(t, filepath.Join(source, name), contents)
	}
	return files
}

func installDockerSkillsE2ECodexFixture(t *testing.T, ctx context.Context, client agentcomposev2connect.ExecServiceClient, sandboxID string) {
	t.Helper()
	result := runE2EWorkspaceCommand(t, ctx, client, sandboxID, "node", "-e", `const fs=require("fs"),path="/data/state/codex-fixture";if(process.env.CODEX_BIN!==path)throw Error("persistent CODEX_BIN missing");fs.writeFileSync(path,process.argv[1],{mode:0o755});fs.accessSync(path,fs.constants.X_OK);`, dockerSkillsE2ECodexFixture)
	requireE2EExecSuccess(t, "install test-owned Codex CLI fixture through Exec", result)
}

func assertDockerSkillsE2ECodexCalls(t *testing.T, ctx context.Context, client agentcomposev2connect.ExecServiceClient, sandboxID string, expected int) {
	t.Helper()
	result := runE2EWorkspaceCommand(t, ctx, client, sandboxID, "node", "-e", `const fs=require("fs"),path="/data/state/codex-fixture.calls";console.log(fs.existsSync(path)?fs.readFileSync(path,"utf8").split("\n").filter(Boolean).length:0);`)
	requireE2EExecSuccess(t, "count local Codex CLI fixture invocations", result)
	count, err := strconv.Atoi(strings.TrimSpace(result.GetStdout()))
	if err != nil || count != expected {
		t.Fatalf("Codex fixture invocations: got %q, want %d", result.GetStdout(), expected)
	}
}

func runDockerSkillsE2EPrompt(t *testing.T, ctx context.Context, client agentcomposev2connect.RunServiceClient, projectID, sandboxID, requestID string) {
	t.Helper()
	response, err := client.RunAgent(ctx, connect.NewRequest(&agentcomposev2.RunAgentRequest{
		ProjectId: projectID, AgentName: "worker", SandboxId: sandboxID,
		Prompt: "Complete the local skills delivery verification fixture.",
		Source: agentcomposev2.RunSource_RUN_SOURCE_API, CleanupPolicy: agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_KEEP_RUNNING,
		ClientRequestId: requestID,
	}))
	if err != nil {
		t.Fatalf("prompt run %s: %v", requestID, err)
	}
	run := response.Msg.GetRun()
	if run.GetSummary().GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED || run.GetSummary().GetSandboxId() != sandboxID || !strings.Contains(run.GetOutput()+run.GetResultJson(), "skills-e2e-done") {
		t.Fatalf("fixture prompt %s did not complete through Codex SDK: status=%s sandbox=%s error=%s output=%s result=%s", requestID, run.GetSummary().GetStatus(), run.GetSummary().GetSandboxId(), run.GetSummary().GetError(), run.GetOutput(), run.GetResultJson())
	}
}

func inventoryDockerSkillsE2E(t *testing.T, ctx context.Context, client agentcomposev2connect.ExecServiceClient, sandboxID string) dockerSkillsE2EInventory {
	t.Helper()
	const script = `const fs=require("fs"),p=require("path"),crypto=require("crypto"),root=process.argv[1];
const entries=[],inodes=[];let files=0,bytes=0;
const sha=x=>crypto.createHash("sha256").update(x).digest("hex");
function walk(rel){const path=p.join(root,rel),st=fs.lstatSync(path),entry={path:rel,executable:st.mode&0o111};inodes.push({path:rel,device:String(st.dev),inode:String(st.ino)});if(st.isDirectory()){entry.kind="directory";entries.push(entry);for(const name of fs.readdirSync(path).sort())walk(rel==="."?name:rel+"/"+name)}else if(st.isFile()){const data=fs.readFileSync(path);entry.kind="file";entry.size=data.length;entry.sha256=sha(data);files++;bytes+=data.length;entries.push(entry)}else throw Error("unexpected entry "+rel)}
walk(".");entries.sort((a,b)=>a.path<b.path?-1:a.path>b.path?1:0);inodes.sort((a,b)=>a.path<b.path?-1:a.path>b.path?1:0);
const alias="/root/.claude/skills",st=fs.lstatSync(alias);if(!st.isSymbolicLink())throw Error("Claude alias is not a symlink");
console.log(JSON.stringify({entries:entries.length,files,bytes,tree_hash:sha(JSON.stringify(entries)),inode_hash:sha(JSON.stringify(inodes)),canonical_path:fs.realpathSync(root),claude_path:fs.realpathSync(alias),claude_link:fs.readlinkSync(alias),claude_identity:st.dev+":"+st.ino}));`
	result := runE2EWorkspaceCommand(t, ctx, client, sandboxID, "node", "-e", script, dockerSkillsE2ERoot)
	requireE2EExecSuccess(t, "inventory complete skills tree and Claude alias", result)
	var inventory dockerSkillsE2EInventory
	if err := json.Unmarshal([]byte(result.GetStdout()), &inventory); err != nil {
		t.Fatalf("decode skills inventory: %v\n%s", err, result.GetStdout())
	}
	if inventory.CanonicalPath != inventory.ClaudePath || inventory.ClaudeLink != "../.agents/skills" || len(inventory.InodeHash) != 64 || len(inventory.TreeHash) != 64 || inventory.Files < 1 {
		t.Fatalf("incomplete skills inventory or inconsistent Claude exposure: %+v", inventory)
	}
	return inventory
}

func assertDockerSkillsE2ECopies(t *testing.T, dataRoot string, files, generations, sandboxes int) {
	t.Helper()
	cacheFiles, privateFiles, artifacts := 0, 0, 0
	entries, err := os.ReadDir(filepath.Join(dataRoot, "skills"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			artifacts++
			cacheFiles += countDockerSkillsE2EFiles(t, filepath.Join(dataRoot, "skills", entry.Name(), "content"))
		}
	}
	privateRoots, err := filepath.Glob(filepath.Join(dataRoot, "sandboxes", "*", "*", "*", "*", "home", ".agents", "skills", "pdf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range privateRoots {
		privateFiles += countDockerSkillsE2EFiles(t, root)
	}
	if artifacts != generations || cacheFiles != files*generations || len(privateRoots) != sandboxes || privateFiles != files*sandboxes {
		t.Fatalf("skills copy accounting: cache_artifacts=%d cache_files=%d private_roots=%d private_files=%d; want generations=%d source_files=%d sandboxes=%d", artifacts, cacheFiles, len(privateRoots), privateFiles, generations, files, sandboxes)
	}
	t.Logf("skills_measurement phase=copy-count source_files=%d cache_artifacts=%d cache_content_files=%d sandbox_private_files=%d sandbox_count=%d cross_sandbox_zero_copy=false", files, artifacts, cacheFiles, privateFiles, sandboxes)
}

func countDockerSkillsE2EFiles(t *testing.T, root string) int {
	t.Helper()
	count := 0
	if err := filepath.WalkDir(root, func(_ string, entry fs.DirEntry, err error) error {
		if err == nil && entry.Type().IsRegular() {
			count++
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertDockerSkillsE2ENoStages(t *testing.T, sandboxRoot string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(sandboxRoot, "home", ".agents"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".skills-update-") || strings.HasPrefix(entry.Name(), ".skills-metadata-") {
			t.Fatalf("owned skill staging accumulated after successful runs: %s", entry.Name())
		}
	}
}
