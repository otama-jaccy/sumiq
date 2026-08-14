// Command sumiq は Redash に ad-hoc クエリを投げ、マスクした結果を出力する CLI。
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/otama-jaccy/sumiq/internal/app"
	"github.com/otama-jaccy/sumiq/internal/cli"
)

func main() {
	os.Exit(run())
}

// run は依存を組み立て、internal/cli を呼び、終了コードを返す。
// ロジックは持たない（ADR-0001）。
func run() int {
	deps := &app.Deps{
		Out: os.Stdout,
		Err: os.Stderr,
		In:  os.Stdin,
		TTY: isTerminal(os.Stdout),
	}

	if err := cli.Execute(context.Background(), os.Args[1:], deps); err != nil {
		fmt.Fprintln(deps.Err, err)
		return 1
	}
	return 0
}

// isTerminal は f が対話端末かどうかを返す。table 出力の装飾（罫線）の
// 有無だけを左右する（ADR-0004 §2）。
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
