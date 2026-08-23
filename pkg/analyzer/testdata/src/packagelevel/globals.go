package packagelevel

import "sync"

var packageMu sync.Mutex
var packageWG sync.WaitGroup
var poolWG sync.WaitGroup

// resetWG models shared state reset after Wait.
var resetWG = &sync.WaitGroup{}
