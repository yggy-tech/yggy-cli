package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoadRoundTripAndClearsProjectOnOrgChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "yggy", "config.json")
	file := File{BaseURL: "https://api.yggy.tech", APIKey: "ygg_secret"}
	file.SetOrg("org-1", "acme-corp", "Acme")
	file.SetProject("proj-1", "diligence", "Diligence")
	if err := Save(path, file); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 0600", info.Mode().Perm())
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Project.Slug != "diligence" {
		t.Fatalf("project = %#v", loaded.Project)
	}

	loaded.SetOrg("org-2", "other", "Other")
	if !loaded.Project.Empty() {
		t.Fatalf("project = %#v, want empty after org change", loaded.Project)
	}
	if loaded.Org.Header() != "other" {
		t.Fatalf("header = %q", loaded.Org.Header())
	}
}

func TestLoadMissingFile(t *testing.T) {
	file, err := Load(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if file.APIKey != "" {
		t.Fatalf("api key = %q", file.APIKey)
	}
}
