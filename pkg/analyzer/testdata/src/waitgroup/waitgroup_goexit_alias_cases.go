package waitgroup

import (
	rt "runtime"
	"sync"
)

func BadDoneAfterAliasedRuntimeGoexit() {
	var wg sync.WaitGroup
	shouldExit := true
	wg.Add(1)
	go func() {
		if shouldExit {
			rt.Goexit()
		}
		wg.Done() // want "waitgroup 'wg' Done should be deferred so it runs on panic or runtime.Goexit"
	}()
	wg.Wait()
}
