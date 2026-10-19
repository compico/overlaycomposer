// Package assets — иконки приложения.
package assets

import _ "embed"

//go:embed app.ico
var ICO []byte

//go:embed app.png
var PNG []byte
