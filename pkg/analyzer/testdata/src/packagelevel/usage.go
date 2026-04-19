package packagelevel

import "sync"

func BadPackageLevelMutex() {
	packageMu.Lock() // want "mutex 'packageMu' is locked but not unlocked"
}

func BadPackageLevelWaitGroup() {
	packageWG.Add(1) // want "waitgroup 'packageWG' has Add without corresponding Done"
	packageWG.Wait()
}

func GoodPackageLevelWaitGroupDoneInHelper() {
	packageWG.Add(1)
	go packageLevelWorker()
	packageWG.Wait()
}

func packageLevelWorker() {
	defer packageWG.Done()
}

func BadShadowedPackageLevelWaitGroup() {
	var packageWG sync.WaitGroup

	packageWG.Add(1) // want "waitgroup 'packageWG' has Add without corresponding Done"
	go packageLevelWorker()
	packageWG.Wait()
}
