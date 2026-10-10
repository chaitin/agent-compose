//go:build linux && cgo && boxlitecgo

package driver

/*
#include <stdlib.h>
#include "boxlite.h"
*/
import "C"

import (
	"unsafe"

	"github.com/chaitin/agent-compose/pkg/credentials"
)

// applyBoxliteSecrets adds one credential secret per binding to the box
// options.
//
// The binding follows the BoxLite v0.10.4 header:
//
//	void boxlite_options_add_secret(CBoxliteOptions *opts,
//	                                const char *name,
//	                                const char *value,
//	                                const char *placeholder,
//	                                const char *const *hosts,
//	                                int hosts_count);
//
// BoxLite has no per-secret "require verified TLS" flag; a hostname allowlist
// entry is enforced through TLS SNI / HTTP Host inspection instead. The engine
// still refuses any binding whose RequireTLS is false, so a caller cannot widen
// the mechanism below the engine's floor. The residual gap — a plaintext HTTP
// Host that matches an allowed hostname — is reported as a capability
// limitation rather than being described as TLS-required.
//
// The C strings are released after the call because boxlite_options_add_secret
// copies its arguments into the options value; the header documents no borrow
// and the options outlive any borrowed pointer, so ownership transfer is the
// only lifetime that makes the documented API usable. BoxLite is not runnable
// in this environment, so that copy is an assumption recorded in the pull
// request, not a verified behavior.
func applyBoxliteSecrets(options *C.CBoxliteOptions, specs []credentials.SecretSpec) error {
	bindings, err := sandboxSecretBindings(specs)
	if err != nil {
		return err
	}
	if options == nil || len(bindings) == 0 {
		return nil
	}
	for _, binding := range bindings {
		addBoxliteSecret(options, binding)
	}
	return nil
}

func addBoxliteSecret(options *C.CBoxliteOptions, binding sandboxSecretBinding) {
	name := C.CString(binding.EnvVar)
	value := C.CString(binding.Value)
	placeholder := C.CString(binding.Placeholder)
	hosts := make([]*C.char, 0, len(binding.AllowHosts))
	for _, host := range binding.AllowHosts {
		hosts = append(hosts, C.CString(host))
	}
	var hostsPointer **C.char
	if len(hosts) > 0 {
		hostsPointer = (**C.char)(unsafe.Pointer(&hosts[0]))
	}
	C.boxlite_options_add_secret(options, name, value, placeholder, hostsPointer, C.int(len(hosts)))
	C.free(unsafe.Pointer(name))
	C.free(unsafe.Pointer(value))
	C.free(unsafe.Pointer(placeholder))
	for _, host := range hosts {
		C.free(unsafe.Pointer(host))
	}
}
