package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/otama-jaccy/sumiq/internal/app/template"
	"github.com/otama-jaccy/sumiq/internal/config"
)

// InitParams は Init 1回分の呼び出しパラメータ。
type InitParams struct {
	// Force は既存ファイルを上書きするかどうか。sumiq.yaml / sumiq.local.yaml の
	// 両方に効く。ファイルごとの個別上書きフラグは設けない。
	Force bool
}

// Init は deps.Dir（空ならカレントディレクトリ）に sumiq.yaml / sumiq.local.yaml の
// 雛形を書き出す。
//
// Force が無い場合、書き込み前に両方の存在を確認する。どちらか一方でも
// 存在すればどちらのファイルにも書き込まずエラーで停止する。存在確認と
// 書き込みを分離しているのは、片方だけ上書きされた状態を作らないため。
func Init(deps Deps, p InitParams) error {
	sharedPath := filepath.Join(deps.Dir, config.SharedFileName)
	localPath := filepath.Join(deps.Dir, config.LocalFileName)

	if !p.Force {
		existing, err := existingFiles(sharedPath, localPath)
		if err != nil {
			return err
		}
		if len(existing) > 0 {
			return fmt.Errorf("既に存在します: %s。上書きするには --force を付けてください", strings.Join(existing, ", "))
		}
	}

	if err := os.WriteFile(sharedPath, template.Shared, 0o644); err != nil {
		return fmt.Errorf("%s の書き込みに失敗しました: %w", sharedPath, err)
	}
	if err := os.WriteFile(localPath, template.Local, 0o644); err != nil {
		return fmt.Errorf("%s の書き込みに失敗しました: %w", localPath, err)
	}

	fmt.Fprintf(deps.Err, "%s と %s を作成しました。\n", sharedPath, localPath)
	fmt.Fprintln(deps.Err, "次にやること:")
	fmt.Fprintln(deps.Err, "  - API KEY を設定する（SUMIQ_REDASH_API_KEY 環境変数、または sumiq.local.yaml の redash.api_key / api_key_command）")
	fmt.Fprintf(deps.Err, "  - %s を編集し、Redash のエンドポイント・データソース・マスクルールを書く\n", sharedPath)

	warnIfGitignoreMissing(deps)

	return nil
}

// existingFiles は paths のうち実際に存在するものを返す。
func existingFiles(paths ...string) ([]string, error) {
	var existing []string
	for _, path := range paths {
		_, err := os.Stat(path)
		switch {
		case err == nil:
			existing = append(existing, path)
		case errors.Is(err, os.ErrNotExist):
			// 無いことはエラーではない。
		default:
			return nil, fmt.Errorf("%s の確認に失敗しました: %w", path, err)
		}
	}
	return existing, nil
}

// warnIfGitignoreMissing は deps.Dir の .gitignore に sumiq.local.yaml の行
// （前後の空白は許容）が無ければ deps.Err に警告を出す。.gitignore 自体は
// 自動編集しない。読めない場合（存在しない場合を含む）も、エントリの
// 有無を確認できていない以上、無いものとして警告する側に倒す。
func warnIfGitignoreMissing(deps Deps) {
	gitignorePath := filepath.Join(deps.Dir, ".gitignore")
	data, err := os.ReadFile(gitignorePath)
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == config.LocalFileName {
				return
			}
		}
	}
	fmt.Fprintf(deps.Err, "Warning: %s に %s の行が見つかりません。追加してください。\n",
		gitignorePath, config.LocalFileName)
}
