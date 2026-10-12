package helps

import "crypto/sha256"

// CodexTerminalHistoryCapacity bounds completed-response bookkeeping per socket.
const CodexTerminalHistoryCapacity = 128

// CodexTerminalHistory remembers recent terminal IDs without retaining arbitrary
// upstream strings. Callers serialize access with the socket lifecycle lock.
type CodexTerminalHistory struct {
	ids   map[[sha256.Size]byte]struct{}
	order [CodexTerminalHistoryCapacity][sha256.Size]byte
	next  int
}

func (h *CodexTerminalHistory) Add(id string) {
	if id == "" {
		return
	}
	key := sha256.Sum256([]byte(id))
	if _, exists := h.ids[key]; exists {
		return
	}
	if h.ids == nil {
		h.ids = make(map[[sha256.Size]byte]struct{}, CodexTerminalHistoryCapacity)
	}
	if len(h.ids) == CodexTerminalHistoryCapacity {
		delete(h.ids, h.order[h.next])
	}
	h.order[h.next] = key
	h.next = (h.next + 1) % CodexTerminalHistoryCapacity
	h.ids[key] = struct{}{}
}

func (h *CodexTerminalHistory) Contains(id string) bool {
	_, exists := h.ids[sha256.Sum256([]byte(id))]
	return exists
}

func (h *CodexTerminalHistory) Reset() {
	*h = CodexTerminalHistory{}
}
