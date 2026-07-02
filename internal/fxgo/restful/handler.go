package restful

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
)

func NullHandler[RT ResponseType[any]](w http.ResponseWriter, r *http.Request) {
	// Get the directory of the executable
	ex, err := os.Executable()
	if err != nil {
		slog.Error("Error getting executable path", "err", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	exPath := filepath.Dir(ex)

	// Read server version from git_version file in the executable's directory
	versionBytes, err := os.ReadFile(filepath.Join(exPath, "git_version"))
	version := ""
	if err == nil {
		version = strings.TrimSpace(string(versionBytes))
	} else {
		slog.Error("Error reading git_version file", "err", err)
	}

	resp := NewResponse[any, RT](nil, codes.OK.String(), map[string]any{
		"timestamp":      float64(time.Now().UnixNano()) / 1e9,
		"server_version": version,
	})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
