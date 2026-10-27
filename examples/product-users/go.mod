module example.test/product-users

go 1.23.0

require github.com/NeoMei/UniHub/packages/userkit-go v0.0.0

require (
	github.com/coreos/go-oidc/v3 v3.14.1 // indirect
	github.com/go-jose/go-jose/v4 v4.0.5 // indirect
	golang.org/x/crypto v0.36.0 // indirect
	golang.org/x/oauth2 v0.28.0 // indirect
)

replace github.com/NeoMei/UniHub/packages/userkit-go => ../../packages/userkit-go
