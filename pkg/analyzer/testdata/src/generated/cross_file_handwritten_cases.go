package generated

// Handwritten callers of helpers in cross_file_generated_helpers.go.
// Must produce no diagnostics: proves the skip doesn't break cross-file
// symbol resolution.

func GoodHandwrittenCallsGeneratedDoneHelper() {
	crossFileWG.Add(1)
	go GeneratedSignalDone()
	crossFileWG.Wait()
}

func (w *CrossFileWrapper) Lock() {
	w.mu.Lock()
}
