package skills

import "testing"

func TestIntegrationResolverLocalAndArchiveSources(t *testing.T) {
	t.Run("file source", TestResolverResolvesFileSkill)
	t.Run("artifact manifest", TestResolverArtifactManifestOmitsSourcePathAndCredentials)
	t.Run("zip subdirectory", TestResolverResolvesZipSkillSubdir)
	t.Run("local source boundaries", TestResolverRejectsLocalSourceOutsideAllowedRoots)
	t.Run("compose source root", TestResolverAllowsComposeSourceRoot)
	t.Run("redirect to internal host", TestDownloadFollowsRedirectToInternalHost)
	t.Run("internal artifact host", TestResolverIntegrationFetchesZipSkillFromInternalHost)
	t.Run("zip traversal", TestExtractZipRejectsBackslashTraversal)
	t.Run("zip modes", TestExtractZipSanitizesEntryModes)
	t.Run("expanded size limit", TestExtractZipRejectsDeclaredOversizeArchive)
}
