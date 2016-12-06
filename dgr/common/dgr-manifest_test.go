package common

import (
	"testing"

	. "github.com/onsi/gomega"
)

func TestProcessManifestTemplate(t *testing.T) {
	RegisterTestingT(t)

	for _, manifest := range []string{
		`name: example.com/name:1`,
		`name: example.com/name:2
aci:
  app:
    exec:
      - /bin/bash`,
		`name: example.com/aci-test:3-1
aci:
  app:
    supplementaryGIDs: [42, 43]
  annotations:
    - {name: test, value: test2}
    - {name: test42, value: test43}
`,
	} {
		Expect(func() error {
			_, err := ProcessManifestTemplate(manifest, nil, false)
			return err
		}()).Should(Succeed())
	}
}
