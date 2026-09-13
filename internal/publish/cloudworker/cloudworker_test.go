package cloudworker

import (
	"encoding/json"
	"strings"
	"testing"
)

// The embedded file is the only thing `orch cloud setup` can deploy. An empty
// file, or one that stopped serving the contract (an orch-cloud refactor
// copied in without looking), would deploy a Worker every later `orch cloud
// login` and `orch publish --to cloud` fails against. The markers are the
// contract's routes and the one header the viewer must send, as they appear
// in the bundled source.
func TestScriptCarriesTheContractRoutes(t *testing.T) {
	if len(Script) < 4096 {
		t.Fatalf("embedded worker.js is %d bytes — not a built orch-cloud Worker", len(Script))
	}
	src := string(Script)
	for _, marker := range []string{
		`"/api/v1/"`,        // the API prefix
		`"health"`,          // GET /api/v1/health
		`"whoami"`,          // GET /api/v1/whoami — what setup polls
		`"projects"`,        // POST /api/v1/projects
		`"site"`,            // PUT …/site
		`"view-token"`,      // POST …/view-token
		`"/v/"`,             // the viewer
		`ADMIN_TOKEN`,       // the secret setup stores
		`"Referrer-Policy"`, // the token is in the viewer URL
	} {
		if !strings.Contains(src, marker) {
			t.Errorf("embedded worker.js does not contain %s", marker)
		}
	}
}

func TestConfigHasNoKVNamespaceID(t *testing.T) {
	raw, err := Config("orch-cloud")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Name              string              `json:"name"`
		Main              string              `json:"main"`
		CompatibilityDate string              `json:"compatibility_date"`
		KV                []map[string]string `json:"kv_namespaces"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("wrangler.jsonc is not valid JSON: %v\n%s", err, raw)
	}
	if cfg.Name != "orch-cloud" || cfg.Main != "worker.js" || cfg.CompatibilityDate != CompatibilityDate {
		t.Errorf("config = %+v", cfg)
	}
	// An id would pin every operator's deploy to one account's namespace;
	// without it wrangler provisions one on the first deploy.
	if len(cfg.KV) != 1 || cfg.KV[0]["binding"] != "ORCH" || cfg.KV[0]["id"] != "" {
		t.Errorf("kv_namespaces = %v, want exactly one ORCH binding with no id", cfg.KV)
	}
}

func TestValidNameRejectsWhatWorkersDevCannotHost(t *testing.T) {
	for _, ok := range []string{"orch-cloud", "a", "acme-2"} {
		if err := ValidName(ok); err != nil {
			t.Errorf("ValidName(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Orch", "-lead", "a_b", "a.b", strings.Repeat("x", 64)} {
		if err := ValidName(bad); err == nil {
			t.Errorf("ValidName(%q) accepted", bad)
		}
	}
	if _, err := Config("Bad Name"); err == nil {
		t.Error("Config accepted an invalid name")
	}
}
