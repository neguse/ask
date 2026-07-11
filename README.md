# ask

AI が人間へ非同期に質問するための薄い CLI。

質問は専用 private repository(inbox)の GitHub issue になり、GitHub Actions が
回答者を mention してスマホへ通知する。人間は気づいたときに issue へコメントで
返信し、AI は依存地点まで別作業を進めてから回答を取得する。

設計と割り切りは [docs/PRD.md](docs/PRD.md) を参照。

## Install

```console
$ go build -o ~/bin/ask .
```

依存は `gh`(認証済みであること)のみ。

## Setup(一度だけ)

```console
$ ask init --inbox OWNER/ask-inbox --responder YOUR_LOGIN
```

inbox repo の作成、通知 workflow の設置、設定ファイル
(`~/.config/ask/config.toml`)の作成、通知〜返信読み取りの疎通確認まで行う。

## Usage

```console
$ ask "この破壊的変更を今回含める？" --context "互換 layer は一 release 残せる" --json
$ ask wait 42 --timeout 8m --json     # 依存地点で回答を待つ
$ ask detail 42 "補足テキスト"         # needs_detail への追加情報
$ ask show 42 --json                  # 待たずに現在の状態を見る
$ ask list                            # 未回答の質問一覧
```

回答者は issue に一コメントで返信する。`more: 欲しい情報` と返信すると
AI へ追加情報を要求できる。それ以外のコメントが最終回答になる。
