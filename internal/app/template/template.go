// Package template は `sumiq init` が書き出す設定ファイルの雛形を保持する。
//
// go:embed はパッケージディレクトリ配下のファイルしか埋め込めず、`../` で
// 親ディレクトリを参照できないため、リポジトリルート直下の
// sumiq.yaml.example / sumiq.local.yaml.example を直接埋め込むことはできない。
// そのためこのパッケージに複製を置く。2つがズレないことは
// internal/app の対応するテストでバイト列を比較して担保する。
package template

import _ "embed"

// Shared は sumiq.yaml の雛形。
//
//go:embed sumiq.yaml.tmpl
var Shared []byte

// Local は sumiq.local.yaml の雛形。
//
//go:embed sumiq.local.yaml.tmpl
var Local []byte
