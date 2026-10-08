//go:build with_acme

package include

import (
	"github.com/swysgh/mill-box/adapter/certificate"
	"github.com/swysgh/mill-box/service/acme"
)

func registerACMECertificateProvider(registry *certificate.Registry) {
	acme.RegisterCertificateProvider(registry)
}
