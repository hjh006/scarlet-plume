//go:build windows

package endpointsecure

import (
	"golang.org/x/sys/windows"
	"testing"
)

// Memory-only ACL fixtures. No directory, policy, service or device is touched.
func TestExistingTreeOwnerAndDACLTrustBoundaries(t *testing.T) {
	for _, tc := range []struct {
		sddl    string
		trusted bool
	}{
		{"O:BAD:P(A;;FA;;;SY)(A;;FA;;;BA)", true},
		{"O:SYD:P(A;;FA;;;SY)(A;;FA;;;BA)", true},
		{"O:BUD:P(A;;FA;;;SY)(A;;FA;;;BA)", false},
		{"O:BAD:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FR;;;BU)", false},
		{"O:BAD:P(A;;FA;;;WD)", false},
		{"O:BAD:P", false},
	} {
		sd, err := windows.SecurityDescriptorFromString(tc.sddl)
		if err != nil {
			t.Fatal(err)
		}
		if trustedOwnerAndACL(sd) != tc.trusted {
			t.Fatal("existing owner/ACL trust boundary misclassified")
		}
	}
}
