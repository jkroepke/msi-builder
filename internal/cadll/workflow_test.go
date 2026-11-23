package cadll_test

import (
	"fmt"
	"slices"
	"testing"

	"go.yaml.in/yaml/v4"
)

// jobNeeds is a job's needs, written as one name or a list.
type jobNeeds []string

func (n *jobNeeds) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		*n = jobNeeds{node.Value}

		return nil
	}

	var list []string

	if err := node.Decode(&list); err != nil {
		return fmt.Errorf("needs: %w", err)
	}

	*n = list

	return nil
}

// TestReleaseNeedsEveryCheck checks that the release and publish jobs of CI
// wait for every check the required fail job covers, in particular the
// rebuild of these DLLs: otherwise a stale committed DLL could be tagged or
// published while that check fails (REVIEW1 #11).
func TestReleaseNeedsEveryCheck(t *testing.T) {
	t.Parallel()

	var workflow struct {
		Jobs map[string]struct {
			Needs jobNeeds `yaml:"needs"`
		} `yaml:"jobs"`
	}

	if err := yaml.Unmarshal([]byte(readFile(t, "../../.github/workflows/ci.yaml")), &workflow); err != nil {
		t.Fatal(err)
	}

	checks := workflow.Jobs["fail"].Needs
	if !slices.Contains(checks, "ca-dll") {
		t.Fatalf("the fail job does not need ca-dll: %v", checks)
	}

	for _, job := range []string{"release", "publish"} {
		needs := workflow.Jobs[job].Needs

		for _, check := range checks {
			// detect-changes only decides which checks run; they need it.
			if check != "detect-changes" && !slices.Contains(needs, check) {
				t.Errorf("job %s does not need %s: %v", job, check, needs)
			}
		}
	}
}
