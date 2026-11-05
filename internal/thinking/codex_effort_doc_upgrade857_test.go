package thinking

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestCodexEffortDocUpgrade857(t *testing.T) {
	if got := ExtractCodexReasoningEffortDoc(nil); got != "" {
		t.Fatalf("nil document effort = %q", got)
	}
	t.Run("pending document mutation", func(t *testing.T) {
		doc := ojson.NewDocString(`{"reasoning":{"effort":"medium"}}`)
		if err := doc.Set("reasoning.effort", "high"); err != nil {
			t.Fatal(err)
		}
		if got := ExtractCodexReasoningEffortDoc(doc); got != "high" {
			t.Fatalf("effective effort after mutation = %q, want high", got)
		}
	})
	t.Run("configuration update takes precedence", func(t *testing.T) {
		doc := ojson.NewDocString(`{"reasoning":{"effort":"medium"},"input":[{"type":"configuration_update","reasoning":{"effort":"high"}}]}`)
		if got := ExtractCodexReasoningEffortDoc(doc); got != "high" {
			t.Fatalf("effective configuration-update effort = %q, want high", got)
		}
		if err := doc.Set("input.0.reasoning.effort", "low"); err != nil {
			t.Fatal(err)
		}
		if got := ExtractCodexReasoningEffortDoc(doc); got != "low" {
			t.Fatalf("updated configuration-update effort = %q, want low", got)
		}
	})
}
