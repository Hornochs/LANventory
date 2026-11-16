package web

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/lan-dot-party/lanventory/internal/store"
)

const (
	// importTTL is how long an upload stays available after its last use.
	importTTL = 30 * time.Minute
	// importMaxStates bounds the memory held for uploads: one per user, at most this many users.
	importMaxStates = 16
	// importMaxFile is the largest accepted CSV file.
	importMaxFile = 5 << 20
	// importMaxBody is the request limit: the file plus the other form fields.
	importMaxBody = importMaxFile + 256<<10
)

// importForm holds the choices of the mapping step.
type importForm struct {
	Mapping         []string
	DefaultCategory int64
	DefaultLocation int64
	SkipInvalid     bool
	CreateLocations bool
	Update          bool
}

// importState is a parsed upload between the steps of the import wizard.
type importState struct {
	userID    int64
	sessionID string
	expires   time.Time
	Table     *store.ImportTable // CSV upload; immutable once stored
	// Data is a parsed data export (JSON upload, see handlers_data.go); a
	// state holds either Table or Data.
	Data *store.DataExport
	Form importForm
	// Previewed is set when a preview was shown for Form; only then can the
	// import be confirmed.
	Previewed bool
	// busy marks an import in progress (see Claim).
	busy bool
}

// importStates keeps uploads in memory, keyed by a random token that is
// bound to the user and the session. Nothing is written to disk; a restart
// drops them. Every user has at most one upload.
type importStates struct {
	mu sync.Mutex
	m  map[string]*importState
	// now is replaced in tests.
	now func() time.Time
}

func newImportStates() *importStates {
	return &importStates{m: map[string]*importState{}, now: time.Now}
}

// sweep removes expired states; the caller holds the lock.
func (s *importStates) sweep() {
	now := s.now()
	for k, v := range s.m {
		if now.After(v.expires) {
			delete(s.m, k)
		}
	}
}

// Put stores a new upload for the user (replacing the user's previous one)
// and returns its token. It fails when too many users hold uploads.
func (s *importStates) Put(userID int64, sessionID string, t *store.ImportTable) (string, bool) {
	return s.put(userID, sessionID, &importState{Table: t})
}

// PutData is Put for a data export upload.
func (s *importStates) PutData(userID int64, sessionID string, d *store.DataExport) (string, bool) {
	return s.put(userID, sessionID, &importState{Data: d})
}

func (s *importStates) put(userID int64, sessionID string, st *importState) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep()
	for k, v := range s.m {
		if v.userID == userID {
			delete(s.m, k)
		}
	}
	if len(s.m) >= importMaxStates {
		return "", false
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand: " + err.Error())
	}
	token := hex.EncodeToString(b)
	st.userID, st.sessionID, st.expires = userID, sessionID, s.now().Add(importTTL)
	s.m[token] = st
	return token, true
}

// Get returns a copy of the state when the token exists, is not expired and
// belongs to the user and session; it extends the lifetime.
func (s *importStates) Get(token string, userID int64, sessionID string) (importState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.lookup(token, userID, sessionID)
	if st == nil {
		return importState{}, false
	}
	st.expires = s.now().Add(importTTL)
	cp := *st
	cp.Form.Mapping = append([]string(nil), st.Form.Mapping...)
	return cp, true
}

func (s *importStates) lookup(token string, userID int64, sessionID string) *importState {
	st, ok := s.m[token]
	if !ok || s.now().After(st.expires) || st.userID != userID || st.sessionID != sessionID {
		return nil
	}
	return st
}

// SetForm stores the mapping choices and whether they were previewed.
func (s *importStates) SetForm(token string, userID int64, sessionID string, f importForm, previewed bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.lookup(token, userID, sessionID)
	if st == nil {
		return false
	}
	st.Form, st.Previewed = f, previewed
	return true
}

// Delete drops a state.
func (s *importStates) Delete(token string, userID int64, sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lookup(token, userID, sessionID) != nil {
		delete(s.m, token)
	}
}

// Claim marks the upload as being imported; it fails when it is gone or
// already claimed (a double click).
func (s *importStates) Claim(token string, userID int64, sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.lookup(token, userID, sessionID)
	if st == nil || st.busy {
		return false
	}
	st.busy = true
	return true
}

// Release ends a claim (the upload stays when the import did not happen).
func (s *importStates) Release(token string, userID int64, sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.lookup(token, userID, sessionID); st != nil {
		st.busy = false
	}
}
