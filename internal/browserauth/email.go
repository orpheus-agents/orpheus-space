package browserauth

import (
	"github.com/crewjam/saml"
	"github.com/orpheus-agents/orpheus-space/internal/schedule"
)

// Call only after assertion signature and login-state validation.
// Username and display name are not email claims.
func assertionEmail(assertion *saml.Assertion) *string {
	for _, statement := range assertion.AttributeStatements {
		for _, attribute := range statement.Attributes {
			if attribute.Name != "email" && attribute.FriendlyName != "email" {
				continue
			}
			for _, value := range attribute.Values {
				if email, err := schedule.Email(value.Value); err == nil {
					return new(email)
				}
			}
		}
	}
	if assertion.Subject != nil && assertion.Subject.NameID != nil {
		name := assertion.Subject.NameID
		if name.Format == "urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress" {
			if email, err := schedule.Email(name.Value); err == nil {
				return new(email)
			}
		}
	}
	return nil
}
