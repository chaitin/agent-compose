package config

import "testing"

func TestIntegrationConfigWorkflows(t *testing.T) {
	testConfigWorkflows(t)
}

func TestE2EConfigWorkflows(t *testing.T) {
	testConfigWorkflows(t)
}

func testConfigWorkflows(t *testing.T) {
	t.Helper()
	t.Run("parses environment", testNewConfigParsesEnvironment)
	t.Run("allows default roots and requires valid driver", testNewConfigAllowsDefaultRootsAndRequiresValidDriver)
	t.Run("defaults daemon listen config", testNewConfigDefaultsDaemonListenConfig)
	t.Run("uses explicit daemon socket", testNewConfigUsesExplicitDaemonSocket)
	t.Run("enables tcp only when http listen is explicit", testNewConfigEnablesTCPOnlyWhenHTTPListenIsExplicit)
	t.Run("rejects invalid daemon addresses", testNewConfigRejectsInvalidDaemonAddresses)
	t.Run("defaults images from default image", testNewConfigDefaultsImagesFromDefaultImage)
	t.Run("rejects invalid image store mode", testNewConfigRejectsInvalidImageStoreMode)
	t.Run("defaults data root from xdg data home", testNewConfigDefaultsDataRootFromXDGDataHome)
	t.Run("ensures host directories exist", testNewConfigEnsuresHostDirectoriesExist)
	t.Run("rejects file data root", testNewConfigRejectsFileDataRoot)
	t.Run("default data root falls back to home", testDefaultDataRootFallsBackToHome)
}
