package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/GT-Drehmal/avSYS/internal/store"
)

// activityKinds are the actions shown as what was done to a server; those
// with files only to whoever can open them.
var (
	activityKinds = []string{"server.", "content.", "snapshot.", "console."}
	fileKinds     = []string{"files.", "terminal."}
)

func isActivity(action string) bool {
	for _, k := range append(activityKinds, fileKinds...) {
		if strings.HasPrefix(action, k) {
			return true
		}
	}
	return false
}

// activityView is something done to a server, and who did it.
type activityView struct {
	At     time.Time    `json:"at"`
	Action string       `json:"action"`
	Detail string       `json:"detail,omitempty"`
	User   *presentUser `json:"user,omitempty"`
}

// serverActivity is what was last done to a server, newest first, since it
// was made: one deleted before it with the same name is not it.
func (s *Server) serverActivity(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, mc, err := s.server(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	kinds := activityKinds
	if a.Role.AtLeast(store.RoleOperator) {
		kinds = append(append([]string(nil), activityKinds...), fileKinds...)
	}
	// Its making is noted just after it is made; the server of its name
	// before it, if any, is told apart by its deletion.
	list, err := s.Store.ServerActivity(r.Context(), a.Org.ID, mc.Name, mc.CreationTimestamp.Time, kinds, 50)
	if err != nil {
		return err
	}
	out := make([]activityView, len(list))
	for i, e := range list {
		out[i] = activityView{At: e.At, Action: e.Action, Detail: e.Detail}
		if e.User != nil {
			who := userOf(e.User)
			out[i].User = &who
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}
