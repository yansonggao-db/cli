package mutator

import (
	"context"
	"fmt"

	"github.com/databricks/cli/bundle"
	"github.com/databricks/cli/libs/diag"
	"github.com/databricks/cli/libs/dyn"
	"github.com/databricks/cli/libs/dyn/dynvar"
)

type validateSecretValueIsVariable struct{}

func ValidateSecretValueIsVariable() bundle.Mutator {
	return &validateSecretValueIsVariable{}
}

func (v *validateSecretValueIsVariable) Name() string {
	return "ValidateSecretValueIsVariable"
}

func (v *validateSecretValueIsVariable) Apply(ctx context.Context, b *bundle.Bundle) diag.Diagnostics {
	var diags diag.Diagnostics

	// Iterate over all secrets in the bundle
	for key := range b.Config.Resources.Secrets {
		p := dyn.NewPath(dyn.Key("resources"), dyn.Key("secrets"), dyn.Key(key), dyn.Key("value"))
		val, err := dyn.GetByPath(b.Config.Value(), p)
		if dyn.IsNoSuchKeyError(err) {
			continue
		}
		if err != nil {
			return diag.FromErr(err)
		}

		valueStr, ok := val.AsString()
		if !ok {
			continue
		}

		// Value must be a variable reference to prevent leaking secrets in config files
		if !dynvar.ContainsVariableReference(valueStr) {
			diags = append(diags, diag.Diagnostic{
				Severity: diag.Error,
				Summary:  "Secret value must be a variable reference",
				Detail: fmt.Sprintf(`The secret value for "%s" must be a variable reference (e.g., ${var.my_secret}).
Plain text secret values are not allowed to prevent leaking secrets in configuration files.
Use bundle variables to pass secret values at deployment time.`, key),
				Locations: val.Locations(),
				Paths:     []dyn.Path{p},
			})
		}
	}

	return diags
}
