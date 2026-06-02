package mutex

import (
	"bytes"
	"go/ast"
	"go/printer"
	"go/token"
)

func exprString(expr ast.Expr) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, token.NewFileSet(), expr); err != nil {
		return ""
	}
	return buf.String()
}

func isLockMethod(methodName string) bool {
	return methodName == "Lock" || methodName == "RLock"
}

func isUnlockMethod(methodName string) bool {
	return methodName == "Unlock" || methodName == "RUnlock"
}

func matchingUnlockMethod(methodName string) string {
	switch methodName {
	case "Lock":
		return "Unlock"
	case "RLock":
		return "RUnlock"
	default:
		return ""
	}
}
