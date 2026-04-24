package apidocs

import _ "embed"

//go:embed openapi.json
var openAPIJSON []byte

//go:embed docs.html
var docsHTML []byte

func MustOpenAPIJSON() []byte {
	return append([]byte(nil), openAPIJSON...)
}

func MustDocsHTML() []byte {
	return append([]byte(nil), docsHTML...)
}
