//go:build js && wasm

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"syscall/js"
	"time"

	"github.com/helius-labs/spr-ceremony/internal/browserproto"
	"github.com/helius-labs/spr-ceremony/internal/contribution"
)

func main() {
	defer func() {
		if p := recover(); p != nil {
			js.Global().Call("sprFailure", fmt.Sprintf("contribution engine failed: %v", p))
		}
	}()
	if err := run(); err != nil {
		js.Global().Call("sprFailure", err.Error())
	}
}

func run() error {
	// Go's js/wasm crypto/rand uses the browser's crypto.getRandomValues.
	// Never accept a seed, entropy bytes, or a randomness substitute from the UI.
	if !js.Global().Get("isSecureContext").Bool() || js.Global().Get("crypto").Get("getRandomValues").Type() != js.TypeFunction {
		return fmt.Errorf("a secure context and browser cryptographic randomness are required")
	}
	text := js.Global().Get("sprFixture").String()
	if len(text) > 64<<10 {
		return fmt.Errorf("fixture metadata exceeds limit")
	}
	var fixture browserproto.Fixture
	d := json.NewDecoder(bytes.NewBufferString(text))
	d.DisallowUnknownFields()
	if err := d.Decode(&fixture); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("trailing fixture data")
	}
	if err := fixture.Validate(); err != nil {
		return err
	}
	source := js.Global().Get("sprInput")
	if int64(source.Get("byteLength").Int()) != fixture.Input.Size {
		return fmt.Errorf("input size mismatch")
	}
	input := make([]byte, fixture.Input.Size)
	if js.CopyBytesToGo(input, source) != len(input) {
		return fmt.Errorf("incomplete input copy")
	}
	js.Global().Set("sprInput", js.Null())
	start := time.Now()
	sha := sha256.Sum256(input)
	if hex.EncodeToString(sha[:]) != fixture.Input.SHA256 {
		return fmt.Errorf("input SHA256 mismatch")
	}
	stages := map[string]float64{}
	last, lastAt := "authenticate", start
	progress := func(name string) {
		now := time.Now()
		stages[last] = now.Sub(lastAt).Seconds()
		last, lastAt = name, now
		var memory runtime.MemStats
		runtime.ReadMemStats(&memory)
		b, _ := json.Marshal(map[string]any{"stage": name, "elapsed_seconds": now.Sub(start).Seconds(), "go_heap_alloc_bytes": memory.HeapAlloc, "go_heap_sys_bytes": memory.HeapSys})
		js.Global().Call("sprStage", string(b))
	}
	var output bytes.Buffer
	output.Grow(len(input) + 32)
	if err := contribution.Contribute(bytes.NewReader(input), &output, fixture.Shape, progress); err != nil {
		return err
	}
	if int64(output.Len()) > fixture.Input.Size+32 {
		return fmt.Errorf("unexpected contribution size")
	}
	progress("complete")
	result := js.Global().Get("Uint8Array").New(output.Len())
	if js.CopyBytesToJS(result, output.Bytes()) != output.Len() {
		return fmt.Errorf("incomplete output copy")
	}
	sha = sha256.Sum256(output.Bytes())
	fixtureSHA := sha256.Sum256([]byte(text))
	b, _ := json.Marshal(map[string]any{
		"engine": browserproto.Engine, "circuit": fixture.Circuit, "insecure_test_setup": true,
		"fixture_sha256": hex.EncodeToString(fixtureSHA[:]), "input": fixture.Input,
		"output":                    browserproto.Artifact{SHA256: hex.EncodeToString(sha[:]), Size: int64(output.Len())},
		"local_verification_passed": true, "stage_seconds": stages, "engine_seconds": time.Since(start).Seconds(),
	})
	js.Global().Call("sprSuccess", string(b), result)
	return nil
}
