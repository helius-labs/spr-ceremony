//go:build js && wasm

package main

import (
	"github.com/helius-labs/spr-ceremony/internal/signing"
	"syscall/js"
)

func main() {
	verify := js.FuncOf(func(_ js.Value, args []js.Value) (result any) {
		defer func() {
			if recover() != nil {
				result = false
			}
		}()
		if len(args) != 4 {
			return false
		}
		return signing.VerifyMessage([]byte(args[0].String()), args[1].String(), args[2].String(), []byte(args[3].String())) == nil
	})
	js.Global().Set("sprVerifySignature", verify)
	js.Global().Call("sprSignaturesReady")
	select {}
}
