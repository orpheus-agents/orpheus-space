package browserauth

import (
	"testing"

	"github.com/crewjam/saml"
)

func TestAssertionEmail(t *testing.T) {
	for _, tc := range []struct {
		name, attribute, friendly, format, subject string
		values                                     []string
		want                                       string
	}{
		{name: "missing"},
		{name: "email", attribute: "email", values: []string{" ALICE@Example.com "}, want: "alice@example.com"},
		{name: "friendly name", attribute: "urn:oid:1.2.840.113549.1.9.1", friendly: "email", values: []string{"alice@example.com"}, want: "alice@example.com"},
		{name: "invalid", attribute: "email", values: []string{"not-an-email"}},
		{name: "display address", attribute: "email", values: []string{"Alice <alice@example.com>"}},
		{name: "empty attribute", attribute: "email"},
		{name: "next valid value", attribute: "email", values: []string{"", "alice@example.com"}, want: "alice@example.com"},
		{name: "username is not email", attribute: "preferred_username", values: []string{"alice@example.com"}},
		{name: "untyped subject is not email", subject: "alice@example.com"},
		{name: "email NameID", format: "urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress", subject: " ALICE@example.com ", want: "alice@example.com"},
		{name: "invalid NameID", format: "urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress", subject: "alice"},
		{name: "attribute takes precedence", attribute: "email", values: []string{"alice@example.com"}, format: "urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress", subject: "other@example.com", want: "alice@example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attribute := saml.Attribute{Name: tc.attribute, FriendlyName: tc.friendly}
			for _, value := range tc.values {
				attribute.Values = append(attribute.Values, saml.AttributeValue{Value: value})
			}
			assertion := &saml.Assertion{
				Subject:             &saml.Subject{NameID: &saml.NameID{Format: tc.format, Value: tc.subject}},
				AttributeStatements: []saml.AttributeStatement{{Attributes: []saml.Attribute{attribute}}},
			}
			got := assertionEmail(assertion)
			if tc.want == "" {
				if got != nil {
					t.Fatalf("wanted absent email, got %q", *got)
				}
			} else if got == nil || *got != tc.want {
				t.Fatalf("wanted %q, got %v", tc.want, got)
			}
		})
	}
}
