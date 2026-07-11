# ask PRD — GitHub Issues inbox を使った雑な MVP

- Status: Draft
- Updated: 2026-07-11

## 1. 何を作るか

`ask` は、AI が人間へ非同期に質問するための薄い CLI である。

- AI は CLI から、専用の private repository(以下 inbox)へ issue を一つ立てて質問する。
- inbox に置いた小さな GitHub Actions workflow が、github-actions[bot] として回答者を mention する。
- 人間は GitHub mobile app の mention 通知から issue を開き、コメントで返信する。
- AI は必要になるまで別作業を進め、依存地点で返信を取得する。
- 会話の正本は inbox の issue である。作業 repository には何も置かない。

独自の backend、database、bot アカウント、token 管理は作らない。認証は既存の `gh` login、通知は GitHub mobile、UI と履歴は issue に任せる。

~~~text
AI (任意の作業 repo で)
  │ ask create / detail
  ▼
ask CLI ── gh api ── inbox repo の issue ──(workflow が mention)── GitHub mobile ── Human
  ▲                        │
  │ ask wait / show        │ comment で返信
  └────────────────────────┘
~~~

### なぜ workflow が要るか

GitHub は自分自身の操作に通知を送らない。AI は回答者本人の `gh` 認証で動くため、issue 本文で回答者を mention しても通知は届かない。そこで通知だけを github-actions[bot] に肩代わりさせる。workflow は「回答者を mention するコメントを一つ打つ」以外のことをしない。

同じ理由で、AI の投稿と人間の返信は author が同一 user になる。AI が書くコメントには不可視 marker `<!-- ask:ai -->` を必ず入れ、marker のないコメントを人間の返信として扱う。

### なぜ作業 repo ではなく専用 inbox か

- workflow の設置が一度きりで済み、作業 repo ごとのセットアップがゼロになる。
- public repo で作業していても、質問と回答は private な inbox から出ない。
- 質問がどこからでも一つの場所に集まり、スマホで見る場所も通知設定も一つで済む。

質問と作業 repo の紐付けは、issue 本文に CLI が自動で入れる `from: owner/repo @ branch` 行で行う。

### 常駐 process は必要か

不要である。

1. `ask create` は inbox に issue を作って直ちに終了する。
2. 人間の返信は issue コメントとして GitHub が保持する。
3. `ask wait` と `ask show` が REST API で新しいコメントを取りに行く。

CLI が動いていない間に回答されても、次回の `show` / `wait` で回収できる。代わりに「回答と同時に停止中の AI job を自動再開する」ことは諦める。

## 2. 前提と割り切り

MVP の利用条件は次のとおり。

- GitHub 上に inbox 用 private repository を一つ作れる(Actions が有効)
- 回答者一人。AI は回答者本人の `gh` 認証で動く
- 回答者のスマホに GitHub mobile app があり、mention 通知が有効
- inbox に残してよい質問だけを扱う
- 通知が Actions の起動待ちで数秒〜1分程度遅れることを許容する

次のものは作らない。

- hosted service、webhook receiver、database、常駐 process
- machine account、PAT、独自 auth、Web Push
- 作業 repo 側のセットアップ、作業 repo への書き込み
- button、reaction、slash command の解釈
- snooze、再通知 schedule、回答期限
- 複数回答者、承認 workflow
- attachment、repository context の自動収集
- exactly-once delivery や通知到達保証
- local の状態ファイル。状態は毎回 issue のコメント列から導出する

人間が今すぐ答えられない場合は、何も返信せず後から同じ issue へ回答する。`Later` UI は作らない。

作業 repo に会話 Markdown を残すことは MVP ではやらない。必要になったら `ask export` を後から足す(§8)。

## 3. Setup

一度だけ実行する。

~~~console
$ ask init --inbox neguse/ask-inbox --responder neguse
~~~

`ask init` は以下を行う。

1. `gh auth status` を確認する。
2. inbox repository がなければ private で作る。
3. inbox に `.github/workflows/ask-notify.yml` を `gh api`(contents API)で push する。
4. user config `~/.config/ask/config.toml` を書く。
5. test 質問を一件立て、スマホ通知と test 返信の読み取りまで確認する。

~~~toml
version = 1
driver = "github"
inbox = "neguse/ask-inbox"
responder = "neguse"
poll_interval_seconds = 10
~~~

config は machine ごとに置く。作業 repo には何も置かないため、以後どの repository でもそのまま `ask` が使える。

生成する workflow は次の一つだけである(responder は `ask init` が埋める)。

~~~yaml
name: ask-notify
on:
  issues:
    types: [opened]
  issue_comment:
    types: [created]
permissions:
  issues: write
jobs:
  notify:
    if: >
      github.event_name == 'issues' ||
      contains(github.event.comment.body, '<!-- ask:ai -->')
    runs-on: ubuntu-latest
    steps:
      - run: >
          gh issue comment "$NUMBER" --repo "$GITHUB_REPOSITORY"
          --body "@neguse 返信をお願いします。"
        env:
          GH_TOKEN: ${{ github.token }}
          NUMBER: ${{ github.event.issue.number }}
~~~

github-actions[bot] のコメントは `GITHUB_TOKEN` によるものなので workflow を再帰的に起動しない。token や secrets の追加設定は一切ない。

## 4. 使い方

### 4.1 質問する

~~~console
$ ask create \
    --title "本番 DB は A と B のどちらにする？" \
    --question "今週の release で採用する方式を決めてほしい" \
    --context "A は安い。B は運用が楽。AI の推奨は A。" \
    --choice "A" \
    --choice "B" \
    --json
~~~

短い質問には shorthand も使える。

~~~console
$ ask "この破壊的変更を今回含める？" --context "互換 layer は一 release 残せる"
~~~

CLI は以下を行う。

1. cwd の `git remote` / branch から `from` 行を作る(git repo 外なら省略)。
2. inbox に issue を一つ作る。issue 番号が question ID になる。
3. 直ちに終了する。`--wait` を指定した場合だけ続けて待つ。

通知の mention は workflow が打つため、CLI は issue を作る以外に何もしない。

Issue 本文の例:

~~~text
> from: neguse/someproject @ master

今週の release で採用する方式を決めてほしい。

## Context

A は安い。B は運用が楽。AI の推奨は A。

## Choices

A / B

この issue にコメントで返信してください。
追加情報が必要なら「more: 欲しい情報」と返信してください。
~~~

本文はスマホで一画面に収まる分量を目安とし、長い背景は AI に要約を求める。

成功時の JSON:

~~~json
{
  "id": 42,
  "status": "pending",
  "issue_url": "https://github.com/neguse/ask-inbox/issues/42",
  "next": "continue_work"
}
~~~

### 4.2 人間が回答する

回答者は issue に一コメントで返信する。

- `more: 移行費用を教えて`
- `more`
- `もっと詳しく`
- `もっと詳しく: 現在の利用量を教えて`
- それ以外のコメントは最終回答

回答として扱うのは、config の `responder` が author で、かつ `<!-- ask:ai -->` marker を含まないコメントだけである。github-actions[bot] のコメントと marker 付きコメントは無視する。

判定前に前後空白を除き、`more` の大小文字と半角・全角 colon(`:` / `：`)を正規化する。

### 4.3 AI が必要なときだけ待つ

質問の回答なしで進められる間は、AI は何もしない。依存地点へ来たら:

~~~console
$ ask wait 42 --timeout 8m --json
~~~

`wait` は issue のコメントを 10 秒ごとに取得し、その間は sleep する。rate limit(403 / 429)を受けたら `Retry-After` に従う。

`--timeout` の既定は 8m とする。AI harness の tool timeout(例: Claude Code の Bash は最大 10 分)より短く収め、timeout 後に再度 `wait` して継続する使い方を標準とする。

終了結果は次の三つだけでよい。

- `answered`
- `needs_detail`
- `timeout`

Timeout や Ctrl-C で issue は消えず、後から再度 `wait` / `show` できる。

### 4.4 追加情報を返す

人間が `more` 系の返信をすると、`wait` は不足内容を返す。内容が省略された場合は単に追加 context を求められたものとする。

~~~json
{
  "id": 42,
  "status": "needs_detail",
  "request": "移行費用を教えて",
  "next": "ask detail 42"
}
~~~

AI は同じ issue へ marker 付きコメントで補足する。workflow がそれを検知して回答者を再度 mention する。

~~~console
$ ask detail 42 "A は約3万円、B は約8万円です"
$ ask wait 42 --timeout 8m --json
~~~

### 4.5 後から確認する

~~~console
$ ask show 42 --json
$ ask list
~~~

`ask show` は issue のコメント列から現在の状態と回答を返す。待たない `wait` と同じ判定を使う。`ask list` は inbox の open issue(未回答の質問)を一覧する。

`answered` を観測した `show` / `wait` は issue を close する。close 済みの issue も `show` で読める。回答後に人間が補足コメントを足した場合も、次の `show` が最新の最終回答を返す。

## 5. 状態

状態は三つで足りる。local に状態ファイルを持たず、毎回コメント列から導出する。

~~~text
pending ── human asks more ──> needs_detail
   ▲                              │
   └────── AI adds detail ────────┘

pending ── human answers ──> answered
~~~

導出規則: bot と marker 付きコメントを除いた回答者コメントのうち最後のものを見る。

- 存在しない → `pending`
- `more` 系 → `needs_detail`(ただしそれより後に AI の marker 付きコメントがあれば `pending` に戻る)
- それ以外 → `answered`(そのコメントが Answer)

要件:

- 会話の正本は issue であり、AI / Human の全 turn と時刻は GitHub が保持する。
- 状態導出は決定的で、`show` を何度実行しても同じ結果になる。
- issue URL からいつでも元の会話を開ける。

## 6. 実装

小さな Go binary 一つとする。single binary で Windows / Linux の両方へクロスコンパイルできることを重視する。専用 server はない。

GitHub へのアクセスはすべて `gh api` 経由とし、独自の認証・token 管理を持たない。使う API は実質五つだけである。

1. repository を作る(`ask init` のみ)。
2. issue を作る。
3. issue へコメントを作る。
4. issue のコメント一覧を取得する。
5. issue を close する。

障害対応も最小限とする。

- GitHub API error はそのまま失敗として返す。
- rate limit(403 / 429 + `Retry-After`)だけは待って再試行する。
- 応答が曖昧な network failure で issue が重複した場合は手で close する。
- GitHub outage 中の独自 queue は持たない。
- notification delivery は GitHub と端末設定次第であり保証しない。Actions の起動待ちによる通知遅延(数秒〜1分)も許容する。

## 7. 受入条件

1. `ask init` の test 質問で github-actions[bot] の mention 通知が回答者のスマホへ届き、test 返信を CLI が読める。
2. `ask create` が数秒で戻り、inbox に issue を一つ作る。作業 repo には何も書かない。
3. CLI が停止中でも issue の回答は残り、後の `ask show` / `wait` で取得できる。
4. 通常コメントを `answered`、`more` 系を `needs_detail` として取得できる。
5. AI の補足(marker 付き)で再通知が飛び、人間の最終回答が同じ issue に順番どおり残る。
6. `wait` は sleep + REST polling で動き、timeout / Ctrl-C 後も再開できる。
7. `show` を何度実行しても状態と回答が変わらない(close の冪等性を含む)。
8. 新しい credential を一つも作らない(`gh` login と `GITHUB_TOKEN` のみ)。marker 付きコメントと bot コメントを回答として扱わない。
9. どの作業 repository からでも、追加セットアップなしで同じ inbox に質問できる。

## 8. この案を卒業する条件

以下が本当に必要になった時だけ、機能や別の伝送路を検討する。

- 回答直後に停止中の AI job を自動再開したい(webhook receiver が要る)。
- 会話を作業 repo に Markdown で残したい(`ask export` を足す)。
- Actions 経由の通知遅延や GitHub mobile の通知信頼性が実用に耐えない(Discord bot driver を追加する。旧案は git history にある)。
- 複数の回答者や routing が要る。

config の `driver` はその日のためにある。MVP の時点では `github` だけを実装する。

## 9. GitHub 公式資料

- [Issues REST API](https://docs.github.com/en/rest/issues/issues) — issue の作成と close。
- [Issue comments REST API](https://docs.github.com/en/rest/issues/comments) — コメントの作成と取得。
- [Events that trigger workflows](https://docs.github.com/en/actions/using-workflows/events-that-trigger-workflows) — `issues` / `issue_comment` event。
- [Automatic token authentication](https://docs.github.com/en/actions/security-guides/automatic-token-authentication) — `GITHUB_TOKEN` の権限と、bot 操作が workflow を再帰起動しないこと。
- [Rate limits for the REST API](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api) — 403 / 429 と `Retry-After`。
- [About notifications](https://docs.github.com/en/subscriptions-and-notifications/get-started/about-notifications) — mention と通知の仕組み(自分自身の操作は通知されない)。
