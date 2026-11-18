package forge

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestGitHubCreateRelease(t *testing.T) {
	var gotPath, gotAuth, gotAccept string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotAccept = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Accept")
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotBody)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	c, err := Detect("https://github.com/acme/mono", env(map[string]string{"GITHUB_TOKEN": "tok", "GITHUB_API_URL": srv.URL}))
	if err != nil {
		t.Fatal(err)
	}
	created, err := c.CreateRelease("platform@1.2.0-beta.1", "platform 1.2.0-beta.1", "## notes", true)
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	if gotPath != "/repos/acme/mono/releases" || gotAuth != "Bearer tok" || !strings.Contains(gotAccept, "github") {
		t.Errorf("request: %s %s %s", gotPath, gotAuth, gotAccept)
	}
	if gotBody["tag_name"] != "platform@1.2.0-beta.1" || gotBody["body"] != "## notes" || gotBody["prerelease"] != true {
		t.Errorf("payload: %v", gotBody)
	}
}

func TestGitHubExistingReleaseIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		io.WriteString(w, `{"message":"Validation Failed","errors":[{"resource":"Release","code":"already_exists","field":"tag_name"}]}`)
	}))
	defer srv.Close()
	c, _ := Detect("https://github.com/a/b", env(map[string]string{"GH_TOKEN": "t", "GITHUB_API_URL": srv.URL}))
	if created, err := c.CreateRelease("x@1.0.0", "x", "", false); err != nil || created {
		t.Errorf("existing release: created=%v err=%v", created, err)
	}
}

func TestGitLabCreateRelease(t *testing.T) {
	var gotPath, gotHeader, gotJob string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotHeader, gotJob = r.URL.EscapedPath(), r.Header.Get("PRIVATE-TOKEN"), r.Header.Get("JOB-TOKEN")
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotBody)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	// personal token, project addressed by its path
	c, err := Detect("https://gitlab.example.com/group/sub/proj", env(map[string]string{"GITLAB_TOKEN": "pat", "CI_API_V4_URL": srv.URL + "/api/v4"}))
	if err != nil {
		t.Fatal(err)
	}
	if created, err := c.CreateRelease("orders@1.0.0", "orders 1.0.0", "notes", false); err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	if gotPath != "/api/v4/projects/group%2Fsub%2Fproj/releases" || gotHeader != "pat" || gotJob != "" {
		t.Errorf("request: %s %q %q", gotPath, gotHeader, gotJob)
	}
	if gotBody["tag_name"] != "orders@1.0.0" || gotBody["description"] != "notes" {
		t.Errorf("payload: %v", gotBody)
	}

	// inside GitLab CI: job token and numeric project id
	c, err = Detect("https://gitlab.example.com/group/proj", env(map[string]string{"CI_JOB_TOKEN": "job", "CI_API_V4_URL": srv.URL + "/api/v4", "CI_PROJECT_ID": "42"}))
	if err != nil {
		t.Fatal(err)
	}
	c.CreateRelease("a@1.0.0", "a", "", false)
	if gotPath != "/api/v4/projects/42/releases" || gotJob != "job" || gotHeader != "" {
		t.Errorf("CI request: %s job=%q private=%q", gotPath, gotJob, gotHeader)
	}
}

func TestGitLabExistingAndErrors(t *testing.T) {
	status := http.StatusConflict
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		io.WriteString(w, `{"message":"Release already exists"}`)
	}))
	defer srv.Close()
	c, _ := Detect("https://gitlab.com/g/p", env(map[string]string{"GITLAB_TOKEN": "t", "CI_API_V4_URL": srv.URL}))
	if created, err := c.CreateRelease("a@1.0.0", "a", "", false); err != nil || created {
		t.Errorf("409 must mean already exists: %v %v", created, err)
	}
	status = http.StatusForbidden
	if _, err := c.CreateRelease("a@1.0.0", "a", "", false); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("403 should be an error mentioning the status: %v", err)
	}
}

func TestDetectErrors(t *testing.T) {
	if _, err := Detect("https://github.com/a/b", env(nil)); err == nil {
		t.Error("missing GitHub token should fail")
	} else if _, ok := err.(ErrNoToken); !ok {
		t.Errorf("want ErrNoToken, got %T", err)
	}
	if _, err := Detect("https://gitlab.com/a/b", env(nil)); err == nil {
		t.Error("missing GitLab token should fail")
	} else if _, ok := err.(ErrNoToken); !ok {
		t.Errorf("want ErrNoToken, got %T", err)
	}
	if _, err := Detect("https://git.internal.example/a/b", env(nil)); err == nil || strings.Contains(err.Error(), "token") {
		t.Errorf("unknown host should ask for MONO_RELEASE_FORGE: %v", err)
	}
	if _, err := Detect("https://git.internal.example/a/b", env(map[string]string{"MONO_RELEASE_FORGE": "gitlab", "GITLAB_TOKEN": "t"})); err != nil {
		t.Errorf("explicit forge should work: %v", err)
	}
	if _, err := Detect("", env(nil)); err == nil {
		t.Error("empty URL should fail")
	}
	// GitHub Enterprise: the API lives under /api/v3 of the same host
	c, err := Detect("https://github.example.com/o/r", env(map[string]string{"GITHUB_TOKEN": "t"}))
	if err != nil || c.(*github).api != "https://github.example.com/api/v3" {
		t.Errorf("GHE api: %v %v", c, err)
	}
}
