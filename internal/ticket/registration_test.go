package ticket

import (
	"encoding/json"
	"testing"
)

func TestRegistrationMeetsHostMetadataRequirements(t *testing.T) {
	raw, err := json.Marshal(registration())
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		SchemaVersion uint32 `json:"schema_version"`
		Metadata      struct{ Name, Version, Author, GitHubRepository string }
		Capabilities  map[string]bool
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	if r.SchemaVersion != 6 || r.Metadata.Name == "" || r.Metadata.Version == "" || r.Metadata.Author == "" || r.Metadata.GitHubRepository == "" {
		t.Fatal("CPA would reject incomplete metadata")
	}
	for _, c := range []string{"request_interceptor", "request_lifecycle_plugin", "response_interceptor", "response_stream_interceptor", "management_api"} {
		if !r.Capabilities[c] {
			t.Fatal("missing capability", c)
		}
	}
}
