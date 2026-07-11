package ask

import "fmt"

// WorkflowPath is where the notify workflow lives in the inbox repository.
const WorkflowPath = ".github/workflows/ask-notify.yml"

// WorkflowYAML renders the notify workflow (PRD §3). GitHub sends no
// notifications for one's own actions, so this workflow has github-actions
// mention the responder instead: on every new issue, and on every comment
// carrying Marker. Bot comments use GITHUB_TOKEN and therefore cannot
// re-trigger the workflow.
func WorkflowYAML(responder string) string {
	return fmt.Sprintf(`name: ask-notify
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
      contains(github.event.comment.body, '%s')
    runs-on: ubuntu-latest
    steps:
      - run: >
          gh issue comment "$NUMBER" --repo "$GITHUB_REPOSITORY"
          --body "@%s 返信をお願いします。"
        env:
          GH_TOKEN: ${{ github.token }}
          NUMBER: ${{ github.event.issue.number }}
`, Marker, responder)
}
