// Package gen holds the code and the descriptor set that buf builds from
// proto/. Only this file is written by hand; `make proto` writes the rest.
package gen

import _ "embed"

// DescriptorSet is the FileDescriptorSet of proto/loop/v1, imports included.
//
//go:embed loop.binpb
var DescriptorSet []byte
