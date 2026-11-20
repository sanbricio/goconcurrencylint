package mutex

import "sync"

// A lock named through a local variable that stands for it (`f := s.file`,
// `mu := &s.box.mu`) is the same lock: dropping and retaking it inside a loop
// through the variable leaves the loop holding what it entered with.

type aliasedFile struct {
	sync.Mutex
	id int
}

type aliasedBox struct {
	mu sync.RWMutex
}

type aliasedSlice struct {
	file *aliasedFile
	box  *aliasedBox
	id   int
}

func aliasedFetch(id *int) int { *id = 1; return 0 }

// GoodRelockThroughEmbeddedAlias must be CLEAN.
func (s *aliasedSlice) GoodRelockThroughEmbeddedAlias(retry bool) {
	f := s.file
	f.Lock()
	for s.id == 0 {
		var id int
		f.Unlock()
		st := aliasedFetch(&id)
		f.Lock()
		if st != 0 {
			break
		}
		if !retry || st == 0 {
			s.id = id
			break
		}
		f.Unlock()
		f.Lock()
	}
	f.Unlock()
}

// GoodRelockThroughPointerAlias must be CLEAN.
func (s *aliasedSlice) GoodRelockThroughPointerAlias() {
	mu := &s.box.mu
	mu.Lock()
	for s.id == 0 {
		mu.Unlock()
		st := aliasedFetch(&s.id)
		mu.Lock()
		if st != 0 {
			break
		}
	}
	mu.Unlock()
}

// BadBreakCarriesAliasedLock must be flagged: the loop is entered without
// the lock and the break leaves holding the one taken in the iteration.
func (s *aliasedSlice) BadBreakCarriesAliasedLock() {
	f := s.file
	for s.id == 0 {
		f.Lock() // want "mutex 's.file' is locked but not unlocked"
		if aliasedFetch(&s.id) != 0 {
			break
		}
		f.Unlock()
	}
}
