package packagelevel

import "sync"

var packageMu sync.Mutex
var packageWG sync.WaitGroup
var poolWG sync.WaitGroup
