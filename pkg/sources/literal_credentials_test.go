package sources

import (
	"context"
	"encoding/base64"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSourceAuthenticationUsesLiteralCredentials(t *testing.T) {
	t.Setenv("AUTH_VALUE", "process-value")
	for _, value := range []string{"${AUTH_VALUE}", "prefix-${AUTH_VALUE}", "$AUTH_VALUE", "${ABSENT_VALUE}", "ordinary-value"} {
		t.Run(value, func(t *testing.T) {
			source := Source{Username: value, Password: value}
			req := httptest.NewRequest("GET", "http://source.test", nil)
			ApplyHTTPAuthentication(req, source)
			user, password, ok := req.BasicAuth()
			if !ok || user != value || password != value {
				t.Fatal("HTTP credentials changed")
			}
			client := GitClient{}
			want := "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(value+":"+value))
			if got := client.authorizationHeader(source); got != want {
				t.Fatal("Git credentials changed")
			}
			source.Token = value
			ApplyHTTPAuthentication(req, source)
			if req.Header.Get("Authorization") != "Bearer "+value {
				t.Fatal("HTTP token changed")
			}
			source.Username = ""
			cmd := client.command(context.Background(), "", source, "ls-remote", "--", "http://source.test/repo")
			want = "GIT_CONFIG_VALUE_0=Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("oauth2:"+value))
			if !strings.Contains(strings.Join(cmd.Env, "\n"), want) {
				t.Fatal("Git command token changed")
			}
		})
	}
}
