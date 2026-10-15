package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// File is the saved CLI profile. The API key is a secret; the file mode is 0600.
type File struct {
	BaseURL string  `json:"baseUrl"`
	APIKey  string  `json:"apiKey"`
	Org     Org     `json:"org"`
	Project Project `json:"project"`
}

type Org struct {
	ID   string `json:"id"`
	Slug string `json:"slug,omitempty"`
	Name string `json:"name,omitempty"`
}

type Project struct {
	ID   string `json:"id,omitempty"`
	Slug string `json:"slug,omitempty"`
	Name string `json:"name,omitempty"`
}

func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "yggy", "config.json"), nil
}

func Load(path string) (File, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return File{}, nil
	}
	if err != nil {
		return File{}, err
	}
	var file File
	if err := json.Unmarshal(data, &file); err != nil {
		return File{}, err
	}
	return file, nil
}

func Save(path string, file File) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}

// SetOrg stores the org and clears the project. A project belongs to one org.
func (f *File) SetOrg(id, slug, name string) {
	f.Org = Org{ID: id, Slug: slug, Name: name}
	f.Project = Project{}
}

func (f *File) SetProject(id, slug, name string) {
	f.Project = Project{ID: id, Slug: slug, Name: name}
}

// Header is the value for x-yggy-organization-id. Slug and id are both accepted.
func (o Org) Header() string {
	if o.Slug != "" {
		return o.Slug
	}
	return o.ID
}

func (o Org) Label() string {
	switch {
	case o.Slug != "" && o.Name != "":
		return o.Slug + " (" + o.Name + ")"
	case o.Slug != "":
		return o.Slug
	case o.Name != "":
		return o.Name
	default:
		return o.ID
	}
}

func (p Project) Label() string {
	switch {
	case p.Slug != "" && p.Name != "":
		return p.Slug + " (" + p.Name + ")"
	case p.Slug != "":
		return p.Slug
	case p.Name != "":
		return p.Name
	default:
		return p.ID
	}
}

func (p Project) Empty() bool {
	return p.ID == "" && p.Slug == "" && p.Name == ""
}
