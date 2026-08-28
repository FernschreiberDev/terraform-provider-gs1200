package provider

import (
	"context"
	"fmt"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/attr"

	"github.com/hashicorp/terraform-plugin-framework/diag"

	"github.com/FernschreiberDev/terraform-provider-gs1200/internal/zyxel"
)

// diagList is the framework's diagnostics collection under a shorter name;
// it is threaded through nearly every function here.
type diagList = diag.Diagnostics

// stateSetter is what Create, Update and Read responses have in common, so the
// write path can be shared between them.
type stateSetter interface {
	Set(ctx context.Context, val any) diag.Diagnostics
}

// clientFrom unwraps the client the provider handed down. ProviderData is nil
// during the framework's early validation passes, which is not an error.
func clientFrom(providerData any, diags *diagList) *zyxel.Client {
	if providerData == nil {
		return nil
	}
	client, ok := providerData.(*zyxel.Client)
	if !ok {
		diags.AddError(
			"Unexpected provider data",
			fmt.Sprintf("Expected a *zyxel.Client, got %T. This is a bug in the provider.",
				providerData),
		)
		return nil
	}
	return client
}

// attrType and attrValue keep the data source's object plumbing readable.
type (
	attrType  = attr.Type
	attrValue = attr.Value
)

func itoa(value int) string { return strconv.Itoa(value) }
