package provider

import tfpath "github.com/hashicorp/terraform-plugin-framework/path"

// path is a small shorthand: attribute paths are one-segment throughout this
// provider, and the import name collides with the standard library's.
func path(name string) tfpath.Path { return tfpath.Root(name) }
