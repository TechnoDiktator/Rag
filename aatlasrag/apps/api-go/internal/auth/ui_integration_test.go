package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"atlasrag/api-go/internal/web"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Opt in with an isolated Chrome profile and temporary PostgreSQL tables.
func TestProjectUIWithPostgres(t *testing.T) {
	databaseURL, browser := os.Getenv("ATLASRAG_TEST_DATABASE_URL"), os.Getenv("ATLASRAG_TEST_BROWSER")
	if databaseURL == "" || browser == "" {
		t.Skip("set ATLASRAG_TEST_DATABASE_URL and ATLASRAG_TEST_BROWSER")
	}
	ctx := context.Background()
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	_, err = pool.Exec(ctx, `
CREATE TEMP TABLE users (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), email text UNIQUE NOT NULL, password_hash text NOT NULL);
CREATE TEMP TABLE projects (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), name text NOT NULL, created_by uuid NOT NULL, created_at timestamptz NOT NULL DEFAULT now());
CREATE TEMP TABLE project_members (project_id uuid NOT NULL, user_id uuid NOT NULL, UNIQUE (project_id, user_id));
CREATE TEMP TABLE documents (id uuid, project_id uuid, filename text, status text, created_at timestamptz);
`)
	if err != nil {
		t.Fatal(err)
	}
	service := testService(t)
	service.db = pool
	frontend := web.Handler()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/auth/register", service.Register)
	mux.HandleFunc("POST /api/v1/auth/login", service.Login)
	mux.Handle("GET /api/v1/projects", service.Authenticate(http.HandlerFunc(service.ListProjects)))
	mux.Handle("POST /api/v1/projects", service.Authenticate(http.HandlerFunc(service.CreateProject)))
	mux.Handle("GET /api/v1/projects/{projectID}/documents", service.ProjectAccess(http.HandlerFunc(service.ListDocuments), false))
	mux.Handle("POST /api/v1/projects/{projectID}/members", service.ProjectAccess(http.HandlerFunc(service.AddMember), false))
	mux.Handle("GET /app.js", frontend)
	mux.Handle("GET /app.css", frontend)
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		response := httptest.NewRecorder()
		frontend.ServeHTTP(response, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(strings.Replace(response.Body.String(), "</body>", uiTestScript+"</body>", 1)))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	runCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	windowSize := os.Getenv("ATLASRAG_TEST_WINDOW_SIZE")
	if windowSize == "" {
		windowSize = "1280,900"
	}
	cmd := exec.CommandContext(runCtx, browser, "--headless=new", "--disable-gpu", "--disable-extensions",
		"--user-data-dir="+t.TempDir(), "--window-size="+windowSize, "--virtual-time-budget=120000", "--dump-dom", server.URL)
	output, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(output), `data-ui-test="PASS"`) {
		result := string(output)
		if index := strings.Index(result, "data-ui-test="); index >= 0 {
			result = result[index:min(index+200, len(result))]
		}
		t.Fatalf("browser flow failed: %v\n%s", err, result)
	}
}

const uiTestScript = `<script>
window.addEventListener("load", async () => {
  const wait = async (condition, label) => {
    for (let i = 0; i < 10000; i++) {
      if (condition()) return;
      await new Promise(resolve => setTimeout(resolve, 10));
    }
    throw new Error("Timeout: " + label);
  };
  const setCredentials = (email, password) => {
    document.querySelector("#auth-email").value = email;
    document.querySelector("#auth-password").value = password;
  };
  const submitAuth = action => {
    document.querySelector("#account-message").textContent = "";
    document.querySelector("#auth-form").requestSubmit(
      document.querySelector('#auth-form button[value="' + action + '"]'));
  };
  const text = () => document.querySelector("#account-message").textContent;
  try {
    if (!document.querySelector("#sidebar-projects").hidden) throw new Error("projects visible before login");
    document.querySelector("#account-button").click();
    if (!document.querySelector("#workspace-dialog").open) throw new Error("account dialog did not open");
    setCredentials("ui-b@example.com", "testing-password-123");
    submitAuth("register");
    await wait(() => text().includes("Account created"), "register B");
    setCredentials("ui-a@example.com", "testing-password-123");
    submitAuth("register");
    await wait(() => text().includes("Account created"), "register A");
    setCredentials("ui-a@example.com", "wrong-password-123");
    submitAuth("login");
    await wait(() => text().includes("invalid email or password"), "invalid login");
    setCredentials("ui-a@example.com", "testing-password-123");
    submitAuth("login");
    await wait(() => !document.querySelector("#sidebar-projects").hidden, "login A");
    if (document.querySelector("#workspace-dialog").open) throw new Error("account dialog did not close");
    const sidebar = document.querySelector("#sidebar-projects").getBoundingClientRect();
    if (sidebar.width === 0 || sidebar.left < 0 || sidebar.right > innerWidth) throw new Error("sidebar projects not visible");
    const create = document.querySelector("#create-project-button").getBoundingClientRect();
    if (create.width === 0 || create.left < 0 || create.right > innerWidth) throw new Error("create button not visible");
    document.querySelector("#create-project-button").click();
    const dialog = document.querySelector("#workspace-dialog").getBoundingClientRect();
    if (dialog.left < 0 || dialog.right > innerWidth) throw new Error("dialog overflows viewport");
    document.querySelector("#project-name").value = "UI test project";
    document.querySelector("#project-form").requestSubmit();
    await wait(() => text() === "Project selected.", "project creation");
    const project = [...document.querySelectorAll("#project-list button")].find(b => b.textContent === "UI test project");
    if (!project || project.getAttribute("aria-pressed") !== "true") throw new Error("project not selected in sidebar");
    if (document.querySelector("#active-project-label").textContent !== "UI test project") throw new Error("topbar project missing");
    document.querySelector("#create-project-button").click();
    document.querySelector("#project-name").value = "Second project";
    document.querySelector("#project-form").requestSubmit();
    await wait(() => document.querySelector("#active-project-label").textContent === "Second project", "second project");
    document.querySelector("#search-document").value = "old-document";
    document.querySelector("#search-query").value = "old query";
    const first = [...document.querySelectorAll("#project-list button")].find(b => b.textContent === "UI test project");
    first.click();
    await wait(() => document.querySelector("#active-project-label").textContent === "UI test project", "switch project");
    if (document.querySelector("#search-document").value || document.querySelector("#search-query").value)
      throw new Error("project switch did not clear previous search");
    document.querySelector("#project-settings-button").click();
    document.querySelector("#member-email").value = "ui-b@example.com";
    document.querySelector("#member-form").requestSubmit();
    await wait(() => text().includes("Member added"), "add member");
    document.querySelector("#close-dialog").click();
    document.querySelector("#account-button").click();
    document.querySelector("#logout-button").click();
    if (!document.querySelector("#sidebar-projects").hidden || document.querySelector("#project-list").children.length)
      throw new Error("logout did not clear sidebar projects");
    document.querySelector("#account-button").click();
    setCredentials("ui-b@example.com", "testing-password-123");
    submitAuth("login");
    await wait(() => !document.querySelector("#sidebar-projects").hidden, "login B");
    const shared = [...document.querySelectorAll("#project-list button")].find(b => b.textContent === "UI test project");
    if (!shared)
      throw new Error("shared project missing");
    shared.click();
    await wait(() => document.querySelector("#active-project-label").textContent === "UI test project", "select shared project");
    document.body.dataset.uiTest = "PASS";
  } catch (error) {
    document.body.dataset.uiTest = "FAIL: " + error.message;
  }
});
</script>`
