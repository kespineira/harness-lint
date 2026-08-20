package codex

import (
	"bytes"
	"testing"
)

func TestParseSkillDocumentAcceptsRealYAMLFrontmatter(t *testing.T) {
	tests := []struct {
		name            string
		frontmatter     string
		wantDescription string
	}{
		{
			name:            "plain scalars",
			frontmatter:     "name: plain\ndescription: Plain description\n",
			wantDescription: "Plain description",
		},
		{
			name: "folded block scalar",
			frontmatter: "name: folded\n" +
				"description: >\n" +
				"  first line\n" +
				"  second line\n",
			wantDescription: "first line second line",
		},
		{
			name: "stripped folded block scalar",
			frontmatter: "name: folded-stripped\n" +
				"description: >-\n" +
				"  first line\n" +
				"  second line\n",
			wantDescription: "first line second line",
		},
		{
			name: "literal block scalar with continuation",
			frontmatter: "name: literal\n" +
				"description: |\n" +
				"    first line\n" +
				"    ---\n" +
				"    last line\n" +
				"metadata:\n" +
				"    owner:\n" +
				"      team: runtime\n",
			wantDescription: "first line\n---\nlast line",
		},
		{
			name: "sequence and nested mapping extras",
			frontmatter: "name: extras\n" +
				"description: Extra fields are tolerated\n" +
				"references:\n" +
				"  - workers\n" +
				"  - pages\n" +
				"metadata:\n" +
				"  author: harness\n" +
				"  links:\n" +
				"    docs: https://example.invalid/docs\n",
			wantDescription: "Extra fields are tolerated",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const body = "BODY_ONLY_SENTINEL\n"
			doc := parseSkillDocument([]byte("---\n" + test.frontmatter + "---\n" + body))
			if doc.malformed {
				t.Fatalf("valid YAML frontmatter marked malformed: %#v", doc)
			}
			if doc.name == "" {
				t.Fatal("name is empty")
			}
			if doc.description != test.wantDescription {
				t.Fatalf("description = %q, want %q", doc.description, test.wantDescription)
			}
			if got := string(doc.body); got != body {
				t.Fatalf("body = %q, want %q", got, body)
			}
			if bytes.Contains(doc.advertisedMetadata, []byte(body)) {
				t.Fatalf("advertised metadata contains body sentinel: %q", doc.advertisedMetadata)
			}
		})
	}
}

func TestParseSkillDocumentRejectsInvalidFrontmatter(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{
			name:    "missing opening delimiter",
			content: "name: valid\ndescription: valid\n---\nbody\n",
		},
		{
			name:    "missing closing delimiter",
			content: "---\nname: valid\ndescription: valid\nbody\n",
		},
		{
			name:    "invalid YAML",
			content: "---\nname: [unterminated\ndescription: valid\n---\nbody\n",
		},
		{
			name:    "duplicate root key",
			content: "---\nname: first\ndescription: valid\nname: second\n---\nbody\n",
		},
		{
			name:    "duplicate nested key",
			content: "---\nname: valid\ndescription: valid\nmetadata:\n  owner: first\n  owner: second\n---\nbody\n",
		},
		{
			name:    "root sequence",
			content: "---\n- name\n- description\n---\nbody\n",
		},
		{
			name:    "missing name",
			content: "---\ndescription: valid\n---\nbody\n",
		},
		{
			name:    "missing description",
			content: "---\nname: valid\n---\nbody\n",
		},
		{
			name:    "empty name",
			content: "---\nname: \ndescription: valid\n---\nbody\n",
		},
		{
			name:    "empty description",
			content: "---\nname: valid\ndescription: \n---\nbody\n",
		},
		{
			name:    "non-string name",
			content: "---\nname: 42\ndescription: valid\n---\nbody\n",
		},
		{
			name:    "non-string description",
			content: "---\nname: valid\ndescription:\n  - not\n  - a string\n---\nbody\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			doc := parseSkillDocument([]byte(test.content))
			if !doc.malformed {
				t.Fatalf("invalid frontmatter accepted: %#v", doc)
			}
			if len(doc.advertisedMetadata) != 0 {
				t.Fatalf("malformed frontmatter advertised metadata: %q", doc.advertisedMetadata)
			}
		})
	}
}

func TestParseSkillDocumentAdvertisedMetadataIsMetadataOnly(t *testing.T) {
	const body = "PRIVATE_BODY_SENTINEL\n"
	doc := parseSkillDocument([]byte("---\nname: privacy\ndescription: public description\n---\n" + body))
	if doc.malformed {
		t.Fatalf("valid frontmatter marked malformed: %#v", doc)
	}
	if got := string(doc.body); got != body {
		t.Fatalf("body = %q, want %q", got, body)
	}
	if got, want := string(doc.advertisedMetadata), "name: privacy\ndescription: public description\n"; got != want {
		t.Fatalf("advertised metadata = %q, want %q", got, want)
	}
	if bytes.Contains(doc.advertisedMetadata, []byte(body)) {
		t.Fatalf("advertised metadata contains private body: %q", doc.advertisedMetadata)
	}
}
