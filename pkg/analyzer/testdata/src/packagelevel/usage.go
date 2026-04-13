package packagelevel

func BadPackageLevelMutex() {
	packageMu.Lock() // want "mutex 'packageMu' is locked but not unlocked"
}

func BadPackageLevelWaitGroup() {
	packageWG.Add(1) // want "waitgroup 'packageWG' has Add without corresponding Done"
	packageWG.Wait()
}
