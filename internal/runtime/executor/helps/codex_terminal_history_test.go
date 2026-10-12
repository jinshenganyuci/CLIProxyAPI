package helps

import (
	"strconv"
	"strings"
	"testing"
)

func TestCodexTerminalHistoryBoundsRecentUniqueResponses(t *testing.T) {
	var history CodexTerminalHistory
	history.Add("")
	if len(history.ids) != 0 {
		t.Fatal("empty response ID consumed history")
	}
	for i := 0; i < 4096; i++ {
		id := "response-" + strconv.Itoa(i)
		history.Add(id)
		history.Add(id)
		if !history.Contains(id) || len(history.ids) > CodexTerminalHistoryCapacity {
			t.Fatalf("history lost latest response or exceeded capacity at %d", i)
		}
	}
	if history.Contains("response-0") || history.Contains("response-3967") || !history.Contains("response-3968") {
		t.Fatal("history does not retain exactly the most recent unique responses")
	}
	largeID := strings.Repeat("response-untrusted-", 65536)
	history.Add(largeID)
	if !history.Contains(largeID) || len(history.ids) != CodexTerminalHistoryCapacity {
		t.Fatal("large IDs must use the same bounded digest storage")
	}
	history.Reset()
	if history.Contains(largeID) || len(history.ids) != 0 {
		t.Fatal("new socket retained old terminal IDs")
	}
}
