package tlb_cf_test

import (
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"testing"
)

func TestTlbGmmu(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "TlbGmmu Suite")
}
